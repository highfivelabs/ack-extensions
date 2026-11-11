// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package cfn

import (
	"fmt"
	"sort"
	"strings"
)

// SecretPaths checks the write-only properties a type opts in to Kubernetes
// Secret references (cloudcontrol/types.yaml, `secrets`, as property
// pointers such as /properties/AddPrivateKey) and returns them as
// CloudFormation property paths from the top (["AddPrivateKey"], or
// ["Auth", "Password"] for a nested one).
//
// Secret references are opt-in per field (decided 2026-11-09): many
// write-only properties are not secrets (an IPAM pool's netmask, a
// GuardDuty member's email, a SAML provider's key ID to remove), and those
// stay plain fields. A pointer that is not a write-only string reachable
// through objects (never inside an array) is an error, so a typo or a
// schema change cannot silently turn a secret into a plain field.
func (s *Schema) SecretPaths(opted []string) ([][]string, error) {
	writeOnly := NewPointerSet(s.WriteOnlyProperties)
	var out [][]string
	seen := map[string]bool{}
	for _, ptr := range opted {
		if seen[ptr] {
			return nil, fmt.Errorf("%s: secret %s listed twice", s.TypeName, ptr)
		}
		seen[ptr] = true
		if !strings.HasPrefix(ptr, "/properties/") {
			return nil, fmt.Errorf("%s: secret %q is not a property pointer (/properties/...)", s.TypeName, ptr)
		}
		if !writeOnly.Has(ptr) {
			return nil, fmt.Errorf("%s: secret %s is not one of the schema's writeOnlyProperties", s.TypeName, ptr)
		}
		parts := strings.Split(strings.TrimPrefix(normalisePointer(ptr), "/properties/"), "/")
		p := s.propAt(parts)
		if p == nil {
			return nil, fmt.Errorf("%s: secret %s is not reachable through objects (inside an array?)", s.TypeName, ptr)
		}
		if p.Type.Single() != "string" {
			return nil, fmt.Errorf("%s: secret %s is not a string", s.TypeName, ptr)
		}
		out = append(out, parts)
	}
	sort.Slice(out, func(i, j int) bool { return strings.Join(out[i], "/") < strings.Join(out[j], "/") })
	return out, nil
}

// SecretCandidates are the write-only string properties a type could opt
// in to Secret references, for the generation report and the survey.
func (s *Schema) SecretCandidates() []string {
	var out []string
	for ptr := range NewPointerSet(s.WriteOnlyProperties) {
		parts := strings.Split(strings.TrimPrefix(normalisePointer(ptr), "/properties/"), "/")
		if p := s.propAt(parts); p != nil && p.Type.Single() == "string" {
			out = append(out, ptr)
		}
	}
	sort.Strings(out)
	return out
}

// propAt walks object properties (never arrays) to a node.
func (s *Schema) propAt(path []string) *Prop {
	var p *Prop = s.Root()
	for _, seg := range path {
		if seg == "*" {
			return nil
		}
		props := s.ObjectProperties(p)
		if props == nil {
			return nil
		}
		p = props[seg]
		if p == nil {
			return nil
		}
	}
	p, _ = s.Resolve(p)
	return p
}

// FieldPath is a CloudFormation path as Kubernetes field names.
func (s *Schema) FieldPath(path []string) []string {
	var out []string
	var p *Prop = s.Root()
	for _, seg := range path {
		props := s.ObjectProperties(p)
		names, _ := Names(props)
		out = append(out, names.ToField[seg])
		p = props[seg]
	}
	return out
}
