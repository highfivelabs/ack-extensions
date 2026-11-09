// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package user_group_membership

import (
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

// The read keeps only the groups this object has a say over: those it
// lists and those it put the user in before. Others are not its.
func TestManagedIgnoresOtherGroups(t *testing.T) {
	got := managed([]string{"audit", "dev", "ops", "oncall"}, []string{"oncall", "dev"}, []string{"dev", "ops"})
	if want := []string{"oncall", "dev", "ops"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("managed = %v, want %v", got, want)
	}
	if got := managed([]string{"audit"}, []string{"dev"}, nil); len(got) != 0 {
		t.Fatalf("a group not listed nor put is managed: %v", got)
	}
}

// A group taken off the list is removed; one added is added; one still
// listed is left.
func TestChanges(t *testing.T) {
	add, remove := changes([]string{"dev", "oncall"}, []string{"dev", "ops"})
	if !reflect.DeepEqual(add, []string{"oncall"}) || !reflect.DeepEqual(remove, []string{"ops"}) {
		t.Fatalf("add %v remove %v", add, remove)
	}
	if add, remove := changes([]string{"dev"}, []string{"dev"}); len(add)+len(remove) != 0 {
		t.Fatalf("no change: add %v remove %v", add, remove)
	}
}

// The groups compare as a set.
func TestGroupOrderIsNoDrift(t *testing.T) {
	m := func(groups ...string) *resource {
		ko := &svcapitypes.UserGroupMembership{}
		ko.Spec.UserName = aws.String("alice")
		for _, g := range groups {
			ko.Spec.Groups = append(ko.Spec.Groups, aws.String(g))
		}
		return &resource{ko}
	}
	if d := newResourceDelta(m("dev", "ops"), m("ops", "dev")); len(d.Differences) != 0 {
		t.Fatalf("reordered groups differ at %v", d.Differences)
	}
	if d := newResourceDelta(m("dev", "ops"), m("dev")); !d.DifferentAt("Spec.Groups") {
		t.Fatal("a missing group is no drift")
	}
}
