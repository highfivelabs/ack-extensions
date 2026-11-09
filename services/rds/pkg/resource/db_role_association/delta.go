// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package db_role_association

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
		{"Spec.DBClusterIdentifier", a.ko.Spec.DBClusterIdentifier, b.ko.Spec.DBClusterIdentifier},
		{"Spec.DBInstanceIdentifier", a.ko.Spec.DBInstanceIdentifier, b.ko.Spec.DBInstanceIdentifier},
		{"Spec.RoleARN", a.ko.Spec.RoleARN, b.ko.Spec.RoleARN},
		{"Spec.FeatureName", a.ko.Spec.FeatureName, b.ko.Spec.FeatureName},
	}
	for _, f := range strs {
		if ackcompare.HasNilDifference(f.a, f.b) {
			delta.Add(f.path, f.a, f.b)
		} else if f.a != nil && f.b != nil && *f.a != *f.b {
			delta.Add(f.path, f.a, f.b)
		}
	}
	if !equality.Semantic.DeepEqual(a.ko.Spec.DBClusterRef, b.ko.Spec.DBClusterRef) {
		delta.Add("Spec.DBClusterRef", a.ko.Spec.DBClusterRef, b.ko.Spec.DBClusterRef)
	}
	if !equality.Semantic.DeepEqual(a.ko.Spec.DBInstanceRef, b.ko.Spec.DBInstanceRef) {
		delta.Add("Spec.DBInstanceRef", a.ko.Spec.DBInstanceRef, b.ko.Spec.DBInstanceRef)
	}
	if !equality.Semantic.DeepEqual(a.ko.Spec.RoleRef, b.ko.Spec.RoleRef) {
		delta.Add("Spec.RoleRef", a.ko.Spec.RoleRef, b.ko.Spec.RoleRef)
	}
	return delta
}
