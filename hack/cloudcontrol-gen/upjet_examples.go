// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/cfn"
)

// Upjet's hand-written examples as fixtures (HFL-700): an example whose
// every field this kind can express becomes a spec that
// TestUpjetExamples (cloudcontrol/test/fake) validates against the
// generated CRD and runs through the fake Cloud Control on the schema
// fake. Only `origin: hand` examples carry values; generated ones
// (converted from Terraform's documentation) are counted, never used.

// upjetFixture is one example as the test reads it
// (cloudcontrol/zz_generated.upjet-examples.json).
type upjetFixture struct {
	TypeName string         `json:"typeName"`
	Group    string         `json:"group"`
	Kind     string         `json:"kind"`
	Plural   string         `json:"plural"`
	Example  string         `json:"example"`
	Upstream string         `json:"upstream"`
	Slice    string         `json:"slice"`
	Manual   string         `json:"manualIntervention,omitempty"`
	Stood    []string       `json:"referencesStoodIn,omitempty"`
	Spec     map[string]any `json:"spec"`
}

// providerFields are Upjet's provider-level fields, not the resource's.
var providerFields = map[string]bool{"region": true, "tags": true, "tags_all": true}

// exampleSpec converts a hand example to a spec, or says why it cannot.
// A field filled by reference stands in as a plain value (the reference
// machinery has its own tests); tags are left out (the reconciler's own).
func exampleSpec(s *cfn.Schema, t typeEntry, refs []cfn.Reference, x *upjetExample) (map[string]any, []string, error) {
	names, err := cfn.Names(s.Properties)
	if err != nil {
		return nil, nil, err
	}
	sets := s.Sets()
	secret := map[string]bool{}
	for _, p := range t.Secrets {
		secret[p] = true
	}
	model := map[string]any{}
	for _, f := range sortedKeys(x.Values) {
		if providerFields[f] {
			continue
		}
		p := topProp(s, f)
		if p == "" {
			return nil, nil, fmt.Errorf("%s: no top-level property of that name", f)
		}
		ptr := cfn.Pointer(p)
		if sets.ReadOnly.Has(ptr) {
			return nil, nil, fmt.Errorf("%s: %s is read-only", f, p)
		}
		if secret[ptr] {
			return nil, nil, fmt.Errorf("%s: %s is a Secret reference", f, p)
		}
		v, err := exampleValue(s, s.Properties[p], x.Values[f], f)
		if err != nil {
			return nil, nil, err
		}
		model[p] = v
	}
	var stood []string
	for _, f := range x.RefFields {
		if providerFields[f] {
			continue
		}
		p := topProp(s, f)
		if p == "" {
			return nil, nil, fmt.Errorf("%s (by reference): no top-level property of that name", f)
		}
		rp, _ := s.Resolve(s.Properties[p])
		if rp == nil || rp.Type.Single() != "string" {
			return nil, nil, fmt.Errorf("%s (by reference): %s is not a string", f, p)
		}
		if sets.ReadOnly.Has(cfn.Pointer(p)) {
			return nil, nil, fmt.Errorf("%s (by reference): %s is read-only", f, p)
		}
		if _, ok := model[p]; !ok {
			// A stand-in within the schema's lengths.
			v := "upjet-ref-" + strings.ReplaceAll(f, "_", "-")
			if rp.MinLength != nil && int64(len(v)) < *rp.MinLength {
				v += strings.Repeat("0", int(*rp.MinLength)-len(v))
			}
			if rp.MaxLength != nil && int64(len(v)) > *rp.MaxLength {
				v = v[:*rp.MaxLength]
			}
			model[p] = v
			stood = append(stood, names.ToField[p])
		}
	}
	for _, r := range s.Required {
		if _, ok := model[r]; !ok && !sets.ReadOnly.Has(cfn.Pointer(r)) {
			return nil, nil, fmt.Errorf("required %s is not in the example", r)
		}
	}
	if len(model) == 0 {
		return nil, nil, fmt.Errorf("no field of the resource's own")
	}
	spec, _ := s.FromCFN(s.Root(), model).(map[string]any)
	return spec, stood, nil
}

// exampleValue checks a Terraform value against a CloudFormation
// property and renames its keys. A block Terraform writes as a list of one
// object is the object.
func exampleValue(s *cfn.Schema, p *cfn.Prop, v any, path string) (any, error) {
	rp, _ := s.Resolve(p)
	if rp == nil {
		return nil, fmt.Errorf("%s: unresolved", path)
	}
	typ := rp.Type.Single()
	switch typ {
	case "string":
		str, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s: a string, not %T", path, v)
		}
		if (rp.MinLength != nil && int64(len(str)) < *rp.MinLength) || (rp.MaxLength != nil && int64(len(str)) > *rp.MaxLength) {
			return nil, fmt.Errorf("%s: %q is outside the schema's lengths", path, str)
		}
		if len(rp.Enum) > 0 {
			found := false
			for _, e := range rp.Enum {
				found = found || fmt.Sprint(e) == str
			}
			if !found {
				return nil, fmt.Errorf("%s: %q is not in the enum", path, str)
			}
		}
		return str, nil
	case "integer", "number":
		n, ok := v.(float64)
		if !ok {
			return nil, fmt.Errorf("%s: a number, not %T", path, v)
		}
		if (rp.Minimum != nil && n < *rp.Minimum) || (rp.Maximum != nil && n > *rp.Maximum) || (typ == "integer" && n != float64(int64(n))) {
			return nil, fmt.Errorf("%s: %v is outside the schema's range", path, n)
		}
		return v, nil
	case "boolean":
		if _, ok := v.(bool); !ok {
			return nil, fmt.Errorf("%s: a boolean, not %T", path, v)
		}
		return v, nil
	case "array":
		l, ok := v.([]any)
		if !ok || rp.Items == nil {
			return nil, fmt.Errorf("%s: a list, not %T", path, v)
		}
		if (rp.MinItems != nil && int64(len(l)) < *rp.MinItems) || (rp.MaxItems != nil && int64(len(l)) > *rp.MaxItems) {
			return nil, fmt.Errorf("%s: %d items is outside the schema's bounds", path, len(l))
		}
		out := make([]any, 0, len(l))
		for i, it := range l {
			c, err := exampleValue(s, rp.Items, it, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		return out, nil
	case "object", "":
		if l, ok := v.([]any); ok && len(l) == 1 {
			v = l[0]
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: an object, not %T", path, v)
		}
		props := s.ObjectProperties(rp)
		if len(props) == 0 {
			return nil, fmt.Errorf("%s: a free-form object (Terraform writes it differently)", path)
		}
		out := map[string]any{}
		for _, k := range sortedKeys(m) {
			var name string
			for pn := range props {
				if strings.EqualFold(pn, strings.ReplaceAll(k, "_", "")) {
					name = pn
				}
			}
			if name == "" {
				return nil, fmt.Errorf("%s.%s: no such property", path, k)
			}
			c, err := exampleValue(s, props[name], m[k], path+"."+k)
			if err != nil {
				return nil, err
			}
			out[name] = c
		}
		for _, r := range rp.Required {
			if _, ok := out[r]; !ok {
				return nil, fmt.Errorf("%s: required %s is not in the example", path, r)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s: type %s", path, typ)
}

// examples turns the type's hand examples into fixtures and checks.
func (ut *upjetType) examples(s *cfn.Schema, t typeEntry, g generated, up *upjetData) []upjetFixture {
	var out []upjetFixture
	seen := map[string]bool{}
	for _, e := range ut.entries {
		for _, x := range e.Facts.Examples {
			if x.Origin != "hand" {
				continue
			}
			if seen[x.At] {
				continue
			}
			seen[x.At] = true
			c := check{kind: "example", subject: x.At, slice: x.line, upstream: x.At}
			spec, stood, err := exampleSpec(s, t, g.References, x)
			if err != nil {
				c.outcome, c.detail = "not-expressible", err.Error()
				ut.checks = append(ut.checks, c)
				continue
			}
			c.outcome, c.detail = "fixture", "runs on the schema fake in TestUpjetExamples"
			if len(stood) > 0 {
				c.detail += "; stood in for references: " + strings.Join(stood, ", ")
			}
			ut.checks = append(ut.checks, c)
			out = append(out, upjetFixture{TypeName: g.TypeName, Group: g.Group, Kind: g.Kind, Plural: g.Plural, Example: x.ID, Upstream: upstreamLink(up, x.At), Slice: at(x.line), Manual: x.ManualIntervention, Stood: stood, Spec: spec})
		}
	}
	return out
}

func writeExampleFixtures(out string, fx []upjetFixture) error {
	b, err := json.MarshalIndent(map[string]any{"generatedBy": header, "examples": fx}, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(out, "zz_generated.upjet-examples.json"), append(b, '\n'))
}

func writeExampleSection(b *bytes.Buffer, gens []generated) {
	fmt.Fprintf(b, "\n## Upjet's hand examples (HFL-700)\n\nEach hand-written example of a served type: a fixture when the kind can express every field (`cloudcontrol/zz_generated.upjet-examples.json`, run by `TestUpjetExamples` in `cloudcontrol/test/fake` on the schema fake in `task check`), otherwise why not.\n\n| Type | Example | Outcome | Detail |\n| --- | --- | --- | --- |\n")
	for _, g := range gens {
		for _, c := range g.Upjet.checks {
			if c.kind == "example" {
				fmt.Fprintf(b, "| `%s` | `%s` | %s | %s |\n", g.TypeName, c.subject, c.outcome, strings.ReplaceAll(c.detail, "|", "\\|"))
			}
		}
	}
}
