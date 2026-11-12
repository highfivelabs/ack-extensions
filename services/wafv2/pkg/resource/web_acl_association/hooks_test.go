// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package web_acl_association

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/wafv2/types"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/wafv2/apis/v1alpha1"
)

// The read takes the web ACL's ARN into the spec and its name into the
// status; another web ACL set outside Kubernetes is then a delta.
func TestSetWebACL(t *testing.T) {
	ko := &svcapitypes.WebACLAssociation{}
	ko.Spec.ResourceARN, ko.Spec.WebACLARN = aws.String("arn:lb"), aws.String("arn:acl/edge")
	setWebACL(ko, &svcsdktypes.WebACL{ARN: aws.String("arn:acl/other"), Name: aws.String("other")})
	if aws.ToString(ko.Spec.WebACLARN) != "arn:acl/other" || aws.ToString(ko.Status.WebACLName) != "other" {
		t.Fatalf("spec %v status %v", aws.ToString(ko.Spec.WebACLARN), aws.ToString(ko.Status.WebACLName))
	}
	desired := &svcapitypes.WebACLAssociation{}
	desired.Spec.ResourceARN, desired.Spec.WebACLARN = aws.String("arn:lb"), aws.String("arn:acl/edge")
	if d := newResourceDelta(&resource{desired}, &resource{ko}); !d.DifferentAt("Spec.WebACLARN") {
		t.Fatal("another web ACL is no delta")
	}
	setWebACL(ko, nil)
	if aws.ToString(ko.Spec.WebACLARN) != "arn:acl/other" {
		t.Fatal("no web ACL changed the spec")
	}
}
