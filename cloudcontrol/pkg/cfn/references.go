// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package cfn

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Reference is one reference field of the overlay (cloudcontrol/types.yaml
// `references`; docs/cloudcontrol.md section 12, item 4): spec.<RefField>
// ({from: {name, namespace}}, ACK's AWSResourceReferenceWrapper) names an
// object of Group/Version/Kind, and the reconciler copies that object's
// Path (a dotted path such as status.vpcID) into spec.<Field>, the
// property's own field, once the object is synced. Either the property or
// its reference is given, never both, as in ACK's generated references.
type Reference struct {
	// Property is the top-level CloudFormation property, as a pointer
	// (/properties/VpcId).
	Property string `json:"property"`
	// Field is the property's spec field (vpcId); filled by
	// Schema.References.
	Field string `json:"-"`
	// RefField is the reference field beside it; default: the field
	// without a trailing Id, Arn or Name, plus Ref (vpcRef).
	RefField string `json:"field,omitempty"`
	// Group, Version (default v1alpha1) and Kind of the referenced kind,
	// and Resource, its plural (default KindPlural(Kind)).
	Group    string `json:"group"`
	Version  string `json:"version,omitempty"`
	Kind     string `json:"kind"`
	Resource string `json:"resource,omitempty"`
	// Path is the referenced object's field holding the value
	// (status.vpcID).
	Path string `json:"path"`
}

// GVK is the referenced kind.
func (r Reference) GVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: r.Group, Version: r.Version, Kind: r.Kind}
}

// PathFields is Path split at its dots.
func (r Reference) PathFields() []string { return strings.Split(r.Path, ".") }

// refSuffixes are stripped from a field to name its reference.
var refSuffixes = []string{"Id", "ID", "Arn", "ARN", "Name"}

// References checks the overlay's references for this schema and fills
// their defaults: each must name a writable top-level string property,
// once, and a referenced kind and path; the reference field must not clash
// with a property's field.
func (s *Schema) References(refs []Reference) ([]Reference, error) {
	names, err := Names(s.Properties)
	if err != nil {
		return nil, err
	}
	readOnly := s.Sets().ReadOnly
	seen := map[string]bool{}
	var out []Reference
	for _, r := range refs {
		if !strings.HasPrefix(r.Property, "/properties/") || strings.Count(r.Property, "/") != 2 {
			return nil, fmt.Errorf("%s: reference %q is not a top-level property pointer (/properties/Name)", s.TypeName, r.Property)
		}
		name := strings.TrimPrefix(r.Property, "/properties/")
		p := s.Properties[name]
		if p == nil {
			return nil, fmt.Errorf("%s: reference %s: no such property", s.TypeName, r.Property)
		}
		if readOnly.Has(r.Property) {
			return nil, fmt.Errorf("%s: reference %s: the property is read-only", s.TypeName, r.Property)
		}
		if rp, _ := s.Resolve(p); rp == nil || rp.Type.Single() != "string" {
			return nil, fmt.Errorf("%s: reference %s: the property is not a string", s.TypeName, r.Property)
		}
		if seen[r.Property] {
			return nil, fmt.Errorf("%s: reference %s listed twice", s.TypeName, r.Property)
		}
		seen[r.Property] = true
		if r.Group == "" || r.Kind == "" || r.Path == "" {
			return nil, fmt.Errorf("%s: reference %s needs group, kind and path", s.TypeName, r.Property)
		}
		r.Field = names.ToField[name]
		if r.RefField == "" {
			base := r.Field
			for _, suf := range refSuffixes {
				if strings.HasSuffix(base, suf) && len(base) > len(suf) {
					base = strings.TrimSuffix(base, suf)
					break
				}
			}
			r.RefField = base + "Ref"
		}
		if _, clash := names.ToCFN[r.RefField]; clash {
			return nil, fmt.Errorf("%s: reference field %s clashes with a property", s.TypeName, r.RefField)
		}
		if r.Version == "" {
			r.Version = "v1alpha1"
		}
		if r.Resource == "" {
			r.Resource = KindPlural(r.Kind)
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Property < out[j].Property })
	for i := 1; i < len(out); i++ {
		if out[i].RefField == out[i-1].RefField {
			return nil, fmt.Errorf("%s: two references named %s", s.TypeName, out[i].RefField)
		}
	}
	return out, nil
}
