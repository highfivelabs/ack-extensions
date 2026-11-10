// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package fakeaws is the one setting that points everything a test starts
// at a fake AWS, and the check that refuses to start anything that could
// still reach real AWS (docs/cloudcontrol.md section 6).
//
// The setting is a single URL, an Endpoint. From it come every AWS
// endpoint a process can use, together: ACK's runtime flags for the
// service and for STS (--aws-endpoint-url, --aws-identity-endpoint-url),
// and AWS_ENDPOINT_URL, which every client the SDK's default configuration
// makes reads (STS for an assumed role, any service client a handler or a
// hook creates). Beside them: fake credentials, the instance metadata
// service off, and every proxy variable at a closed local port, so a
// request to any host the endpoint does not cover dies at the proxy.
//
// Verify is the check. It is given a process's whole command line and
// environment (or a Kubernetes container's, with $(VAR) references
// expanded), and fails unless every endpoint is set, every one is the
// fake's (local) and none is an AWS domain, the proxy is the dead one, and
// no credential source other than the fake keys is configured. Every
// process the tests start goes through Command, which calls it; the
// operator calls it on every manager it makes in fake mode; and
// tests/operator/run.sh runs it on every AWS client in the kind cluster.
// hack/noaws checks, in task check, that no test path goes around it.
package fakeaws

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

const (
	// AccessKeyID and SecretAccessKey are moto's conventional fake
	// credentials, the only ones used; no AWS account accepts them.
	AccessKeyID     = "testing"
	SecretAccessKey = "testing"
	// DeadProxy is a local port nothing listens on (discard, RFC 863).
	DeadProxy = "http://127.0.0.1:9"
)

// AWSDomains are the DNS suffixes of every AWS partition, regular and dual
// stack (aws-sdk-go-v2 internal/endpoints/awsrulesfn/partitions.json at
// the version go.mod pins). No endpoint in a test may be under one.
var AWSDomains = []string{
	"amazonaws.com", "amazonaws.com.cn", "amazonaws.eu",
	"api.aws", "api.amazonwebservices.com.cn", "api.amazonwebservices.eu",
	"c2s.ic.gov", "sc2s.sgov.gov", "csp.hci.ic.gov", "cloud.adc-e.uk",
	"api.aws.ic.gov", "api.aws.scloud", "api.aws.hci.ic.gov", "api.cloud-aws.adc-e.uk",
}

// RealEndpoint is a real AWS endpoint, for the tests that show it is
// refused; hack/noaws refuses AWS endpoints written anywhere else in a
// test path.
const RealEndpoint = "https://sts.us-west-2.amazonaws.com"

// IsAWSHost reports whether host is under an AWS partition's domain.
func IsAWSHost(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	for _, d := range AWSDomains {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}

// Scope is where a fake may live.
type Scope int

const (
	// Loopback: a fake on this machine (the Go tests: moto, the Cloud
	// Control fake).
	Loopback Scope = iota
	// Cluster: a fake in the Kubernetes cluster the process runs in (kind:
	// moto as a Service), or on loopback.
	Cluster
)

func (s Scope) String() string {
	if s == Cluster {
		return "in-cluster or loopback"
	}
	return "loopback"
}

func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// clusterHost is a name only a cluster's DNS answers, or a private address.
func clusterHost(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	for _, s := range []string{".svc", ".svc.cluster.local", ".cluster.local"} {
		if strings.HasSuffix(h, s) {
			return true
		}
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsPrivate()
}

func (s Scope) allows(host string) bool {
	if IsAWSHost(host) {
		return false
	}
	return loopbackHost(host) || (s == Cluster && clusterHost(host))
}

// Endpoint is the single setting: the URL every AWS endpoint resolves to.
type Endpoint struct {
	u     *url.URL
	scope Scope
}

// Parse makes an Endpoint, refusing a URL that is not http(s), not in
// scope, or under an AWS domain.
func Parse(raw string, scope Scope) (Endpoint, error) {
	u, err := checkURL(raw, scope)
	if err != nil {
		return Endpoint{}, err
	}
	return Endpoint{u: u, scope: scope}, nil
}

// MustParse is Parse for a URL a test has just made; it panics on error.
func MustParse(raw string, scope Scope) Endpoint {
	e, err := Parse(raw, scope)
	if err != nil {
		panic(err)
	}
	return e
}

func checkURL(raw string, scope Scope) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("endpoint %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("endpoint %q: not http(s)", raw)
	}
	if IsAWSHost(u.Hostname()) {
		return nil, fmt.Errorf("endpoint %q is an AWS domain: refusing to start, tests must never reach AWS", raw)
	}
	if !scope.allows(u.Hostname()) {
		return nil, fmt.Errorf("endpoint %q is not %s: refusing to start, tests must never reach AWS", raw, scope)
	}
	return u, nil
}

// String is the endpoint's URL.
func (e Endpoint) String() string {
	if e.u == nil {
		return ""
	}
	return e.u.String()
}

// Host is the endpoint's host name, without the port.
func (e Endpoint) Host() string { return e.u.Hostname() }

// Scope is where the endpoint may be.
func (e Endpoint) Scope() Scope { return e.scope }

// Args are ACK's runtime flags for every endpoint it has: the service's
// and STS's, both the fake.
func (e Endpoint) Args() []string {
	s := e.String()
	return []string{
		"--aws-endpoint-url=" + s,
		"--aws-identity-endpoint-url=" + s,
		"--allow-unsafe-aws-endpoint-urls",
	}
}

// Env is the AWS part of a process's environment (NAME=value): every SDK
// endpoint at the fake, the fake credentials, the metadata service off,
// and the dead proxy for every host but the fake's and noProxy (in a
// cluster, the Kubernetes API's address; Go never proxies loopback).
// On loopback it also hides shared configuration files.
func (e Endpoint) Env(noProxy ...string) []string {
	s := e.String()
	np := []string{}
	if e.scope == Cluster && !loopbackHost(e.Host()) {
		np = append(np, e.Host())
	}
	np = append(np, noProxy...)
	out := []string{
		"AWS_ENDPOINT_URL=" + s,
		"AWS_ACCESS_KEY_ID=" + AccessKeyID,
		"AWS_SECRET_ACCESS_KEY=" + SecretAccessKey,
		"AWS_EC2_METADATA_DISABLED=true",
	}
	if e.scope == Loopback {
		out = append(out, "AWS_SHARED_CREDENTIALS_FILE=/dev/null", "AWS_CONFIG_FILE=/dev/null")
	}
	return append(out, proxyEnv(strings.Join(np, ","))...)
}

func proxyEnv(noProxy string) []string {
	return []string{
		"HTTP_PROXY=" + DeadProxy, "HTTPS_PROXY=" + DeadProxy,
		"http_proxy=" + DeadProxy, "https_proxy=" + DeadProxy,
		"NO_PROXY=" + noProxy, "no_proxy=" + noProxy,
	}
}

// Isolated is the whole environment of a process the tests start that is
// not an AWS client (moto itself): the fake credentials, no shared
// configuration, and the dead proxy; extra (NAME=value) after.
func Isolated(extra ...string) []string {
	return append(append([]string{
		"AWS_ACCESS_KEY_ID=" + AccessKeyID,
		"AWS_SECRET_ACCESS_KEY=" + SecretAccessKey,
		"AWS_SHARED_CREDENTIALS_FILE=/dev/null",
		"AWS_CONFIG_FILE=/dev/null",
		"AWS_EC2_METADATA_DISABLED=true",
	}, proxyEnv("")...), extra...)
}

// HTTPClient refuses to dial anything but a loopback address, checked on
// the literal host before any name is resolved, and never uses a proxy.
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

// Config is the only SDK configuration tests make: the fake at raw (on
// loopback), the fake credentials and HTTPClient.
func Config(raw, region string) (aws.Config, error) {
	e, err := Parse(raw, Loopback)
	if err != nil {
		return aws.Config{}, err
	}
	return e.Config(region), nil
}

// Config is an SDK configuration for this (loopback) endpoint.
func (e Endpoint) Config(region string) aws.Config {
	return aws.Config{
		Region:       region,
		Credentials:  credentials.NewStaticCredentialsProvider(AccessKeyID, SecretAccessKey, ""),
		BaseEndpoint: aws.String(e.String()),
		HTTPClient:   HTTPClient(),
	}
}

// Command is a process that is an AWS client (a manager, ACK's runtime):
// args, then e's endpoint flags (a later flag wins), with an environment
// built from nothing: e's, then extra (which may not touch AWS_*, proxy
// or endpoint variables). It is verified before it is returned.
func Command(ctx context.Context, e Endpoint, bin string, args []string, extra ...string) (*exec.Cmd, error) {
	for _, kv := range extra {
		k, _, _ := strings.Cut(kv, "=")
		if guardedVar(k) {
			return nil, fmt.Errorf("fakeaws.Command: %s is the endpoint's to set, not the caller's", k)
		}
	}
	all := append(append([]string{}, args...), e.Args()...)
	env := append(e.Env(), extra...)
	if err := Verify(all, env, e.scope); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, all...)
	cmd.Env = env
	return cmd, nil
}

func guardedVar(k string) bool {
	u := strings.ToUpper(k)
	return strings.HasPrefix(u, "AWS_") || u == "HTTP_PROXY" || u == "HTTPS_PROXY" || u == "NO_PROXY" || u == "ALL_PROXY"
}

// Credential sources other than the fake keys. Pod Identity's and IRSA's
// are injected by EKS at admission, so on kind they are never present.
var forbiddenVars = []string{
	"AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_SESSION_TOKEN",
	"AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
	"AWS_CONTAINER_AUTHORIZATION_TOKEN", "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE",
	"AWS_IGNORE_CONFIGURED_ENDPOINT_URLS", "ALL_PROXY", "all_proxy",
}

var varRef = regexp.MustCompile(`\$\(([A-Za-z_][A-Za-z0-9_]*)\)`)

// Verify fails unless a process with this command line and environment
// (NAME=value; the last of a name wins, as for exec and Kubernetes) can
// reach nothing but the fake: ACK's runtime's service and STS endpoints
// and the SDK's AWS_ENDPOINT_URL all set, every endpoint variable or flag
// in scope and none an AWS domain, the dead proxy, no exceptions to it
// beyond local names, and no credential source but the fake keys.
// Kubernetes-style $(VAR) references in args are expanded from env.
func Verify(args, env []string, scope Scope) error {
	vars := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		vars[k] = v
	}
	expand := func(s string) string {
		return varRef.ReplaceAllStringFunc(s, func(m string) string {
			if v, ok := vars[m[2:len(m)-1]]; ok {
				return v
			}
			return m
		})
	}
	var errs []string
	bad := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	// Every endpoint flag, in both spellings; the last one wins.
	flags := map[string]string{}
	for i := 0; i < len(args); i++ {
		name, val, hasVal := strings.Cut(args[i], "=")
		// --aws-endpoint-url, --aws-identity-endpoint-url, and any other
		// flag naming an endpoint URL (not --allow-unsafe-aws-endpoint-urls).
		if !strings.HasPrefix(name, "-") || !strings.HasSuffix(strings.ToLower(name), "endpoint-url") {
			continue
		}
		name = "--" + strings.TrimLeft(name, "-")
		if !hasVal && i+1 < len(args) {
			i++
			val = args[i]
		}
		flags[name] = expand(val)
	}
	endpoints := map[string]string{}
	for _, f := range []string{"--aws-endpoint-url", "--aws-identity-endpoint-url"} {
		if _, ok := flags[f]; !ok {
			bad("%s is not set: ACK's runtime would use AWS's own endpoint", f)
		}
	}
	for f, v := range flags {
		endpoints[f] = v
	}
	if _, ok := vars["AWS_ENDPOINT_URL"]; !ok {
		bad("AWS_ENDPOINT_URL is not set: an SDK client made from the default configuration would use AWS's own endpoint")
	}
	for k, v := range vars {
		if strings.Contains(strings.ToUpper(k), "ENDPOINT_URL") {
			endpoints[k] = v
		}
	}
	for _, k := range sortedKeys(endpoints) {
		if _, err := checkURL(endpoints[k], scope); err != nil {
			bad("%s: %v", k, err)
		}
	}

	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY"} {
		if vars[k] != DeadProxy {
			bad("%s is %q, not the dead proxy %s", k, vars[k], DeadProxy)
		}
		if v, ok := vars[strings.ToLower(k)]; ok && v != DeadProxy {
			bad("%s is %q, not the dead proxy %s", strings.ToLower(k), v, DeadProxy)
		}
	}
	allowedNoProxy := map[string]bool{}
	for _, v := range endpoints {
		if u, err := url.Parse(v); err == nil {
			allowedNoProxy[strings.ToLower(u.Hostname())] = true
		}
	}
	for _, k := range []string{"NO_PROXY", "no_proxy"} {
		for _, h := range strings.Split(vars[k], ",") {
			if h = strings.TrimSpace(h); h != "" && !noProxyOK(h, scope, allowedNoProxy) {
				bad("%s has %q: only the fake's host and local addresses may skip the dead proxy", k, h)
			}
		}
	}

	for _, k := range forbiddenVars {
		if v, ok := vars[k]; ok && v != "" {
			bad("%s is set: only the fake credentials and endpoint may be configured", k)
		}
	}
	if v, ok := vars["AWS_ACCESS_KEY_ID"]; ok && v != AccessKeyID {
		bad("AWS_ACCESS_KEY_ID is not the fake key %q", AccessKeyID)
	}
	if len(errs) > 0 {
		return errors.New("refusing to start, it could reach real AWS:\n  " + strings.Join(errs, "\n  "))
	}
	return nil
}

func noProxyOK(h string, scope Scope, endpointHosts map[string]bool) bool {
	h = strings.ToLower(h)
	if h == "*" || IsAWSHost(strings.TrimPrefix(h, ".")) {
		return false
	}
	if endpointHosts[h] || loopbackHost(h) {
		return true
	}
	if scope != Cluster {
		return false
	}
	if _, n, err := net.ParseCIDR(h); err == nil {
		return n.IP.IsPrivate() || n.IP.IsLoopback()
	}
	return clusterHost(strings.TrimPrefix(h, "."))
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
