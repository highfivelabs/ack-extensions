// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package web_acl_association

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/wafv2/apis/v1alpha1"
)

func association(resourceARN, webACLARN string) *resource {
	ko := &svcapitypes.WebACLAssociation{}
	ko.Spec.ResourceARN, ko.Spec.WebACLARN = aws.String(resourceARN), aws.String(webACLARN)
	return &resource{ko}
}

// Another web ACL on the resource, set outside Kubernetes, is drift (which
// sdkUpdate puts right); the same one is not.
func TestOtherWebACLIsDrift(t *testing.T) {
	const lb = "arn:aws:elasticloadbalancing:us-west-2:123456789012:loadbalancer/app/web/1"
	if d := newResourceDelta(association(lb, "arn:acl/edge"), association(lb, "arn:acl/edge")); len(d.Differences) != 0 {
		t.Fatalf("the same association differs at %v", d.Differences)
	}
	if d := newResourceDelta(association(lb, "arn:acl/edge"), association(lb, "arn:acl/other")); !d.DifferentAt("Spec.WebACLARN") {
		t.Fatal("another web ACL is no drift")
	}
}
