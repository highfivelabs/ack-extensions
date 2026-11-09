// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package login_profile

import (
	"testing"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	corev1 "k8s.io/api/core/v1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

func profile(reset *bool) *resource {
	ko := &svcapitypes.LoginProfile{}
	ko.Spec.UserName = aws.String("carol")
	ko.Spec.Password = &ackv1alpha1.SecretKeyReference{SecretReference: corev1.SecretReference{Name: "console"}, Key: "password"}
	ko.Spec.PasswordResetRequired = reset
	return &resource{ko}
}

// IAM reports passwordResetRequired false for a spec that leaves it out.
func TestDefaultResetIsNoDrift(t *testing.T) {
	if d := newResourceDelta(profile(nil), profile(aws.Bool(false))); len(d.Differences) != 0 {
		t.Fatalf("the default reads as drift at %v", d.Differences)
	}
}

// A value the spec sets is still compared.
func TestSetResetIsCompared(t *testing.T) {
	if d := newResourceDelta(profile(aws.Bool(true)), profile(aws.Bool(false))); !d.DifferentAt("Spec.PasswordResetRequired") {
		t.Fatal("a changed passwordResetRequired is no drift")
	}
}
