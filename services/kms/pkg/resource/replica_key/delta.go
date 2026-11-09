// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package replica_key

import (
	"reflect"

	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	"k8s.io/apimachinery/pkg/api/equality"
)

// newResourceDelta compares the spec field by field, as ACK's generated
// delta.go does, with two exceptions: the policy is compared as JSON (KMS
// may store it reformatted) and the tags as a set.
func newResourceDelta(a *resource, b *resource) *ackcompare.Delta {
	delta := ackcompare.NewDelta()
	if (a == nil && b != nil) || (a != nil && b == nil) {
		delta.Add("", a, b)
		return delta
	}
	sa, sb := a.ko.Spec, b.ko.Spec
	for _, f := range []struct {
		path string
		a, b *string
	}{
		{"Spec.KeyID", sa.KeyID, sb.KeyID},
		{"Spec.ReplicaRegion", sa.ReplicaRegion, sb.ReplicaRegion},
		{"Spec.Description", sa.Description, sb.Description},
	} {
		if ackcompare.HasNilDifference(f.a, f.b) {
			delta.Add(f.path, f.a, f.b)
		} else if f.a != nil && f.b != nil && *f.a != *f.b {
			delta.Add(f.path, f.a, f.b)
		}
	}
	if ackcompare.HasNilDifference(sa.BypassPolicyLockoutSafetyCheck, sb.BypassPolicyLockoutSafetyCheck) ||
		(sa.BypassPolicyLockoutSafetyCheck != nil && sb.BypassPolicyLockoutSafetyCheck != nil && *sa.BypassPolicyLockoutSafetyCheck != *sb.BypassPolicyLockoutSafetyCheck) {
		delta.Add("Spec.BypassPolicyLockoutSafetyCheck", sa.BypassPolicyLockoutSafetyCheck, sb.BypassPolicyLockoutSafetyCheck)
	}
	if !samePolicy(sa.Policy, sb.Policy) {
		delta.Add("Spec.Policy", sa.Policy, sb.Policy)
	}
	if !reflect.DeepEqual(tagMap(sa.Tags), tagMap(sb.Tags)) {
		delta.Add("Spec.Tags", sa.Tags, sb.Tags)
	}
	if !equality.Semantic.DeepEqual(sa.KeyRef, sb.KeyRef) {
		delta.Add("Spec.KeyRef", sa.KeyRef, sb.KeyRef)
	}
	return delta
}
