// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package account_alias

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

func alias(a string) *resource {
	ko := &svcapitypes.AccountAlias{}
	ko.Spec.AccountAlias = aws.String(a)
	return &resource{ko}
}

func TestSameAliasIsNoDrift(t *testing.T) {
	if d := newResourceDelta(alias("acme-prod"), alias("acme-prod")); len(d.Differences) != 0 {
		t.Fatalf("differs at %v", d.Differences)
	}
	if d := newResourceDelta(alias("acme-prod"), alias("acme-dev")); !d.DifferentAt("Spec.AccountAlias") {
		t.Fatal("another alias is no difference")
	}
}
