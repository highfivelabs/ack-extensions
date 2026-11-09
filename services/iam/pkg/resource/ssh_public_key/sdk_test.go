// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package ssh_public_key

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

func key(status *string, body string) *resource {
	ko := &svcapitypes.SSHPublicKey{}
	ko.Spec.UserName, ko.Spec.SSHPublicKeyBody, ko.Spec.Status = aws.String("erin"), aws.String(body), status
	return &resource{ko}
}

// No status in the spec is Active, IAM's default; the body (which IAM may
// return in another form) is not compared.
func TestStatusDefaultAndBodyAreNoDrift(t *testing.T) {
	if d := newResourceDelta(key(nil, "ssh-rsa AAAA a@b"), key(aws.String("Active"), "ssh-rsa AAAA")); len(d.Differences) != 0 {
		t.Fatalf("differs at %v", d.Differences)
	}
}

// A different status is drift.
func TestStatusIsCompared(t *testing.T) {
	if d := newResourceDelta(key(aws.String("Inactive"), "k"), key(aws.String("Active"), "k")); !d.DifferentAt("Spec.Status") {
		t.Fatal("a changed status is no drift")
	}
}
