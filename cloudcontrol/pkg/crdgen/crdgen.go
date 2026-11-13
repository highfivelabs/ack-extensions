// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package crdgen writes a CustomResourceDefinition from a CloudFormation
// registry schema:
//
//   - writable properties to spec, in lower camel case;
//   - readOnlyProperties to status (top-level ones; a read-only property
//     nested inside a writable object is dropped from spec and reported),
//     keeping types and structure but no value checks (enum, lengths,
//     bounds, item counts): AWS writes status, so only spec is validated;
//   - createOnlyProperties to CEL transition rules: self == oldSelf on the
//     field, and has(self.f) == has(oldSelf.f) on its parent, so the API
//     server refuses the change instead of the cloud (rules cannot reach
//     inside arrays; those are reported, and the reconciler refuses them);
//   - the writeOnlyProperties a type opts in (Options.Secrets: write-only
//     strings only) to Secret references (ACK's SecretKeyReference shape),
//     resolved by the reconciler; every other write-only property is a
//     plain field;
//   - the overlay's references (Options.References) to reference fields
//     beside the ID property they fill (vpcRef beside vpcId, ACK's
//     AWSResourceReferenceWrapper shape), with CEL rules that one of the two
//     is given, not both, and that a create-only property's reference does
//     not change either;
//   - primaryIdentifier to status.cloudControl.identifier (Cloud Control's
//     identifier), beside the last request and a digest of write-only values;
//   - the status fields every ACK kind has: ackResourceMetadata and
//     conditions (ACK.ResourceSynced, Ready, ACK.Terminal, ...).
//
// JSON Schema keywords Kubernetes cannot express are dropped and counted
// (pattern: ECMAScript regular expressions are not RE2; format), and nodes
// it cannot type (a list of types, a recursion) become
// x-kubernetes-preserve-unknown-fields.
package crdgen

import (
	"fmt"
	"sort"
	"strings"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/cfn"
)

// AnnotationTypeName on a CRD names the CloudFormation type it was made from.
const AnnotationTypeName = "cloudcontrol.aws.hfl.dev/type-name"

// AnnotationUnproven on a CRD says why its kind is unproven on the fake
// (no moto backend: moto lacks the service's API).
const AnnotationUnproven = "cloudcontrol.aws.hfl.dev/unproven-on-fake"

// AnnotationNeverDelete on a CRD says deleting one of its objects never
// deletes the resource in AWS (neverDelete in cloudcontrol/types.yaml).
const AnnotationNeverDelete = "cloudcontrol.aws.hfl.dev/never-delete"

// Reserved status fields; a read-only property with one of these names is
// an error the overlay must resolve.
var reservedStatus = map[string]bool{
	"ackResourceMetadata": true, "conditions": true, "cloudControl": true,
}

// Options name the kind and the write-only properties served as Secret
// references (property pointers, cloudcontrol/types.yaml `secrets`).
type Options struct {
	Group, Version, Kind string
	Secrets              []string
	// Unproven, when set, says why the kind has no test on the fake; it
	// becomes the AnnotationUnproven annotation, which the coverage data
	// reports.
	Unproven string
	// NeverDelete becomes the AnnotationNeverDelete annotation.
	NeverDelete bool
	// References are the overlay's reference fields, checked and filled by
	// cfn.Schema.References.
	References []cfn.Reference
}

// Report is what the generation could not map exactly.
type Report struct {
	TypeName        string
	SpecFields      int
	StatusFields    int
	CELRules        int
	SecretRefs      []string // write-only strings opted in to Secret references
	References      []string // reference fields added by the overlay
	PlainWriteOnly  []string // write-only properties kept as plain fields
	SecretCandidate []string // plain write-only strings that could be opted in
	CELSkipped      []string // create-only paths no CEL rule can reach
	Lossy           []string // nodes made x-kubernetes-preserve-unknown-fields
	NestedReadOnly  []string // read-only properties inside writable objects, dropped
	DroppedPatterns int
	Recursions      int
}

type gen struct {
	s       *cfn.Schema
	sets    cfn.Sets
	secrets map[string]bool
	rep     *Report
}

// Generate makes the CRD.
func Generate(s *cfn.Schema, o Options) (*apiextv1.CustomResourceDefinition, *Report, error) {
	g := &gen{s: s, sets: s.Sets(), secrets: map[string]bool{}, rep: &Report{TypeName: s.TypeName}}
	paths, err := s.SecretPaths(o.Secrets)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range paths {
		g.secrets[cfn.Pointer(p...)] = true
		g.rep.SecretRefs = append(g.rep.SecretRefs, cfn.Pointer(p...))
	}
	opted := cfn.NewPointerSet(o.Secrets)
	for _, p := range s.WriteOnlyProperties {
		if !opted.Has(p) {
			g.rep.PlainWriteOnly = append(g.rep.PlainWriteOnly, p)
		}
	}
	for _, p := range s.SecretCandidates() {
		if !opted.Has(p) {
			g.rep.SecretCandidate = append(g.rep.SecretCandidate, p)
		}
	}

	spec, err := g.object(s.Root(), "/properties", map[string]bool{}, true)
	if err != nil {
		return nil, nil, err
	}
	// Top-level required, minus read-only.
	topNames, err := cfn.Names(s.Properties)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", s.TypeName, err)
	}
	spec.Required = nil
	for _, r := range s.Required {
		if !g.sets.ReadOnly.Covers(cfn.Pointer(r)) && s.Properties[r] != nil {
			spec.Required = append(spec.Required, topNames.ToField[r])
		}
	}
	sort.Strings(spec.Required)
	g.rep.SpecFields = len(spec.Properties)
	if err := g.createOnlyRules(&spec); err != nil {
		return nil, nil, err
	}
	g.references(&spec, o.References)

	status := statusSchema()
	for _, name := range sortedKeys(s.Properties) {
		ptr := cfn.Pointer(name)
		if !g.sets.ReadOnly.Has(ptr) {
			continue
		}
		f := topNames.ToField[name]
		if reservedStatus[f] {
			return nil, nil, fmt.Errorf("%s: read-only property %s clashes with status.%s", s.TypeName, name, f)
		}
		n := g.node(s.Properties[name], ptr, map[string]bool{}, true)
		status.Properties[f] = n
		g.rep.StatusFields++
	}

	plural := cfn.KindPlural(o.Kind)
	annotations := map[string]string{AnnotationTypeName: s.TypeName}
	if o.Unproven != "" {
		annotations[AnnotationUnproven] = o.Unproven
	}
	if o.NeverDelete {
		annotations[AnnotationNeverDelete] = "true"
	}
	crd := &apiextv1.CustomResourceDefinition{
		TypeMeta: metav1.TypeMeta{APIVersion: "apiextensions.k8s.io/v1", Kind: "CustomResourceDefinition"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        plural + "." + o.Group,
			Annotations: annotations,
		},
		Spec: apiextv1.CustomResourceDefinitionSpec{
			Group: o.Group,
			Names: apiextv1.CustomResourceDefinitionNames{
				Kind: o.Kind, ListKind: o.Kind + "List", Plural: plural,
				Singular: strings.ToLower(o.Kind), Categories: []string{"aws", "cloudcontrol"},
			},
			Scope: apiextv1.NamespaceScoped,
			Versions: []apiextv1.CustomResourceDefinitionVersion{{
				Name: o.Version, Served: true, Storage: true,
				Subresources: &apiextv1.CustomResourceSubresources{Status: &apiextv1.CustomResourceSubresourceStatus{}},
				AdditionalPrinterColumns: []apiextv1.CustomResourceColumnDefinition{
					{Name: "ID", Type: "string", JSONPath: ".status.cloudControl.identifier"},
					{Name: "Synced", Type: "string", JSONPath: `.status.conditions[?(@.type=="ACK.ResourceSynced")].status`},
					{Name: "Ready", Type: "string", JSONPath: `.status.conditions[?(@.type=="Ready")].status`},
					{Name: "Age", Type: "date", JSONPath: ".metadata.creationTimestamp"},
				},
				Schema: &apiextv1.CustomResourceValidation{OpenAPIV3Schema: &apiextv1.JSONSchemaProps{
					Description: fmt.Sprintf("%s is %s, managed through AWS Cloud Control API. Generated from the CloudFormation registry schema; do not edit.", o.Kind, s.TypeName),
					Type:        "object",
					Properties: map[string]apiextv1.JSONSchemaProps{
						"apiVersion": {Type: "string"},
						"kind":       {Type: "string"},
						"metadata":   {Type: "object"},
						"spec":       spec,
						"status":     status,
					},
					Required: []string{"spec"},
				}},
			}},
		},
	}
	return crd, g.rep, nil
}

func sortedKeys(m map[string]*cfn.Prop) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func preserve() apiextv1.JSONSchemaProps {
	t := true
	return apiextv1.JSONSchemaProps{XPreserveUnknownFields: &t}
}

func (g *gen) lossy(ptr, why string) apiextv1.JSONSchemaProps {
	g.rep.Lossy = append(g.rep.Lossy, ptr+" ("+why+")")
	return preserve()
}

func describe(d string) string {
	d = strings.TrimSpace(d)
	if len(d) > 1000 {
		d = d[:1000] + "..."
	}
	return d
}

// object builds an object node with declared properties; top is the spec.
func (g *gen) object(p *cfn.Prop, ptr string, stack map[string]bool, top bool) (apiextv1.JSONSchemaProps, error) {
	props := g.s.ObjectProperties(p)
	names, err := cfn.Names(props)
	if err != nil {
		return apiextv1.JSONSchemaProps{}, fmt.Errorf("%s %s: %w", g.s.TypeName, ptr, err)
	}
	out := apiextv1.JSONSchemaProps{Type: "object", Properties: map[string]apiextv1.JSONSchemaProps{}}
	rp, _ := g.s.Resolve(p)
	for _, name := range sortedKeys(props) {
		child := ptr + "/" + name
		if top {
			child = cfn.Pointer(name)
		}
		if g.sets.ReadOnly.Covers(child) {
			if !top {
				g.rep.NestedReadOnly = append(g.rep.NestedReadOnly, child)
			}
			continue
		}
		if g.secrets[child] {
			out.Properties[names.ToField[name]] = secretRef(props[name])
			continue
		}
		out.Properties[names.ToField[name]] = g.node(props[name], child, stack, false)
	}
	if rp != nil && !top {
		for _, r := range rp.Required {
			if f, ok := names.ToField[r]; ok {
				if _, kept := out.Properties[f]; kept {
					out.Required = append(out.Required, f)
				}
			}
		}
		sort.Strings(out.Required)
	}
	return out, nil
}

func secretRef(p *cfn.Prop) apiextv1.JSONSchemaProps {
	d := "A reference to a key of a Kubernetes Secret holding this write-only value."
	if p != nil && p.Description != "" {
		d = describe(p.Description) + " " + d
	}
	return apiextv1.JSONSchemaProps{
		Description: d,
		Type:        "object",
		Properties: map[string]apiextv1.JSONSchemaProps{
			"name":      {Type: "string", Description: "The Secret's name."},
			"namespace": {Type: "string", Description: "The Secret's namespace; the object's own when empty."},
			"key":       {Type: "string", Description: "The key in the Secret's data."},
		},
		Required: []string{"key", "name"},
	}
}

// node builds any schema node. inStatus keeps nested read-only properties.
func (g *gen) node(p *cfn.Prop, ptr string, stack map[string]bool, inStatus bool) apiextv1.JSONSchemaProps {
	rp, def := g.s.Resolve(p)
	if rp == nil {
		return g.lossy(ptr, "no schema")
	}
	if def != "" {
		if stack[def] {
			g.rep.Recursions++
			return g.lossy(ptr, "recursive definition "+def)
		}
		stack[def] = true
		defer delete(stack, def)
	}
	if rp.Pattern != "" {
		g.rep.DroppedPatterns++
	}
	props := g.s.ObjectProperties(rp)
	t := rp.Type.Single()
	if len(rp.Type) > 1 {
		ts := append([]string(nil), rp.Type...)
		sort.Strings(ts)
		if strings.Join(ts, ",") == "integer,string" {
			return apiextv1.JSONSchemaProps{Description: describe(rp.Description), XIntOrString: true}
		}
		return g.lossy(ptr, "several types "+strings.Join(rp.Type, "|"))
	}
	if t == "" {
		switch {
		case props != nil || len(rp.PatternProperties) > 0:
			t = "object"
		case rp.Items != nil:
			t = "array"
		case len(rp.OneOf)+len(rp.AnyOf) > 0:
			return g.lossy(ptr, "oneOf/anyOf without a type")
		default:
			return g.lossy(ptr, "no type")
		}
	}
	out := apiextv1.JSONSchemaProps{Description: describe(rp.Description)}
	switch t {
	case "string":
		out.Type = "string"
		out.MinLength, out.MaxLength = rp.MinLength, rp.MaxLength
		for _, e := range rp.Enum {
			if s, ok := e.(string); ok {
				out.Enum = append(out.Enum, apiextv1.JSON{Raw: []byte(fmt.Sprintf("%q", s))})
			}
		}
		if len(out.Enum) != len(rp.Enum) {
			out.Enum = nil
		}
	case "integer", "number":
		out.Type = t
		out.Minimum, out.Maximum = rp.Minimum, rp.Maximum
	case "boolean":
		out.Type = "boolean"
	case "array":
		out.Type = "array"
		out.MinItems, out.MaxItems = rp.MinItems, rp.MaxItems
		if rp.Items == nil {
			it := preserve()
			out.Items = &apiextv1.JSONSchemaPropsOrArray{Schema: &it}
		} else {
			it := g.node(rp.Items, ptr+"/*", stack, inStatus)
			out.Items = &apiextv1.JSONSchemaPropsOrArray{Schema: &it}
		}
	case "object":
		switch {
		case props != nil:
			if inStatus {
				o := apiextv1.JSONSchemaProps{Type: "object", Description: out.Description, Properties: map[string]apiextv1.JSONSchemaProps{}}
				names, err := cfn.Names(props)
				if err != nil {
					return g.lossy(ptr, err.Error())
				}
				for _, name := range sortedKeys(props) {
					o.Properties[names.ToField[name]] = g.node(props[name], ptr+"/"+name, stack, true)
				}
				return o
			}
			o, err := g.object(rp, ptr, stack, false)
			if err != nil {
				return g.lossy(ptr, err.Error())
			}
			o.Description = out.Description
			return o
		case len(rp.PatternProperties) > 0:
			var first *cfn.Prop
			for _, k := range sortedKeys(rp.PatternProperties) {
				first = rp.PatternProperties[k]
				break
			}
			v := g.node(first, ptr+"/*", stack, inStatus)
			out.Type = "object"
			out.AdditionalProperties = &apiextv1.JSONSchemaPropsOrBool{Allows: true, Schema: &v}
		default:
			// A free-form JSON object: a policy document, a map of anything.
			yes := true
			out.Type = "object"
			out.XPreserveUnknownFields = &yes
		}
	default:
		return g.lossy(ptr, "type "+t)
	}
	if inStatus {
		// Status is written from what AWS returns, not by the user: a value
		// outside the pinned schema (a new enum value, a longer string)
		// would only make the API server refuse the status update. Types
		// and structure stay; value checks are spec's alone.
		out.Enum = nil
		out.MinLength, out.MaxLength = nil, nil
		out.Minimum, out.Maximum = nil, nil
		out.MinItems, out.MaxItems = nil, nil
	}
	return out
}
