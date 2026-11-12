// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package resource_tag

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	smithy "github.com/aws/smithy-go"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
)

func tag(resourceID, key, value string) *resource {
	ko := &svcapitypes.ResourceTag{}
	ko.Spec.ResourceID, ko.Spec.Key, ko.Spec.Value = aws.String(resourceID), aws.String(key), aws.String(value)
	return &resource{ko}
}

// A value changed outside Kubernetes is drift; the same tag is not.
func TestValueIsCompared(t *testing.T) {
	if d := newResourceDelta(tag("vpc-1", "team", "a"), tag("vpc-1", "team", "a")); len(d.Differences) != 0 {
		t.Fatalf("the same tag differs at %v", d.Differences)
	}
	if d := newResourceDelta(tag("vpc-1", "team", "a"), tag("vpc-1", "team", "b")); !d.DifferentAt("Spec.Value") {
		t.Fatal("a changed value is no drift")
	}
	if d := newResourceDelta(tag("vpc-1", "team", ""), tag("vpc-1", "team", "a")); !d.DifferentAt("Spec.Value") {
		t.Fatal("an empty value and a value read the same")
	}
}

// A resource not made yet is retried; a malformed ID, a bad key and the
// tag limit need a person.
func TestTerminalErrors(t *testing.T) {
	rm := &resourceManager{}
	for code, want := range map[string]bool{
		"InvalidVpcID.NotFound":       false,
		"InvalidSubnetID.NotFound":    false,
		"RequestLimitExceeded":        false,
		"InvalidID":                   true,
		"InvalidParameterValue":       true,
		"TagLimitExceeded":            true,
		"InvalidInstanceID.Malformed": true,
	} {
		err := &smithy.GenericAPIError{Code: code}
		if got := rm.terminalAWSError(err); got != want {
			t.Errorf("%s: terminal %v, want %v", code, got, want)
		}
		if got := notFound(err); got != (code == "InvalidVpcID.NotFound" || code == "InvalidSubnetID.NotFound") {
			t.Errorf("%s: not found %v", code, got)
		}
	}
	if rm.terminalAWSError(nil) {
		t.Error("no error is terminal")
	}
}
