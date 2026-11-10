// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package resource

import (
	"fmt"
	"sort"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/cfn"
)

// Op is one RFC 6902 operation.
type Op struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

// CreateOnlyChange is a desired create-only value that differs from AWS's.
// Cloud Control cannot update it; the CRD's CEL rules refuse the edit, so
// this is reached only by adoption, or a create-only path inside an array.
type CreateOnlyChange struct {
	Property         string
	Desired, Current any
}

func (c *CreateOnlyChange) Error() string {
	return fmt.Sprintf("%s is create-only (CloudFormation createOnlyProperties) and differs from AWS (desired %v, AWS %v): it cannot be updated; delete and re-create the object to change it", c.Property, c.Desired, c.Current)
}

func sortedMapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Patch computes the JSON Patch that takes the observed model to the
// desired one, both CloudFormation-cased and normalised:
//
//   - only top-level properties desired sets are touched (a property it
//     leaves out is the server's);
//   - never a read-only property; never a create-only one (a difference
//     there is a CreateOnlyChange error);
//   - an object is merged: desired's keys over the observed value, so a
//     nested server default is kept rather than removed;
//   - an array is replaced whole, never patched by position (insertionOrder:
//     false arrays, and the tag property, are compared as multisets first,
//     so a reordering alone is no change);
//   - a write-only property (never returned) is sent when writeOnlyChanged,
//     unless it is also create-only.
func Patch(s *cfn.Schema, desired, observed map[string]any, writeOnlyChanged bool) ([]Op, error) {
	sets := s.Sets()
	var ops []Op
	for _, k := range sortedMapKeys(desired) {
		dv := desired[k]
		ptr := cfn.Pointer(k)
		if dv == nil || sets.ReadOnly.Has(ptr) {
			continue
		}
		prop := s.ComparedProperty(k)
		if sets.WriteOnly.Has(ptr) {
			if writeOnlyChanged && !sets.CreateOnly.Has(ptr) {
				ops = append(ops, Op{Op: "add", Path: "/" + k, Value: dv})
			}
			continue
		}
		ov, present := observed[k]
		if present && s.Satisfies(prop, dv, ov) {
			continue
		}
		if sets.CreateOnly.Has(ptr) {
			if !present && sets.WriteOnly.Covers(ptr) {
				continue
			}
			return nil, &CreateOnlyChange{Property: k, Desired: dv, Current: ov}
		}
		if !present {
			ops = append(ops, Op{Op: "add", Path: "/" + k, Value: dv})
			continue
		}
		ops = append(ops, Op{Op: "replace", Path: "/" + k, Value: mergeOver(s, sets.ReadOnly, ptr, prop, dv, ov)})
	}
	return ops, nil
}

// mergeOver lays desired over observed for objects with declared
// properties, recursively, leaving out observed read-only properties;
// anything else is desired's value.
func mergeOver(s *cfn.Schema, readOnly cfn.PointerSet, ptr string, p *cfn.Prop, desired, observed any) any {
	d, ok1 := desired.(map[string]any)
	o, ok2 := observed.(map[string]any)
	props := s.ObjectProperties(p)
	if !ok1 || !ok2 || props == nil {
		return desired
	}
	out := map[string]any{}
	for k, v := range o {
		if !readOnly.Covers(ptr + "/" + k) {
			out[k] = v
		}
	}
	for k, v := range d {
		if v == nil {
			continue
		}
		out[k] = mergeOver(s, readOnly, ptr+"/"+k, props[k], v, o[k])
	}
	return out
}
