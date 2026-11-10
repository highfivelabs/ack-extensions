// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package cfn

import (
	"encoding/json"
	"strconv"
)

// Root is the schema's top level as an object node.
func (s *Schema) Root() *Prop { return &Prop{Properties: s.Properties} }

// direction picks the name map to apply.
type direction int

const (
	toCFN direction = iota
	toField
)

// ToCFN converts a Kubernetes-side value (spec fields, lower camel case) to
// CloudFormation's (PascalCase) under the schema node p. Keys of free-form
// objects and maps are kept as they are; unknown keys pass through.
func (s *Schema) ToCFN(p *Prop, v any) any { return s.rename(p, v, toCFN, 0) }

// FromCFN converts a CloudFormation model value to the Kubernetes side.
func (s *Schema) FromCFN(p *Prop, v any) any { return s.rename(p, v, toField, 0) }

func (s *Schema) rename(p *Prop, v any, dir direction, depth int) any {
	if depth > 64 {
		return v
	}
	p, _ = s.Resolve(p)
	switch val := v.(type) {
	case map[string]any:
		props := s.ObjectProperties(p)
		if props == nil {
			// A map (patternProperties) or a free-form object.
			var item *Prop
			if p != nil {
				for _, pp := range p.PatternProperties {
					item = pp
					break
				}
			}
			out := make(map[string]any, len(val))
			for k, x := range val {
				if item != nil {
					out[k] = s.rename(item, x, dir, depth+1)
				} else {
					out[k] = x
				}
			}
			return out
		}
		names, _ := Names(props)
		out := make(map[string]any, len(val))
		for k, x := range val {
			var cfnName, outName string
			if dir == toCFN {
				cfnName = names.ToCFN[k]
				outName = cfnName
			} else {
				cfnName = k
				outName = names.ToField[k]
			}
			if cfnName == "" || outName == "" {
				out[k] = x
				continue
			}
			out[outName] = s.rename(props[cfnName], x, dir, depth+1)
		}
		return out
	case []any:
		var item *Prop
		if p != nil {
			item = p.Items
		}
		out := make([]any, len(val))
		for i, x := range val {
			out[i] = s.rename(item, x, dir, depth+1)
		}
		return out
	default:
		return v
	}
}

// Normalise round-trips a value through JSON so that every number is a
// float64 and every object a map[string]any, whatever produced it
// (unstructured Kubernetes objects carry int64s).
func Normalise(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return v
	}
	return out
}

// scalar coerces a string to the schema's scalar type, since some
// handlers return numbers and booleans as strings.
func scalar(p *Prop, v any) any {
	s, ok := v.(string)
	if !ok || p == nil {
		return v
	}
	switch p.Type.Single() {
	case "integer", "number":
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
	case "boolean":
		if b, err := strconv.ParseBool(s); err == nil {
			return b
		}
	}
	return v
}
