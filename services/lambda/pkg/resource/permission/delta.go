// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package permission

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
	sa, sb := a.ko.Spec, b.ko.Spec
	strs := []struct {
		path string
		a, b *string
	}{
		{"Spec.FunctionName", sa.FunctionName, sb.FunctionName},
		{"Spec.Qualifier", sa.Qualifier, sb.Qualifier},
		{"Spec.StatementID", sa.StatementID, sb.StatementID},
		{"Spec.Action", sa.Action, sb.Action},
		{"Spec.Principal", sa.Principal, sb.Principal},
		{"Spec.SourceARN", sa.SourceARN, sb.SourceARN},
		{"Spec.SourceAccount", sa.SourceAccount, sb.SourceAccount},
		{"Spec.EventSourceToken", sa.EventSourceToken, sb.EventSourceToken},
		{"Spec.PrincipalOrgID", sa.PrincipalOrgID, sb.PrincipalOrgID},
		{"Spec.FunctionURLAuthType", sa.FunctionURLAuthType, sb.FunctionURLAuthType},
	}
	for _, f := range strs {
		if ackcompare.HasNilDifference(f.a, f.b) {
			delta.Add(f.path, f.a, f.b)
		} else if f.a != nil && f.b != nil && *f.a != *f.b {
			delta.Add(f.path, f.a, f.b)
		}
	}
	if !equality.Semantic.DeepEqual(sa.FunctionRef, sb.FunctionRef) {
		delta.Add("Spec.FunctionRef", sa.FunctionRef, sb.FunctionRef)
	}
	return delta
}
