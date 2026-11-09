// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package cfn

import (
	"encoding/json"
	"reflect"
)

// Satisfies reports whether the observed value meets the desired one under
// the schema node p (both on the same side, CloudFormation or Kubernetes,
// and normalised).
//
// The rules are the drift rules of the generic reconciler:
//
//   - an object with declared properties is compared on the keys desired
//     sets; a key desired leaves out is the server's (a default), not
//     drift. This is narrower than awscc's "every optional attribute is
//     computed", which ignores a changed value too, and wider than a full
//     comparison, which would fight every server default;
//   - a free-form object (a policy document) or a map is compared whole;
//   - an array whose schema says insertionOrder: false is a multiset: the
//     same elements in any order (the awscc bugs #3259, #3302 and #3234 are
//     positional patches on such arrays);
//   - other arrays are compared in order and must have the same length;
//   - a scalar the handler returned as a string ("5", "true") equals the
//     number or boolean.
func (s *Schema) Satisfies(p *Prop, desired, observed any) bool {
	return s.satisfies(p, desired, observed, 0)
}

func (s *Schema) satisfies(p *Prop, desired, observed any, depth int) bool {
	if depth > 64 {
		return reflect.DeepEqual(desired, observed)
	}
	p, _ = s.Resolve(p)
	switch d := desired.(type) {
	case map[string]any:
		o, ok := observed.(map[string]any)
		if !ok {
			return false
		}
		props := s.ObjectProperties(p)
		if props == nil {
			return canonicalEqual(d, o)
		}
		for k, dv := range d {
			child := props[k]
			if child == nil {
				// The Kubernetes side: find the property by field name.
				if names, err := Names(props); err == nil {
					child = props[names.ToCFN[k]]
				}
			}
			ov, present := o[k]
			if dv == nil {
				continue
			}
			if !present || !s.satisfies(child, dv, ov, depth+1) {
				return false
			}
		}
		return true
	case []any:
		o, ok := observed.([]any)
		if !ok || len(o) != len(d) {
			return false
		}
		var item *Prop
		if p != nil {
			item = p.Items
		}
		if Unordered(p) {
			used := make([]bool, len(o))
			for _, dv := range d {
				found := false
				for j, ov := range o {
					if !used[j] && s.satisfies(item, dv, ov, depth+1) {
						used[j], found = true, true
						break
					}
				}
				if !found {
					return false
				}
			}
			return true
		}
		for i := range d {
			if !s.satisfies(item, d[i], o[i], depth+1) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(scalar(p, desired), scalar(p, observed))
	}
}

func canonicalEqual(a, b any) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(ja) == string(jb)
}
