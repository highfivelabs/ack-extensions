// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package public_access_block

import (
	"testing"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/s3control/types"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/s3control/apis/v1alpha1"
)

func block(acls, ignore, policy, restrict *bool) *resource {
	return &resource{ko: &svcapitypes.PublicAccessBlock{Spec: svcapitypes.PublicAccessBlockSpec{
		PublicAccessBlockConfiguration: &svcapitypes.PublicAccessBlockConfiguration{
			BlockPublicACLs: acls, IgnorePublicACLs: ignore, BlockPublicPolicy: policy, RestrictPublicBuckets: restrict,
		},
	}}}
}

func TestDelta(t *testing.T) {
	yes, no := aws.Bool(true), aws.Bool(false)
	for _, c := range []struct {
		what            string
		desired, latest *resource
		diff            bool
	}{
		{"two left out read back false: no drift", block(yes, nil, yes, nil), block(yes, no, yes, no), false},
		{"one left out set true outside Kubernetes: drift", block(yes, nil, yes, nil), block(yes, no, yes, yes), true},
		{"one set changed outside Kubernetes: drift", block(yes, no, yes, no), block(no, no, yes, no), true},
		{"all four as read: no drift", block(yes, yes, yes, yes), block(yes, yes, yes, yes), false},
	} {
		if d := newResourceDelta(c.desired, c.latest); d.DifferentAt("Spec.PublicAccessBlockConfiguration") != c.diff {
			t.Errorf("%s: different %v, want %v (%v)", c.what, !c.diff, c.diff, d.Differences)
		}
	}
}

func TestHooks(t *testing.T) {
	rm := &resourceManager{awsPartition: "aws", awsRegion: "us-west-2", awsAccountID: ackv1alpha1.AWSAccountID("123456789012")}
	if got := *rm.accountID(); got != "123456789012" {
		t.Fatalf("the request names account %s, not the manager's", got)
	}
	ko := &svcapitypes.PublicAccessBlock{}
	if made(ko) {
		t.Fatal("a new object counts as having put the settings")
	}
	rm.setARN(ko)
	if !made(ko) {
		t.Fatal("an object with its ARN set does not count as having put the settings")
	}
	setConfiguration(ko, &svcsdktypes.PublicAccessBlockConfiguration{
		BlockPublicAcls: aws.Bool(true), IgnorePublicAcls: aws.Bool(false), BlockPublicPolicy: aws.Bool(true), RestrictPublicBuckets: aws.Bool(false),
	})
	c := ko.Spec.PublicAccessBlockConfiguration
	if !*c.BlockPublicACLs || *c.IgnorePublicACLs || !*c.BlockPublicPolicy || *c.RestrictPublicBuckets {
		t.Fatalf("read %+v", c)
	}
}
