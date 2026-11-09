// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package cfn

import (
	"sort"
	"strings"
)

// SecretPaths are the write-only string properties that become Kubernetes
// Secret references, as CloudFormation property paths from the top
// (["AddPrivateKey"], or ["Auth", "Password"] for a nested one). A
// write-only property inside an array, or one that is not a string, stays a
// plain field (CloudFormation marks some non-secrets write-only too: S3's
// are not secrets); the generator reports those.
func (s *Schema) SecretPaths() [][]string {
	var out [][]string
	for ptr := range NewPointerSet(s.WriteOnlyProperties) {
		parts := strings.Split(strings.TrimPrefix(ptr, "/properties/"), "/")
		if p := s.propAt(parts); p != nil && p.Type.Single() == "string" {
			out = append(out, parts)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.Join(out[i], "/") < strings.Join(out[j], "/") })
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
