// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package user_group_membership

import (
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	"k8s.io/apimachinery/pkg/api/equality"
)

// newResourceDelta compares the user, and the groups as a set: the order
// of spec.groups is not IAM's.
func newResourceDelta(a *resource, b *resource) *ackcompare.Delta {
	delta := ackcompare.NewDelta()
	if (a == nil && b != nil) || (a != nil && b == nil) {
		delta.Add("", a, b)
		return delta
	}
	if ackcompare.HasNilDifference(a.ko.Spec.UserName, b.ko.Spec.UserName) ||
		(a.ko.Spec.UserName != nil && b.ko.Spec.UserName != nil && *a.ko.Spec.UserName != *b.ko.Spec.UserName) {
		delta.Add("Spec.UserName", a.ko.Spec.UserName, b.ko.Spec.UserName)
	}
	if ga, gb := set(names(a.ko.Spec.Groups)), set(names(b.ko.Spec.Groups)); !equality.Semantic.DeepEqual(ga, gb) {
		delta.Add("Spec.Groups", a.ko.Spec.Groups, b.ko.Spec.Groups)
	}
	if !equality.Semantic.DeepEqual(a.ko.Spec.UserRef, b.ko.Spec.UserRef) {
		delta.Add("Spec.UserRef", a.ko.Spec.UserRef, b.ko.Spec.UserRef)
	}
	if !equality.Semantic.DeepEqual(a.ko.Spec.GroupRefs, b.ko.Spec.GroupRefs) {
		delta.Add("Spec.GroupRefs", a.ko.Spec.GroupRefs, b.ko.Spec.GroupRefs)
	}
	return delta
}
