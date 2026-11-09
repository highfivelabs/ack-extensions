// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package account_alias

import (
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
)

// newResourceDelta compares the alias. The read finds only this object's
// alias, so a difference comes only from a spec change, which the CRD
// refuses.
func newResourceDelta(a *resource, b *resource) *ackcompare.Delta {
	delta := ackcompare.NewDelta()
	if (a == nil && b != nil) || (a != nil && b == nil) {
		delta.Add("", a, b)
		return delta
	}
	if ackcompare.HasNilDifference(a.ko.Spec.AccountAlias, b.ko.Spec.AccountAlias) ||
		(a.ko.Spec.AccountAlias != nil && b.ko.Spec.AccountAlias != nil && *a.ko.Spec.AccountAlias != *b.ko.Spec.AccountAlias) {
		delta.Add("Spec.AccountAlias", a.ko.Spec.AccountAlias, b.ko.Spec.AccountAlias)
	}
	return delta
}
