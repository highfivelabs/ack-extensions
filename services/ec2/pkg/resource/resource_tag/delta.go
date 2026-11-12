// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package resource_tag

import (
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
)

// newResourceDelta compares the spec field by field, as ACK's generated
// delta.go does. Only the value can change in place (sdkUpdate); the
// resource and the key are immutable.
func newResourceDelta(a *resource, b *resource) *ackcompare.Delta {
	delta := ackcompare.NewDelta()
	if (a == nil && b != nil) || (a != nil && b == nil) {
		delta.Add("", a, b)
		return delta
	}
	for _, f := range []struct {
		path string
		a, b *string
	}{
		{"Spec.ResourceID", a.ko.Spec.ResourceID, b.ko.Spec.ResourceID},
		{"Spec.Key", a.ko.Spec.Key, b.ko.Spec.Key},
		{"Spec.Value", a.ko.Spec.Value, b.ko.Spec.Value},
	} {
		if ackcompare.HasNilDifference(f.a, f.b) {
			delta.Add(f.path, f.a, f.b)
		} else if f.a != nil && f.b != nil && *f.a != *f.b {
			delta.Add(f.path, f.a, f.b)
		}
	}
	return delta
}
