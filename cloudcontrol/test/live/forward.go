// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

//go:build live

package live

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// Forwarder is the ACK-kind tests' (tests/e2e) counterpart of the
// Recorder: a loopback URL that is the one endpoint of the managers and of
// the tests' own SDK clients, as moto is on a fake run. Every request is
// signed by its client with pkg/fakeaws's fake keys; the forwarder reads
// the service and region from that signature's scope, signs the request
// again with the run's federated credentials and sends it to the real
// endpoint of that service in the sandbox account. Only the services in
// ForwardedServices are forwarded; anything else is refused. So the
// managers still run under pkg/fakeaws (fake keys, dead proxy, a loopback
// endpoint) and never hold real credentials: only this process does.
type Forwarder struct {
	cfg     aws.Config
	account string
	srv     *httptest.Server
	client  *http.Client

	mu    sync.Mutex
	calls map[string]int // "service Action" -> count, for the run's log
}

// ForwardedServices are the SigV4 signing names a forwarder sends on: the
// services of the ACK-generated kinds and of the tests' fixtures.
var ForwardedServices = map[string]bool{
	"application-autoscaling": true, "autoscaling": true, "ec2": true, "ecr": true,
	"ecs": true, "elasticloadbalancing": true, "events": true, "iam": true, "kms": true,
	"lambda": true, "logs": true, "organizations": true, "rds": true, "s3": true,
	"scheduler": true, "secretsmanager": true, "sqs": true, "ssm": true, "sts": true,
	"wafv2": true,
}

// NewForwarder makes a forwarder for the run.
func NewForwarder(r *Run) *Forwarder {
	return &Forwarder{cfg: r.Config, account: r.Account, client: &http.Client{Timeout: 120 * time.Second}, calls: map[string]int{}}
}

// Start listens on loopback and returns the URL.
func (f *Forwarder) Start() string {
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	return f.srv.URL
}

// Close stops it.
func (f *Forwarder) Close() { f.srv.Close() }

// Calls is how many requests of each "service Action" were forwarded.
func (f *Forwarder) Calls() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for k, v := range f.calls {
		out[k] = v
	}
	return out
}

var sigScope = regexp.MustCompile(`Credential=[^/]+/\d{8}/([^/]+)/([^/]+)/aws4_request`)

// target is the real host and signing region of a request a client signed
// for service in region.
func (f *Forwarder) target(service, region string, req *http.Request) (host, signRegion string, err error) {
	if !ForwardedServices[service] {
		return "", "", fmt.Errorf("live forwarder: service %q is not forwarded", service)
	}
	switch service {
	case "iam":
		return "iam.amazonaws.com", "us-east-1", nil
	case "organizations":
		return "organizations.us-east-1.amazonaws.com", "us-east-1", nil
	case "ecr":
		return "api.ecr." + region + ".amazonaws.com", region, nil
	case "s3":
		// S3 Control is signed as s3; its paths start with the API version
		// and it is addressed at the account's own host.
		if strings.HasPrefix(req.URL.Path, "/v20180820/") {
			acct := req.Header.Get("X-Amz-Account-Id")
			if acct != f.account {
				return "", "", fmt.Errorf("live forwarder: S3 Control for account %q, not the sandbox account", acct)
			}
			return acct + ".s3-control." + region + ".amazonaws.com", region, nil
		}
		return "s3." + region + ".amazonaws.com", region, nil
	}
	return service + "." + region + ".amazonaws.com", region, nil
}

var dropForwardHeaders = []string{"Authorization", "X-Amz-Date", "X-Amz-Security-Token", "X-Amz-Content-Sha256", "Content-Length", "Host"}

func (f *Forwarder) serve(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	m := sigScope.FindStringSubmatch(req.Header.Get("Authorization"))
	if m == nil {
		http.Error(w, "live forwarder: an unsigned request (no SigV4 scope to sign it again with)", http.StatusForbidden)
		return
	}
	region, service := m[1], m[2]
	host, signRegion, err := f.target(service, region, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	f.count(service, req, body)
	out, err := http.NewRequestWithContext(req.Context(), req.Method, "https://"+host+req.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	out.Header = req.Header.Clone()
	for _, h := range dropForwardHeaders {
		out.Header.Del(h)
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	if service == "s3" {
		out.Header.Set("X-Amz-Content-Sha256", hash)
	}
	creds, err := f.cfg.Credentials.Retrieve(req.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if err := v4.NewSigner().SignHTTP(context.Background(), creds, out, hash, service, signRegion, time.Now()); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	resp, err := f.client.Do(out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

var queryAction = regexp.MustCompile(`(?:^|&)Action=([A-Za-z0-9]+)`)

func (f *Forwarder) count(service string, req *http.Request, body []byte) {
	action := req.Header.Get("X-Amz-Target")
	if i := strings.LastIndex(action, "."); i >= 0 {
		action = action[i+1:]
	}
	if action == "" {
		if m := queryAction.FindSubmatch(body); m != nil {
			action = string(m[1])
		} else if m := queryAction.FindStringSubmatch(req.URL.RawQuery); m != nil {
			action = m[1]
		} else {
			action = req.Method + " " + req.URL.Path
			if len(action) > 60 {
				action = action[:60]
			}
		}
	}
	f.mu.Lock()
	f.calls[service+" "+action]++
	f.mu.Unlock()
}
