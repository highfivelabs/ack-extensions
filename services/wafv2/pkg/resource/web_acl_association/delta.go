// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package web_acl_association

import (
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	"k8s.io/apimachinery/pkg/api/equality"
)

// newResourceDelta compares the spec field by field, as ACK's generated
// delta.go does.
func newResourceDelta(a *resource, b *resource) *ackcompare.Delta {
	delta := ackcompare.NewDelta()
	if (a == nil && b != nil) || (a != nil && b == nil) {
		delta.Add("", a, b)
		return delta
	}
	strs := []struct {
		path string
		a, b *string
	}{
		{"Spec.ResourceARN", a.ko.Spec.ResourceARN, b.ko.Spec.ResourceARN},
		{"Spec.WebACLARN", a.ko.Spec.WebACLARN, b.ko.Spec.WebACLARN},
	}
	for _, f := range strs {
		if ackcompare.HasNilDifference(f.a, f.b) {
			delta.Add(f.path, f.a, f.b)
		} else if f.a != nil && f.b != nil && *f.a != *f.b {
			delta.Add(f.path, f.a, f.b)
		}
	}
	if !equality.Semantic.DeepEqual(a.ko.Spec.WebACLRef, b.ko.Spec.WebACLRef) {
		delta.Add("Spec.WebACLRef", a.ko.Spec.WebACLRef, b.ko.Spec.WebACLRef)
	}
	return delta
}
