// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package ssh_public_key

import (
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	"k8s.io/apimachinery/pkg/api/equality"
)

// newResourceDelta compares the user and the key's status (Active when
// unset). The body is immutable, and IAM may return it in another form,
// so it is not compared.
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
	if wantStatus(a.ko) != wantStatus(b.ko) {
		delta.Add("Spec.Status", a.ko.Spec.Status, b.ko.Spec.Status)
	}
	if !equality.Semantic.DeepEqual(a.ko.Spec.UserRef, b.ko.Spec.UserRef) {
		delta.Add("Spec.UserRef", a.ko.Spec.UserRef, b.ko.Spec.UserRef)
	}
	return delta
}
