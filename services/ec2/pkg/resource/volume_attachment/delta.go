// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package volume_attachment

import (
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	"k8s.io/apimachinery/pkg/api/equality"
)

// newResourceDelta compares the spec field by field, as ACK's generated
// delta.go does. Every field is immutable, so any difference is refused by
// sdkUpdate.
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
		{"Spec.VolumeID", a.ko.Spec.VolumeID, b.ko.Spec.VolumeID},
		{"Spec.InstanceID", a.ko.Spec.InstanceID, b.ko.Spec.InstanceID},
		{"Spec.DeviceName", a.ko.Spec.DeviceName, b.ko.Spec.DeviceName},
	}
	for _, f := range strs {
		if ackcompare.HasNilDifference(f.a, f.b) {
			delta.Add(f.path, f.a, f.b)
		} else if f.a != nil && f.b != nil && *f.a != *f.b {
			delta.Add(f.path, f.a, f.b)
		}
	}
	if !equality.Semantic.DeepEqual(a.ko.Spec.VolumeRef, b.ko.Spec.VolumeRef) {
		delta.Add("Spec.VolumeRef", a.ko.Spec.VolumeRef, b.ko.Spec.VolumeRef)
	}
	return delta
}
