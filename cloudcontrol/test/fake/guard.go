// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

// The guard: nothing in these tests may reach real AWS. The Upjet spike in
// highfivelabs/catalog (docs/terraform-leverage.md, spike 1) sent signed
// requests to the public Organizations endpoint because an endpoint block
// was mistyped, and nothing failed until AWS answered. Here three things
// stand in the way, each enough on its own:
//
//  1. LocalOnly refuses any endpoint that is not a loopback address; the
//     harness checks the fake's and moto's URLs with it before anything
//     starts;
//  2. HTTPClient dials loopback addresses only, by the literal host, before
//     any name is resolved; every SDK client the tests and the fake make
//     uses it, so a missing endpoint override fails at the dial;
//  3. the manager runs as a separate process with an environment built from
//     nothing (ChildEnv): fake credentials, and HTTP(S)_PROXY pointing at a
//     closed local port, so any request it makes to a non-loopback host
//     (Go never proxies loopback) fails at the proxy.
//
// The credentials are moto's conventional "testing"/"testing"; no AWS
// account accepts them.

// FakeAccessKeyID and FakeSecretAccessKey are the only credentials used.
const (
	FakeAccessKeyID     = "testing"
	FakeSecretAccessKey = "testing"
	// DeadProxy is a local port nothing listens on (discard, RFC 863).
	DeadProxy = "http://127.0.0.1:9"
)

func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// LocalOnly returns an error unless raw is an http(s) URL on a loopback
// address.
func LocalOnly(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("endpoint %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("endpoint %q: not http(s)", raw)
	}
	if !loopbackHost(u.Hostname()) {
		return fmt.Errorf("endpoint %q is not a loopback address: refusing to run, these tests must never reach AWS", raw)
	}
	return nil
}

// HTTPClient is an HTTP client that refuses to dial anything but a loopback
// address, and never uses a proxy.
func HTTPClient() *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Client{Transport: &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if !loopbackHost(host) {
				return nil, fmt.Errorf("refusing to dial %s: only loopback addresses are allowed in these tests", addr)
			}
			return d.DialContext(ctx, network, addr)
		},
	}}
}

// AWSConfig is an SDK configuration for endpoint, which must be local,
// with the fake credentials and the guarded HTTP client.
func AWSConfig(endpoint, region string) (aws.Config, error) {
	if err := LocalOnly(endpoint); err != nil {
		return aws.Config{}, err
	}
	return aws.Config{
		Region:       region,
		Credentials:  credentials.NewStaticCredentialsProvider(FakeAccessKeyID, FakeSecretAccessKey, ""),
		BaseEndpoint: aws.String(endpoint),
		HTTPClient:   HTTPClient(),
	}, nil
}

// ChildEnv is the whole environment of a process the tests start: fake
// credentials, no shared config or profile, and a dead proxy for any host
// that is not loopback.
func ChildEnv(extra ...string) []string {
	return append([]string{
		"AWS_ACCESS_KEY_ID=" + FakeAccessKeyID,
		"AWS_SECRET_ACCESS_KEY=" + FakeSecretAccessKey,
		"AWS_SHARED_CREDENTIALS_FILE=/dev/null",
		"AWS_CONFIG_FILE=/dev/null",
		"AWS_EC2_METADATA_DISABLED=true",
		"HTTP_PROXY=" + DeadProxy,
		"HTTPS_PROXY=" + DeadProxy,
		"http_proxy=" + DeadProxy,
		"https_proxy=" + DeadProxy,
		"NO_PROXY=",
		"no_proxy=",
	}, extra...)
}
