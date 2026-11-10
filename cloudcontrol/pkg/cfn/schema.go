// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package cfn reads CloudFormation registry resource schemas (the files in
// docs/coverage/inputs/CloudformationSchema.zip) and answers the questions
// the generator and the generic reconciler ask of them: which properties
// are read-only, create-only or write-only, what the primary identifier is,
// which handlers exist and what each needs, and how a property is named on
// the Kubernetes side.
//
// Everything here is derived from the schema; nothing is per type.
package cfn

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Schema is one resource type's registry schema.
type Schema struct {
	TypeName              string              `json:"typeName"`
	Description           string              `json:"description"`
	Properties            map[string]*Prop    `json:"properties"`
	Definitions           map[string]*Prop    `json:"definitions"`
	Required              []string            `json:"required"`
	ReadOnlyProperties    []string            `json:"readOnlyProperties"`
	CreateOnlyProperties  []string            `json:"createOnlyProperties"`
	WriteOnlyProperties   []string            `json:"writeOnlyProperties"`
	PrimaryIdentifier     []string            `json:"primaryIdentifier"`
	AdditionalIdentifiers [][]string          `json:"additionalIdentifiers"`
	Handlers              map[string]*Handler `json:"handlers"`
	Tagging               *Tagging            `json:"tagging"`
	SourceURL             string              `json:"sourceUrl"`
}

// Handler is one of create, read, update, delete, list.
type Handler struct {
	Permissions      []string `json:"permissions"`
	TimeoutInMinutes int      `json:"timeoutInMinutes"`
}

// Tagging is the schema's tagging block.
type Tagging struct {
	Taggable     *bool    `json:"taggable"`
	TagOnCreate  *bool    `json:"tagOnCreate"`
	TagUpdatable *bool    `json:"tagUpdatable"`
	TagProperty  string   `json:"tagProperty"`
	Permissions  []string `json:"permissions"`
}

// Prop is a JSON Schema node as the registry uses it.
type Prop struct {
	Type                 TypeList         `json:"type"`
	Ref                  string           `json:"$ref"`
	Description          string           `json:"description"`
	Properties           map[string]*Prop `json:"properties"`
	PatternProperties    map[string]*Prop `json:"patternProperties"`
	AdditionalProperties json.RawMessage  `json:"additionalProperties"`
	Items                *Prop            `json:"items"`
	Required             []string         `json:"required"`
	Enum                 []any            `json:"enum"`
	InsertionOrder       *bool            `json:"insertionOrder"`
	UniqueItems          bool             `json:"uniqueItems"`
	MinLength            *int64           `json:"minLength"`
	MaxLength            *int64           `json:"maxLength"`
	Minimum              *float64         `json:"minimum"`
	Maximum              *float64         `json:"maximum"`
	MinItems             *int64           `json:"minItems"`
	MaxItems             *int64           `json:"maxItems"`
	OneOf                []*Prop          `json:"oneOf"`
	AnyOf                []*Prop          `json:"anyOf"`
	AllOf                []*Prop          `json:"allOf"`
	Pattern              string           `json:"pattern"`
	Format               string           `json:"format"`
	RelationshipRef      *RelationshipRef `json:"relationshipRef"`
}

// RelationshipRef is the registry's hint that a property holds another
// type's identifier (present in some schemas only).
type RelationshipRef struct {
	TypeName     string `json:"typeName"`
	PropertyPath string `json:"propertyPath"`
}

// TypeList is JSON Schema's "type", a string or a list of strings.
type TypeList []string

// UnmarshalJSON accepts a string or a list.
func (t *TypeList) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*t = TypeList{s}
		return nil
	}
	var l []string
	if err := json.Unmarshal(b, &l); err != nil {
		return err
	}
	*t = l
	return nil
}

// Single is the one type, or "" when there are none or several.
func (t TypeList) Single() string {
	if len(t) == 1 {
		return t[0]
	}
	return ""
}

// Parse reads a schema.
func Parse(b []byte) (*Schema, error) {
	var s Schema
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.TypeName == "" {
		return nil, fmt.Errorf("schema has no typeName")
	}
	return &s, nil
}

// Service is the second part of AWS::<Service>::<Resource>.
func (s *Schema) Service() string { return strings.Split(s.TypeName, "::")[1] }

// Resource is the third part of AWS::<Service>::<Resource>.
func (s *Schema) Resource() string { return strings.Split(s.TypeName, "::")[2] }

// Provisioning is how Cloud Control can manage the type, derived from the
// handlers as CloudFormation's DescribeType derives ProvisioningType:
// FULLY_MUTABLE with create, read, update and delete handlers, IMMUTABLE
// without update, NON_PROVISIONABLE without create, read or delete.
func (s *Schema) Provisioning() string {
	if s.Handlers["create"] == nil || s.Handlers["read"] == nil || s.Handlers["delete"] == nil {
		return "NON_PROVISIONABLE"
	}
	if s.Handlers["update"] == nil {
		return "IMMUTABLE"
	}
	return "FULLY_MUTABLE"
}

// Resolve follows $ref (only local "#/definitions/X" references exist in
// the registry schemas) and returns the node it names, and the name of the
// definition ("" for an inline node).
func (s *Schema) Resolve(p *Prop) (*Prop, string) {
	for i := 0; p != nil && p.Ref != "" && i < 32; i++ {
		name := strings.TrimPrefix(p.Ref, "#/definitions/")
		if name == p.Ref {
			return p, ""
		}
		d := s.Definitions[name]
		if d == nil {
			return p, ""
		}
		if d.Ref == "" {
			return d, name
		}
		p = d
	}
	return p, ""
}

// PointerSet is a set of property pointers such as /properties/Name or
// /properties/Foo/*/Bar, with prefix tests.
type PointerSet map[string]bool

// NewPointerSet builds a set from the schema's list.
func NewPointerSet(ptrs []string) PointerSet {
	out := PointerSet{}
	for _, p := range ptrs {
		out[normalisePointer(p)] = true
	}
	return out
}

// normalisePointer drops the /properties segments below the top so that
// /properties/A/properties/B and /properties/A/B (both spellings occur in
// the registry) compare equal, and writes array steps as "*".
func normalisePointer(p string) string {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	var out []string
	for i, seg := range parts {
		if seg == "properties" && i > 0 {
			continue
		}
		if seg == "items" {
			seg = "*"
		}
		out = append(out, seg)
	}
	return "/" + strings.Join(out, "/")
}

// Has reports whether the pointer itself is in the set.
func (ps PointerSet) Has(ptr string) bool { return ps[normalisePointer(ptr)] }

// Covers reports whether ptr or one of its ancestors is in the set.
func (ps PointerSet) Covers(ptr string) bool {
	p := normalisePointer(ptr)
	for {
		if ps[p] {
			return true
		}
		i := strings.LastIndex(p, "/")
		if i <= len("/properties") {
			return false
		}
		p = p[:i]
	}
}

// TopLevel is the top-level property names in the set, sorted.
func (ps PointerSet) TopLevel() []string {
	seen := map[string]bool{}
	for p := range ps {
		parts := strings.Split(strings.TrimPrefix(p, "/properties/"), "/")
		seen[parts[0]] = true
	}
	var out []string
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Pointer is the pointer of a top-level property.
func Pointer(names ...string) string { return "/properties/" + strings.Join(names, "/") }

// Sets are the schema's behaviour lists as pointer sets.
type Sets struct {
	ReadOnly, CreateOnly, WriteOnly PointerSet
}

// Sets returns the schema's behaviour lists.
func (s *Schema) Sets() Sets {
	return Sets{
		ReadOnly:   NewPointerSet(s.ReadOnlyProperties),
		CreateOnly: NewPointerSet(s.CreateOnlyProperties),
		WriteOnly:  NewPointerSet(s.WriteOnlyProperties),
	}
}

// PrimaryIdentifierNames is the top-level property names that make up the
// primary identifier, in order. Cloud Control's identifier is their values
// joined with "|".
func (s *Schema) PrimaryIdentifierNames() []string {
	var out []string
	for _, p := range s.PrimaryIdentifier {
		out = append(out, strings.TrimPrefix(normalisePointer(p), "/properties/"))
	}
	return out
}

// TagProperty is the top-level tag property's name, or "" when the type is
// not taggable or its tags are not a top-level list of Key/Value objects.
func (s *Schema) TagProperty() string {
	if s.Tagging == nil || (s.Tagging.Taggable != nil && !*s.Tagging.Taggable) {
		return ""
	}
	ptr := s.Tagging.TagProperty
	if ptr == "" {
		ptr = "/properties/Tags"
	}
	name := strings.TrimPrefix(ptr, "/properties/")
	if strings.Contains(name, "/") {
		return ""
	}
	p, _ := s.Resolve(s.Properties[name])
	if p == nil || p.Type.Single() != "array" || p.Items == nil {
		return ""
	}
	it, _ := s.Resolve(p.Items)
	if it == nil || it.Properties["Key"] == nil || it.Properties["Value"] == nil {
		return ""
	}
	return name
}

// ComparedProperty is the top-level property name's node as Satisfies
// should compare it: the schema's, except that the tag property is
// unordered whatever its insertionOrder says. Tags are a set by key: AWS
// returns them in its own order, and ACK's default tags (--resource-tags)
// come from a map.
func (s *Schema) ComparedProperty(name string) *Prop {
	p := s.Properties[name]
	if p == nil || name != s.TagProperty() {
		return p
	}
	r, _ := s.Resolve(p)
	c := *r
	unordered := false
	c.InsertionOrder = &unordered
	return &c
}

// Unordered reports whether an array node's order does not matter.
func Unordered(p *Prop) bool { return p != nil && p.InsertionOrder != nil && !*p.InsertionOrder }

// ObjectProperties is the properties of an object node, merging oneOf,
// anyOf and allOf branches (the registry uses them for alternatives of an
// object's fields). Free-form objects return nil.
func (s *Schema) ObjectProperties(p *Prop) map[string]*Prop {
	p, _ = s.Resolve(p)
	if p == nil {
		return nil
	}
	out := map[string]*Prop{}
	for k, v := range p.Properties {
		out[k] = v
	}
	for _, branches := range [][]*Prop{p.OneOf, p.AnyOf, p.AllOf} {
		for _, b := range branches {
			for k, v := range s.ObjectProperties(b) {
				if _, ok := out[k]; !ok {
					out[k] = v
				}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
