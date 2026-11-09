// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package delegated_administrator

import (
	"errors"
	"testing"

	smithy "github.com/aws/smithy-go"
)

func TestAlreadyDeregistered(t *testing.T) {
	for code, want := range map[string]bool{
		"AccountNotRegisteredException":   true,
		"AccountNotFoundException":        true,
		"AccessDeniedException":           false,
		"ConcurrentModificationException": false,
	} {
		if got := alreadyDeregistered(&smithy.GenericAPIError{Code: code}); got != want {
			t.Errorf("%s: alreadyDeregistered = %v, want %v", code, got, want)
		}
	}
	if alreadyDeregistered(nil) || alreadyDeregistered(errors.New("network")) {
		t.Error("a nil or non-API error counts as deregistered")
	}
}
