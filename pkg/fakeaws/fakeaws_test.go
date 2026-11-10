// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fakeaws_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"golang.org/x/net/http/httpproxy"

	"github.com/highfivelabs/ack-extensions/pkg/fakeaws"
)

const real = fakeaws.RealEndpoint

func TestParse(t *testing.T) {
	for _, c := range []struct {
		url   string
		scope fakeaws.Scope
		ok    bool
	}{
		{"http://127.0.0.1:5000", fakeaws.Loopback, true},
		{"http://localhost:1", fakeaws.Loopback, true},
		{"http://[::1]:80", fakeaws.Loopback, true},
		{real, fakeaws.Loopback, false},
		{real, fakeaws.Cluster, false},
		{"https://cloudcontrolapi.us-west-2.api.aws", fakeaws.Cluster, false},
		{"https://sts.cn-north-1.amazonaws.com.cn", fakeaws.Cluster, false},
		// A cluster-looking name under an AWS domain is still AWS.
		{"https://x.svc.amazonaws.com", fakeaws.Cluster, false},
		{"http://10.0.0.1:5000", fakeaws.Loopback, false},
		{"http://example.com", fakeaws.Cluster, false},
		{"http://moto.fake-aws:5000", fakeaws.Cluster, false},
		{"http://moto.fake-aws.svc.cluster.local:5000", fakeaws.Loopback, false},
		{"http://moto.fake-aws.svc.cluster.local:5000", fakeaws.Cluster, true},
		{"http://10.96.12.4:5000", fakeaws.Cluster, true},
		{"ftp://127.0.0.1", fakeaws.Loopback, false},
	} {
		_, err := fakeaws.Parse(c.url, c.scope)
		if (err == nil) != c.ok {
			t.Errorf("Parse(%q, %s): %v, want ok=%v", c.url, c.scope, err, c.ok)
		}
	}
	if _, err := fakeaws.Config(real, "us-west-2"); err == nil {
		t.Error("Config accepted a real endpoint")
	}
}

// One setting makes a process that passes; the incident of 2026-11-09 (only
// the service endpoint set, so ACK's runtime asked real STS who it was)
// and each other way out is refused.
func TestVerify(t *testing.T) {
	for _, scope := range []fakeaws.Scope{fakeaws.Loopback, fakeaws.Cluster} {
		u := "http://127.0.0.1:5000"
		if scope == fakeaws.Cluster {
			u = "http://moto.fake-aws.svc.cluster.local:5000"
		}
		e := fakeaws.MustParse(u, scope)
		args := append([]string{"--aws-region", "us-west-2"}, e.Args()...)
		var env []string
		if scope == fakeaws.Cluster {
			env = e.Env("10.96.0.1")
		} else {
			env = e.Env()
		}
		if err := fakeaws.Verify(args, env, scope); err != nil {
			t.Errorf("%s: the endpoint's own settings refused: %v", scope, err)
		}
	}

	e := fakeaws.MustParse("http://127.0.0.1:5000", fakeaws.Loopback)
	good := e.Env()
	for name, c := range map[string]struct {
		args []string
		env  []string
		want string
	}{
		"incident: no identity endpoint": {
			[]string{"--aws-endpoint-url=http://127.0.0.1:5000"}, good, "--aws-identity-endpoint-url is not set"},
		"identity endpoint at real STS": {
			[]string{"--aws-endpoint-url", "http://127.0.0.1:5000", "--aws-identity-endpoint-url", real}, good, "AWS domain"},
		"no AWS_ENDPOINT_URL": {
			e.Args(), without(good, "AWS_ENDPOINT_URL"), "AWS_ENDPOINT_URL is not set"},
		"a service's endpoint at AWS": {
			e.Args(), with(good, "AWS_ENDPOINT_URL_STS="+real), "AWS_ENDPOINT_URL_STS"},
		"configured endpoints ignored": {
			e.Args(), with(good, "AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true"), "AWS_IGNORE_CONFIGURED_ENDPOINT_URLS"},
		"no proxy": {
			e.Args(), without(good, "HTTPS_PROXY"), "HTTPS_PROXY"},
		"lower-case proxy elsewhere": {
			e.Args(), with(good, "https_proxy=http://proxy.example:3128"), "https_proxy"},
		"everything skips the proxy": {
			e.Args(), with(good, "NO_PROXY=*"), "NO_PROXY"},
		"AWS skips the proxy": {
			e.Args(), with(good, "no_proxy=.amazonaws.com"), "no_proxy"},
		"web identity": {
			e.Args(), with(good, "AWS_WEB_IDENTITY_TOKEN_FILE=/var/run/token", "AWS_ROLE_ARN=arn:aws:iam::1:role/x"), "AWS_ROLE_ARN"},
		"pod identity": {
			e.Args(), with(good, "AWS_CONTAINER_CREDENTIALS_FULL_URI=http://169.254.170.23/v1/credentials"), "AWS_CONTAINER_CREDENTIALS_FULL_URI"},
		"another key": {
			e.Args(), with(good, "AWS_ACCESS_KEY_ID=AKIAEXAMPLE"), "AWS_ACCESS_KEY_ID"},
	} {
		err := fakeaws.Verify(c.args, c.env, fakeaws.Loopback)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error naming %q", name, err, c.want)
		}
	}

	// ACK's Helm chart spells its flags as two arguments with $(VAR)
	// references; they are expanded as Kubernetes does.
	chart := []string{"--aws-endpoint-url", "$(AWS_ENDPOINT_URL)", "--aws-identity-endpoint-url", "$(AWS_IDENTITY_ENDPOINT_URL)", "--allow-unsafe-aws-endpoint-urls", "--aws-region", "us-west-2"}
	ce := fakeaws.MustParse("http://moto.fake-aws.svc.cluster.local:5000", fakeaws.Cluster)
	env := append(ce.Env("10.96.0.1"), "AWS_IDENTITY_ENDPOINT_URL="+ce.String(), "AWS_PROFILE=default", "AWS_SHARED_CREDENTIALS_FILE=/var/run/secrets/aws/credentials")
	if err := fakeaws.Verify(chart, env, fakeaws.Cluster); err != nil {
		t.Errorf("ACK's chart, pointed at the fake: %v", err)
	}
	env = append(env, "AWS_IDENTITY_ENDPOINT_URL=")
	if err := fakeaws.Verify(chart, env, fakeaws.Cluster); err == nil {
		t.Error("ACK's chart with an empty identity endpoint passed")
	}
}

func with(env []string, kv ...string) []string {
	return append(append([]string{}, env...), kv...)
}

func without(env []string, name string) []string {
	var out []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, name+"=") {
			out = append(out, kv)
		}
	}
	return out
}

func TestCommand(t *testing.T) {
	e := fakeaws.MustParse("http://127.0.0.1:5000", fakeaws.Loopback)
	cmd, err := fakeaws.Command(context.Background(), e, "/bin/true", []string{"--aws-endpoint-url", real}, "HOME=/tmp")
	if err != nil {
		t.Fatalf("a real endpoint before the fake's (the fake's flags come last and win): %v", err)
	}
	if last := cmd.Args[len(cmd.Args)-1]; last != "--allow-unsafe-aws-endpoint-urls" {
		t.Errorf("the endpoint's flags are not last: %v", cmd.Args)
	}
	if _, err := fakeaws.Command(context.Background(), e, "/bin/true", nil, "AWS_ENDPOINT_URL_STS="+real); err == nil {
		t.Error("Command let the caller set an AWS variable")
	}
	if _, err := fakeaws.Command(context.Background(), e, "/bin/true", nil, "HTTPS_PROXY="); err == nil {
		t.Error("Command let the caller clear the proxy")
	}
}

// The three locks of docs/cloudcontrol.md section 6, each on its own.
func TestLocks(t *testing.T) {
	// An SDK client with no endpoint override, as a mistyped configuration
	// would make: the dial is refused before any name is resolved.
	cfg := aws.Config{
		Region:           "us-west-2",
		Credentials:      credentials.NewStaticCredentialsProvider(fakeaws.AccessKeyID, fakeaws.SecretAccessKey, ""),
		HTTPClient:       fakeaws.HTTPClient(),
		RetryMaxAttempts: 1,
	}
	_, err := sts.NewFromConfig(cfg).GetCallerIdentity(context.Background(), &sts.GetCallerIdentityInput{})
	if err == nil || !strings.Contains(err.Error(), "refusing to dial") {
		t.Errorf("a request to real STS was not refused at the dial: %v", err)
	}
	// A process's environment sends every non-loopback request to the
	// dead proxy, and loopback ones direct.
	for _, env := range [][]string{fakeaws.Isolated(), fakeaws.MustParse("http://127.0.0.1:1", fakeaws.Loopback).Env()} {
		m := map[string]string{}
		for _, kv := range env {
			k, v, _ := strings.Cut(kv, "=")
			m[k] = v
		}
		proxy := (&httpproxy.Config{HTTPProxy: m["HTTP_PROXY"], HTTPSProxy: m["HTTPS_PROXY"], NoProxy: m["NO_PROXY"]}).ProxyFunc()
		r, _ := url.Parse(real + "/")
		if p, err := proxy(r); err != nil || p == nil || p.String() != fakeaws.DeadProxy {
			t.Errorf("real STS is not sent to the dead proxy: %v %v", p, err)
		}
		l, _ := url.Parse("http://127.0.0.1:4566/")
		if p, _ := proxy(l); p != nil {
			t.Errorf("loopback goes through the proxy: %v", p)
		}
		if m["AWS_ACCESS_KEY_ID"] != fakeaws.AccessKeyID || m["AWS_SHARED_CREDENTIALS_FILE"] != "/dev/null" {
			t.Errorf("credentials: %v", m)
		}
	}
	// In a cluster, the fake's Service is reached direct, AWS through the
	// dead proxy.
	c := fakeaws.MustParse("http://moto.fake-aws.svc.cluster.local:5000", fakeaws.Cluster)
	m := map[string]string{}
	for _, kv := range c.Env("10.96.0.1") {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	proxy := (&httpproxy.Config{HTTPProxy: m["HTTP_PROXY"], HTTPSProxy: m["HTTPS_PROXY"], NoProxy: m["NO_PROXY"]}).ProxyFunc()
	for _, c := range []struct {
		u    string
		dead bool
	}{{real + "/", true}, {"http://moto.fake-aws.svc.cluster.local:5000/", false}, {"https://10.96.0.1:443/api", false}} {
		r, _ := url.Parse(c.u)
		p, _ := proxy(r)
		if (p != nil && p.String() == fakeaws.DeadProxy) != c.dead {
			t.Errorf("%s: proxy %v, want dead=%v", c.u, p, c.dead)
		}
	}
}
