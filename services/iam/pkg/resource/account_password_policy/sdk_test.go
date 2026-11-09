// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package account_password_policy

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

func policy(spec svcapitypes.AccountPasswordPolicySpec) *resource {
	ko := &svcapitypes.AccountPasswordPolicy{}
	ko.Spec = spec
	return &resource{ko}
}

// IAM reports every field; the ones the spec leaves out are no drift.
func TestUnsetFieldsAreNoDrift(t *testing.T) {
	read := specFromPolicy(&svcsdktypes.PasswordPolicy{
		MinimumPasswordLength: aws.Int32(14), RequireSymbols: true, AllowUsersToChangePassword: false,
		MaxPasswordAge: aws.Int32(0), HardExpiry: aws.Bool(false),
	})
	want := svcapitypes.AccountPasswordPolicySpec{MinimumPasswordLength: aws.Int64(14), RequireSymbols: aws.Bool(true)}
	if d := newResourceDelta(policy(want), policy(read)); len(d.Differences) != 0 {
		t.Fatalf("IAM's defaults differ at %v", d.Differences)
	}
}

// A field the spec sets is compared.
func TestSetFieldsAreCompared(t *testing.T) {
	read := specFromPolicy(&svcsdktypes.PasswordPolicy{MinimumPasswordLength: aws.Int32(8)})
	want := svcapitypes.AccountPasswordPolicySpec{MinimumPasswordLength: aws.Int64(14), RequireNumbers: aws.Bool(false)}
	d := newResourceDelta(policy(want), policy(read))
	if !d.DifferentAt("Spec.MinimumPasswordLength") || d.DifferentAt("Spec.RequireNumbers") {
		t.Fatalf("differences %v", d.Differences)
	}
}
