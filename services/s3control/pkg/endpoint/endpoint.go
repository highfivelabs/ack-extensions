// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package endpoint keeps S3 Control's requests on a fake AWS's own host.
//
// S3 Control names the account in the host of every account-level call
// ({AccountId}.s3-control.{region}.amazonaws.com), and its endpoint rules
// do the same to a custom endpoint: http://127.0.0.1:5000 becomes
// http://123456789012.127.0.0.1:5000, a name nothing resolves, so a
// manager pointed at moto or AWS Emulator with --aws-endpoint-url never
// reaches it. The account also travels in the x-amz-account-id header,
// which is what the fakes read.
//
// Unprefixed resolves the endpoint as the SDK's rules do (signing name,
// region and the rest) and then, for a custom endpoint only, sends the
// request to that endpoint's own host. ForConfig applies it only when
// ACK's runtime was given a plain-HTTP endpoint with
// --allow-unsafe-aws-endpoint-urls: a fake, never AWS (AWS's endpoints,
// VPC endpoints included, are HTTPS and keep the account prefix).
package endpoint

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	svcsdk "github.com/aws/aws-sdk-go-v2/service/s3control"
	smithyendpoints "github.com/aws/smithy-go/endpoints"
)

type unprefixed struct{ next svcsdk.EndpointResolverV2 }

func (u unprefixed) ResolveEndpoint(ctx context.Context, params svcsdk.EndpointParameters) (smithyendpoints.Endpoint, error) {
	ep, err := u.next.ResolveEndpoint(ctx, params)
	if err != nil || params.Endpoint == nil {
		return ep, err
	}
	custom, err := url.Parse(*params.Endpoint)
	if err != nil {
		return smithyendpoints.Endpoint{}, fmt.Errorf("custom endpoint %q: %w", *params.Endpoint, err)
	}
	ep.URI = *custom
	return ep, nil
}

// Unprefixed is a client option: a custom endpoint is used as given,
// without the account ID in front of its host. Without a custom endpoint
// it changes nothing.
func Unprefixed() func(*svcsdk.Options) {
	return func(o *svcsdk.Options) {
		next := o.EndpointResolverV2
		if next == nil {
			next = svcsdk.NewDefaultEndpointResolverV2()
		}
		o.EndpointResolverV2 = unprefixed{next: next}
	}
}

// ForConfig is the manager's client option for ACK's endpoint settings
// (--aws-endpoint-url, --allow-unsafe-aws-endpoint-urls): Unprefixed for a
// plain-HTTP endpoint the runtime was allowed to use, which only a fake
// is; otherwise nothing.
func ForConfig(endpointURL string, allowUnsafe bool) func(*svcsdk.Options) {
	if allowUnsafe && strings.HasPrefix(strings.ToLower(endpointURL), "http://") {
		return Unprefixed()
	}
	return func(*svcsdk.Options) {}
}
