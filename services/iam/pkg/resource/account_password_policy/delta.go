// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package account_password_policy

import (
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	"k8s.io/apimachinery/pkg/api/equality"
)

// newResourceDelta compares the fields the spec sets. A field the spec
// leaves out keeps IAM's default and is no drift; IAM reports every
// field.
func newResourceDelta(a *resource, b *resource) *ackcompare.Delta {
	delta := ackcompare.NewDelta()
	if (a == nil && b != nil) || (a != nil && b == nil) {
		delta.Add("", a, b)
		return delta
	}
	as, bs := a.ko.Spec, b.ko.Spec
	bools := []struct {
		path string
		a, b *bool
	}{
		{"Spec.AllowUsersToChangePassword", as.AllowUsersToChangePassword, bs.AllowUsersToChangePassword},
		{"Spec.HardExpiry", as.HardExpiry, bs.HardExpiry},
		{"Spec.RequireLowercaseCharacters", as.RequireLowercaseCharacters, bs.RequireLowercaseCharacters},
		{"Spec.RequireNumbers", as.RequireNumbers, bs.RequireNumbers},
		{"Spec.RequireSymbols", as.RequireSymbols, bs.RequireSymbols},
		{"Spec.RequireUppercaseCharacters", as.RequireUppercaseCharacters, bs.RequireUppercaseCharacters},
	}
	for _, f := range bools {
		if f.a != nil && !equality.Semantic.DeepEqual(f.a, f.b) {
			delta.Add(f.path, f.a, f.b)
		}
	}
	ints := []struct {
		path string
		a, b *int64
	}{
		{"Spec.MaxPasswordAge", as.MaxPasswordAge, bs.MaxPasswordAge},
		{"Spec.MinimumPasswordLength", as.MinimumPasswordLength, bs.MinimumPasswordLength},
		{"Spec.PasswordReusePrevention", as.PasswordReusePrevention, bs.PasswordReusePrevention},
	}
	for _, f := range ints {
		if f.a != nil && !equality.Semantic.DeepEqual(f.a, f.b) {
			delta.Add(f.path, f.a, f.b)
		}
	}
	return delta
}
