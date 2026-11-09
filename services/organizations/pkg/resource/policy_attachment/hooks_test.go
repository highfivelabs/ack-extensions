// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package policy_attachment

import (
	"errors"
	"testing"

	smithy "github.com/aws/smithy-go"
)

func TestAlreadyDetached(t *testing.T) {
	for code, want := range map[string]bool{
		"PolicyNotAttachedException":          true,
		"PolicyNotFoundException":             true,
		"TargetNotFoundException":             true,
		"AccountNotFoundException":            true,
		"OrganizationalUnitNotFoundException": true,
		"AccessDeniedException":               false,
		"ConcurrentModificationException":     false,
	} {
		if got := alreadyDetached(&smithy.GenericAPIError{Code: code}); got != want {
			t.Errorf("%s: alreadyDetached = %v, want %v", code, got, want)
		}
	}
	if alreadyDetached(nil) || alreadyDetached(errors.New("network")) {
		t.Error("a nil or non-API error counts as detached")
	}
}
