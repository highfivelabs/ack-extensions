// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/s3control"
)

func resolve(t *testing.T, opt func(*svcsdk.Options), endpoint *string) string {
	t.Helper()
	o := svcsdk.Options{}
	opt(&o)
	r := o.EndpointResolverV2
	if r == nil {
		r = svcsdk.NewDefaultEndpointResolverV2()
	}
	ep, err := r.ResolveEndpoint(context.Background(), svcsdk.EndpointParameters{
		Region:            aws.String("us-west-2"),
		AccountId:         aws.String("123456789012"),
		RequiresAccountId: aws.Bool(true),
		Endpoint:          endpoint,
		UseFIPS:           aws.Bool(false),
		UseDualStack:      aws.Bool(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	return ep.URI.String()
}

func TestEndpoints(t *testing.T) {
	fake := aws.String("http://127.0.0.1:5000")
	for _, c := range []struct {
		what     string
		opt      func(*svcsdk.Options)
		endpoint *string
		want     string
	}{
		{"the SDK's rules put the account in front of a custom endpoint", func(*svcsdk.Options) {}, fake, "http://123456789012.127.0.0.1:5000"},
		{"Unprefixed keeps the fake's host", Unprefixed(), fake, "http://127.0.0.1:5000"},
		{"a plain-HTTP endpoint allowed as unsafe is a fake", ForConfig(*fake, true), fake, "http://127.0.0.1:5000"},
		{"not allowed as unsafe: the SDK's rules", ForConfig(*fake, false), fake, "http://123456789012.127.0.0.1:5000"},
		{"an HTTPS endpoint keeps the prefix", ForConfig("https://vpce-0a.s3-control.us-west-2.vpce.example", true), aws.String("https://vpce-0a.s3-control.us-west-2.vpce.example"), "https://123456789012.vpce-0a.s3-control.us-west-2.vpce.example"},
	} {
		if got := resolve(t, c.opt, c.endpoint); got != c.want {
			t.Errorf("%s: %s, want %s", c.what, got, c.want)
		}
	}
	// Without a custom endpoint (AWS's own, account prefix and all) nothing
	// changes. Compared, not written out: no AWS host in a test path.
	if got, want := resolve(t, Unprefixed(), nil), resolve(t, func(*svcsdk.Options) {}, nil); got != want {
		t.Errorf("AWS's own endpoint changed: %s, want %s", got, want)
	}
}
