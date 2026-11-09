// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package api_destination

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/eventbridge/apis/v1alpha1"
)

func destination(rate *int64) *resource {
	ko := &svcapitypes.APIDestination{}
	ko.Spec.Name = aws.String("orders")
	ko.Spec.ConnectionARN = aws.String("arn:aws:events:us-west-2:123456789012:connection/partner/abc")
	ko.Spec.InvocationEndpoint = aws.String("https://partner.example.com/orders")
	ko.Spec.HTTPMethod = aws.String("POST")
	ko.Spec.InvocationRateLimitPerSecond = rate
	return &resource{ko}
}

// AWS reports 300 per second for a destination made without a rate limit
// (moto reports none, so the e2e test cannot show this).
func TestDefaultRateLimitIsNoDrift(t *testing.T) {
	if d := newResourceDelta(destination(nil), destination(aws.Int64(300))); len(d.Differences) != 0 {
		t.Fatalf("the default rate limit reads as drift at %v", d.Differences)
	}
}

// A rate limit the spec sets is still compared.
func TestSetRateLimitIsCompared(t *testing.T) {
	if d := newResourceDelta(destination(aws.Int64(10)), destination(aws.Int64(300))); !d.DifferentAt("Spec.InvocationRateLimitPerSecond") {
		t.Fatal("a changed rate limit is no drift")
	}
}
