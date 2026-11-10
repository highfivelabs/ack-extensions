// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
	"golang.org/x/net/http/httpproxy"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/test/fake"
)

// The guard refuses AWS's real endpoints, by URL, by dial and by proxy.
func TestGuard(t *testing.T) {
	for _, u := range []string{"https://cloudcontrolapi.us-west-2.amazonaws.com", "http://10.0.0.1:5000", "http://example.com"} {
		if err := fake.LocalOnly(u); err == nil {
			t.Errorf("LocalOnly(%q) accepted", u)
		}
	}
	for _, u := range []string{"http://127.0.0.1:5000", "http://localhost:1", "http://[::1]:80"} {
		if err := fake.LocalOnly(u); err != nil {
			t.Errorf("LocalOnly(%q): %v", u, err)
		}
	}
	if _, err := fake.AWSConfig("https://cloudcontrolapi.us-west-2.amazonaws.com", "us-west-2"); err == nil {
		t.Errorf("AWSConfig accepted a real endpoint")
	}
	// An SDK client with no endpoint override, as a mistyped configuration
	// would make: the dial is refused before any name is resolved.
	cfg := aws.Config{
		Region:      "us-west-2",
		Credentials: credentials.NewStaticCredentialsProvider(fake.FakeAccessKeyID, fake.FakeSecretAccessKey, ""),
		HTTPClient:  fake.HTTPClient(),
	}
	cfg.RetryMaxAttempts = 1
	_, err := cloudcontrol.NewFromConfig(cfg).GetResource(context.Background(), &cloudcontrol.GetResourceInput{TypeName: aws.String("AWS::IAM::SAMLProvider"), Identifier: aws.String("x")})
	if err == nil || !strings.Contains(err.Error(), "refusing to dial") {
		t.Errorf("a request to the real endpoint was not refused at the dial: %v", err)
	}
	// The child environment sends every non-loopback request to a dead
	// local proxy (and loopback ones direct).
	env := map[string]string{}
	for _, kv := range fake.ChildEnv() {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	pc := httpproxy.Config{HTTPProxy: env["HTTP_PROXY"], HTTPSProxy: env["HTTPS_PROXY"], NoProxy: env["NO_PROXY"]}
	proxy := pc.ProxyFunc()
	real, _ := url.Parse("https://cloudcontrolapi.us-west-2.amazonaws.com/")
	if p, err := proxy(real); err != nil || p == nil || p.String() != fake.DeadProxy {
		t.Errorf("the real endpoint is not sent to the dead proxy: %v %v", p, err)
	}
	local, _ := url.Parse("http://127.0.0.1:4566/")
	if p, _ := proxy(local); p != nil {
		t.Errorf("loopback goes through the proxy: %v", p)
	}
	if env["AWS_ACCESS_KEY_ID"] != "testing" || env["AWS_SHARED_CREDENTIALS_FILE"] != "/dev/null" {
		t.Errorf("child credentials: %v", env)
	}
}

// mem is an in-memory backend for the server's own rules.
type mem struct {
	n    int
	objs map[string]map[string]any
}

func (m *mem) Create(_ context.Context, model map[string]any) (string, error) {
	m.n++
	id := fmt.Sprintf("tgw-rtb-%d", m.n)
	model["TransitGatewayRouteTableId"] = id
	m.objs[id] = model
	return id, nil
}
func (m *mem) Read(_ context.Context, id string) (map[string]any, error) {
	o, ok := m.objs[id]
	if !ok {
		return nil, fake.NotFound("%s", id)
	}
	out := map[string]any{}
	for k, v := range o {
		out[k] = v
	}
	return out, nil
}
func (m *mem) Update(_ context.Context, id string, _, next map[string]any) error {
	next["TransitGatewayRouteTableId"] = id
	m.objs[id] = next
	return nil
}
func (m *mem) Delete(_ context.Context, id string) error {
	if _, ok := m.objs[id]; !ok {
		return fake.NotFound("%s", id)
	}
	delete(m.objs, id)
	return nil
}

func TestServerRules(t *testing.T) {
	ctx := context.Background()
	srv := fake.New()
	srv.Polls = 1
	srv.Register(registry.ByTypeName("AWS::EC2::TransitGatewayRouteTable").Schema(), &mem{objs: map[string]map[string]any{}})
	u := srv.Start()
	defer srv.Close()
	cfg, err := fake.AWSConfig(u, "us-west-2")
	if err != nil {
		t.Fatal(err)
	}
	c := cloudcontrol.NewFromConfig(cfg)
	typ := aws.String("AWS::EC2::TransitGatewayRouteTable")

	// Asynchronous, with the identifier at the end; the same client token
	// is the same request.
	in := &cloudcontrol.CreateResourceInput{TypeName: typ, DesiredState: aws.String(`{"TransitGatewayId":"tgw-1"}`), ClientToken: aws.String("tok1")}
	o, err := c.CreateResource(ctx, in)
	if err != nil || o.ProgressEvent.OperationStatus != cctypes.OperationStatusInProgress || o.ProgressEvent.Identifier != nil {
		t.Fatalf("create: %v %+v", err, o)
	}
	again, _ := c.CreateResource(ctx, in)
	if aws.ToString(again.ProgressEvent.RequestToken) != aws.ToString(o.ProgressEvent.RequestToken) {
		t.Errorf("the same client token made a second request")
	}
	st, _ := c.GetResourceRequestStatus(ctx, &cloudcontrol.GetResourceRequestStatusInput{RequestToken: o.ProgressEvent.RequestToken})
	if st.ProgressEvent.OperationStatus != cctypes.OperationStatusInProgress {
		t.Errorf("first poll %s", st.ProgressEvent.OperationStatus)
	}
	st, _ = c.GetResourceRequestStatus(ctx, &cloudcontrol.GetResourceRequestStatusInput{RequestToken: o.ProgressEvent.RequestToken})
	if st.ProgressEvent.OperationStatus != cctypes.OperationStatusSuccess || aws.ToString(st.ProgressEvent.Identifier) != "tgw-rtb-1" {
		t.Errorf("second poll %+v", st.ProgressEvent)
	}

	// Cloud Control's validation: unknown, read-only and missing required.
	for body, want := range map[string]string{
		`{"TransitGatewayId":"tgw-1","Bogus":1}`:                        "extraneous key [Bogus]",
		`{"TransitGatewayId":"tgw-1","TransitGatewayRouteTableId":"x"}`: "read-only",
		`{}`: "required key [TransitGatewayId]",
	} {
		srv.Polls = 0
		o, err := c.CreateResource(ctx, &cloudcontrol.CreateResourceInput{TypeName: typ, DesiredState: aws.String(body)})
		if err != nil || o.ProgressEvent.OperationStatus != cctypes.OperationStatusFailed || o.ProgressEvent.ErrorCode != cctypes.HandlerErrorCodeInvalidRequest || !strings.Contains(aws.ToString(o.ProgressEvent.StatusMessage), want) {
			t.Errorf("create %s: %v %+v", body, err, o.ProgressEvent)
		}
	}

	// A patch of a create-only or read-only property is NotUpdatable.
	for _, patch := range []string{`[{"op":"replace","path":"/TransitGatewayId","value":"tgw-2"}]`, `[{"op":"replace","path":"/TransitGatewayRouteTableId","value":"x"}]`} {
		o, err := c.UpdateResource(ctx, &cloudcontrol.UpdateResourceInput{TypeName: typ, Identifier: aws.String("tgw-rtb-1"), PatchDocument: aws.String(patch)})
		if err != nil || o.ProgressEvent.ErrorCode != cctypes.HandlerErrorCodeNotUpdatable {
			t.Errorf("patch %s: %v %+v", patch, err, o.ProgressEvent)
		}
	}
	// A missing resource is ResourceNotFoundException.
	_, err = c.GetResource(ctx, &cloudcontrol.GetResourceInput{TypeName: typ, Identifier: aws.String("tgw-rtb-404")})
	var nf *cctypes.ResourceNotFoundException
	if !errors.As(err, &nf) {
		t.Errorf("get missing: %v", err)
	}
	// An unknown type.
	_, err = c.GetResource(ctx, &cloudcontrol.GetResourceInput{TypeName: aws.String("AWS::No::Such"), Identifier: aws.String("x")})
	if err == nil || !strings.Contains(err.Error(), "TypeNotFoundException") {
		t.Errorf("unknown type: %v", err)
	}
}
