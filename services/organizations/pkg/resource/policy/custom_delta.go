// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package policy

import (
	"encoding/json"
	"reflect"

	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
)

// customPreCompare compares a policy's content as a JSON document, so a
// change of key order or white space is no drift. Content that is not JSON
// is compared as text.
func customPreCompare(
	delta *ackcompare.Delta,
	a *resource,
	b *resource,
) {
	if !sameContent(a.ko.Spec.Content, b.ko.Spec.Content) {
		delta.Add("Spec.Content", a.ko.Spec.Content, b.ko.Spec.Content)
	}
}

func sameContent(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	if *a == *b {
		return true
	}
	var x, y any
	if json.Unmarshal([]byte(*a), &x) != nil || json.Unmarshal([]byte(*b), &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}
