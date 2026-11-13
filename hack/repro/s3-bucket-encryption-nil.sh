#!/usr/bin/env bash
# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
#
# Reproduce ACK s3-controller's nil-pointer panic reading a Bucket that has
# no default encryption (docs/plan.md, section 8, item 15):
# customFindBucket tolerates GetBucketEncryption's
# ServerSideEncryptionConfigurationNotFoundError and then dereferences the
# nil response (pkg/resource/bucket/hook.go:487 at v1.15.0).
#
#   hack/repro/s3-bucket-encryption-nil.sh [S3_CONTROLLER_REF]   (default v1.15.0)
#
# Linux with apt (an offload VM): installs AWS Emulator with
# hack/aws-emulator.sh, clones s3-controller at the ref into a temporary
# directory, adds one test to its pkg/resource/bucket package and runs it.
# The test starts the emulator (telemetry off, loopback only, its own home),
# makes one bucket with the emulator's keys, and calls the controller's own
# read (sdkFind, which is customFindBucket): on the new bucket, after
# DeleteBucketEncryption, through a loopback proxy that answers
# GetBucketEncryption with S3's documented
# ServerSideEncryptionConfigurationNotFoundError (everything else goes to
# the emulator), and after PutBucketEncryption (the control). It prints what
# GetBucketEncryption answers and whether the read panicked; it fails when
# the documented answer does not panic. No AWS account is reached: the client's only
# endpoint is the emulator's loopback URL. Nothing here changes this
# repository or its module.
set -euo pipefail

REF="${1:-v1.15.0}"
here="$(cd "$(dirname "$0")/../.." && pwd)"
"$here/hack/aws-emulator.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
git clone --quiet --depth 1 --branch "$REF" https://github.com/aws-controllers-k8s/s3-controller "$work/s3-controller"
cd "$work/s3-controller"
echo "s3-controller $REF at $(git rev-parse HEAD)"

cat > pkg/resource/bucket/zz_repro_test.go <<'EOF'
package bucket

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	ackmetrics "github.com/aws-controllers-k8s/runtime/pkg/metrics"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/go-logr/logr"

	svcapitypes "github.com/aws-controllers-k8s/s3-controller/apis/v1alpha1"
)

func emulator(t *testing.T) (endpoint string, hc *http.Client, creds aws.Credentials) {
	home := t.TempDir()
	cmd := exec.Command("aws-emulator", "--port", "0", "--telemetry", "off")
	cmd.Env = []string{"HOME=" + home, "EMULATOR_TELEMETRY=off", "PATH=" + os.Getenv("PATH"),
		"HTTPS_PROXY=http://127.0.0.1:9", "HTTP_PROXY=http://127.0.0.1:9", "NO_PROXY=127.0.0.1,localhost"}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	var info struct {
		Message, EndpointURL, CAFile string
	}
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		f, _ := m["fields"].(map[string]any)
		if f == nil {
			f = m
		}
		if u, _ := f["endpoint_url"].(string); u != "" {
			info.EndpointURL = u
			info.CAFile, _ = f["ca_file"].(string)
			break
		}
	}
	go func() {
		for sc.Scan() {
		}
	}()
	if !strings.HasPrefix(info.EndpointURL, "https://127.0.0.1:") && !strings.HasPrefix(info.EndpointURL, "https://localhost:") {
		t.Fatalf("emulator endpoint %q is not loopback", info.EndpointURL)
	}
	ca, err := os.ReadFile(info.CAFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	hc = &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{
		Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(info.CAFile), "emulator_credentials"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^\s*(aws_access_key_id|aws_secret_access_key)\s*=\s*(\S+)`)
	for _, m := range re.FindAllStringSubmatch(string(b), -1) {
		if m[1] == "aws_access_key_id" {
			creds.AccessKeyID = m[2]
		} else {
			creds.SecretAccessKey = m[2]
		}
	}
	for i := 0; i < 100; i++ {
		if resp, err := hc.Get(info.EndpointURL + "/__admin/health"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return info.EndpointURL, hc, creds
}

func read(rm *resourceManager, name string) (err error, panicked any, stack string) {
	defer func() {
		if p := recover(); p != nil {
			panicked, stack = p, string(debug.Stack())
		}
	}()
	_, err = rm.sdkFind(context.Background(), &resource{ko: &svcapitypes.Bucket{Spec: svcapitypes.BucketSpec{Name: aws.String(name)}}})
	return err, nil, ""
}

func s3client(endpoint string, hc *http.Client, creds aws.Credentials) *svcsdk.Client {
	return svcsdk.New(svcsdk.Options{
		Region: "us-east-1", BaseEndpoint: aws.String(endpoint), UsePathStyle: true, HTTPClient: hc,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) { return creds, nil }),
	})
}

func report(rm *resourceManager, name, what string) any {
	err, p, stack := read(rm, name)
	fmt.Printf("sdkFind, %s: err=%v panic=%v\n", what, err, p)
	if p != nil {
		for _, l := range strings.Split(stack, "\n") {
			if strings.Contains(l, "hook.go") || strings.Contains(l, "sdk.go") {
				fmt.Println("  at", strings.TrimSpace(l))
			}
		}
	}
	return p
}

// noEncryption answers GetBucketEncryption (GET /<bucket>?encryption) as S3
// does for a bucket without a default encryption configuration, which S3
// documents and which buckets made before 2023 and S3-compatible stores
// still give; every other request goes to the emulator unchanged.
func noEncryption(t *testing.T, endpoint string, hc *http.Client) string {
	target, _ := url.Parse(endpoint)
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.Transport = hc.Transport
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["encryption"]; ok && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>ServerSideEncryptionConfigurationNotFoundError</Code><Message>The server side encryption configuration was not found</Message></Error>`)
			return
		}
		// The Host the client signed stays: the emulator checks the
		// signature over it.
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestReproEncryptionNil(t *testing.T) {
	endpoint, hc, creds := emulator(t)
	client := s3client(endpoint, hc, creds)
	ctx := context.Background()
	const name = "repro-no-encryption"
	if _, err := client.CreateBucket(ctx, &svcsdk.CreateBucketInput{Bucket: aws.String(name)}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	rm := &resourceManager{sdkapi: client, metrics: ackmetrics.NewMetrics("s3"), log: logr.Discard()}

	// 1. The emulator as it is: a new bucket.
	_, gerr := client.GetBucketEncryption(ctx, &svcsdk.GetBucketEncryptionInput{Bucket: aws.String(name)})
	fmt.Printf("emulator, GetBucketEncryption on a new bucket: err=%v\n", gerr)
	report(rm, name, "emulator, new bucket")

	// 2. The emulator after DeleteBucketEncryption (on AWS the bucket
	// falls back to SSE-S3).
	_, derr := client.DeleteBucketEncryption(ctx, &svcsdk.DeleteBucketEncryptionInput{Bucket: aws.String(name)})
	_, gerr = client.GetBucketEncryption(ctx, &svcsdk.GetBucketEncryptionInput{Bucket: aws.String(name)})
	fmt.Printf("emulator, after DeleteBucketEncryption (err=%v), GetBucketEncryption: err=%v\n", derr, gerr)
	pDeleted := report(rm, name, "emulator, after DeleteBucketEncryption")

	// 3. S3's documented answer for a bucket without default encryption.
	plain := &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{Proxy: nil}}
	rmNone := &resourceManager{sdkapi: s3client(noEncryption(t, endpoint, hc), plain, creds), metrics: ackmetrics.NewMetrics("s3"), log: logr.Discard()}
	_, gerr = rmNone.sdkapi.GetBucketEncryption(ctx, &svcsdk.GetBucketEncryptionInput{Bucket: aws.String(name)})
	fmt.Printf("ServerSideEncryptionConfigurationNotFoundError answered, GetBucketEncryption: err=%v\n", gerr)
	pNone := report(rmNone, name, "ServerSideEncryptionConfigurationNotFoundError answered")

	// 4. Control: with an encryption configuration, no panic.
	if _, err := client.PutBucketEncryption(ctx, &svcsdk.PutBucketEncryptionInput{Bucket: aws.String(name),
		ServerSideEncryptionConfiguration: &s3types.ServerSideEncryptionConfiguration{Rules: []s3types.ServerSideEncryptionRule{{
			ApplyServerSideEncryptionByDefault: &s3types.ServerSideEncryptionByDefault{SSEAlgorithm: s3types.ServerSideEncryptionAes256}}}}}); err != nil {
		t.Fatalf("PutBucketEncryption: %v", err)
	}
	pPut := report(rm, name, "emulator, after PutBucketEncryption")
	fmt.Printf("REPRODUCED emulator-after-delete=%v documented-error=%v\n", pDeleted != nil, pNone != nil)
	if pNone == nil {
		t.Errorf("no panic on S3's documented answer: not reproduced")
	}
	if pPut != nil {
		t.Errorf("panic with an encryption configuration: %v", pPut)
	}
}
EOF

go test ./pkg/resource/bucket/ -run TestReproEncryptionNil -count=1 -v
