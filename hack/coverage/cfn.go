// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"sort"
	"strings"
)

// attr is one writable leaf property of a resource: a CloudFormation
// property path, or a CRD spec path.
type attr struct {
	Path     string   // display path: Foo.Bar[].Baz, maps as Foo{}
	Names    []string // the path's names without markers, for read-only matching
	Segs     []string // normalized segments (lower case, markers kept)
	Type     string
	Required bool
	Free     bool // a free-form value: an object with no known properties
}

func (a attr) key() string { return strings.Join(a.Segs, ".") }

type cfnResource struct {
	TypeName string // AWS::RDS::DBInstance
	Service  string // rds (lower case)
	Name     string // DBInstance
	Attrs    []attr
	// Primary lists the primaryIdentifier property names.
	Primary []string
	// ReadOnly counts the leaf paths removed as read-only.
	ReadOnlyLeaves int
	// Cut counts recursive references cut where a definition contains itself.
	Cut int
}

type jm = map[string]any

// normName lower-cases a name and drops everything but letters and digits.
func normName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

type cfnWalker struct {
	root     jm
	defs     jm
	readOnly [][]string
	out      []attr
	roLeaves int
	cut      int
}

func parseCFN(b []byte) (*cfnResource, error) {
	var doc jm
	if err := jsonDecode(b, &doc); err != nil {
		return nil, err
	}
	tn, _ := doc["typeName"].(string)
	parts := strings.Split(tn, "::")
	if len(parts) != 3 {
		return nil, fmt.Errorf("odd typeName %q", tn)
	}
	w := &cfnWalker{root: doc}
	w.defs, _ = doc["definitions"].(jm)
	for _, p := range strList(doc["readOnlyProperties"]) {
		w.readOnly = append(w.readOnly, pointerNames(p))
	}
	props, _ := doc["properties"].(jm)
	req := strSet(doc["required"])
	for _, name := range sortedKeys(props) {
		child, _ := props[name].(jm)
		w.walk(child, name, []string{name}, []string{normName(name)}, req[name], nil)
	}
	r := &cfnResource{TypeName: tn, Service: strings.ToLower(parts[1]), Name: parts[2], Attrs: w.out, ReadOnlyLeaves: w.roLeaves, Cut: w.cut}
	for _, p := range strList(doc["primaryIdentifier"]) {
		if n := pointerNames(p); len(n) == 1 {
			r.Primary = append(r.Primary, n[0])
		}
	}
	return r, nil
}

// pointerNames turns /properties/A/*/B into [A B].
func pointerNames(p string) []string {
	var out []string
	for _, s := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		if s == "properties" && len(out) == 0 || s == "*" || s == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

func (w *cfnWalker) isReadOnly(names []string) bool {
	for _, ro := range w.readOnly {
		if len(ro) <= len(names) {
			ok := true
			for i := range ro {
				if ro[i] != names[i] {
					ok = false
					break
				}
			}
			if ok {
				return true
			}
		}
	}
	return false
}

// deref follows $ref chains inside definitions. It returns the schema and
// the definition names followed.
func (w *cfnWalker) deref(n jm) (jm, []string) {
	var refs []string
	for i := 0; i < 20; i++ {
		ref, ok := n["$ref"].(string)
		if !ok {
			return n, refs
		}
		name := strings.TrimPrefix(ref, "#/definitions/")
		refs = append(refs, name)
		next, ok := w.defs[name].(jm)
		if !ok {
			return jm{}, refs
		}
		n = next
	}
	return n, refs
}

// subSchemas returns n and the dereferenced members of its allOf, anyOf
// and oneOf lists, flattened.
func (w *cfnWalker) members(n jm, stack []string, depth int) []jm {
	out := []jm{n}
	if depth > 6 {
		return out
	}
	for _, k := range []string{"allOf", "anyOf", "oneOf"} {
		list, _ := n[k].([]any)
		for _, e := range list {
			m, _ := e.(jm)
			m, refs := w.deref(m)
			if hasAny(refs, stack) {
				continue
			}
			out = append(out, w.members(m, append(stack, refs...), depth+1)...)
		}
	}
	return out
}

func hasAny(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func (w *cfnWalker) walk(n jm, path string, names, segs []string, required bool, stack []string) {
	n, refs := w.deref(n)
	if hasAny(refs, stack) {
		// A definition that contains itself: cut here.
		w.leaf(path, names, segs, "object", required, true)
		w.cut++
		return
	}
	stack = append(append([]string(nil), stack...), refs...)

	mem := w.members(n, stack, 0)
	props := jm{}
	reqd := map[string]bool{}
	for _, m := range mem {
		if p, ok := m["properties"].(jm); ok {
			for k, v := range p {
				if _, dup := props[k]; !dup {
					props[k] = v
				}
			}
		}
	}
	// Required names apply from the node itself and its allOf members.
	for k := range strSet(n["required"]) {
		reqd[k] = true
	}
	if list, ok := n["allOf"].([]any); ok {
		for _, e := range list {
			m, _ := e.(jm)
			m, _ = w.deref(m)
			for k := range strSet(m["required"]) {
				reqd[k] = true
			}
		}
	}
	if len(props) > 0 {
		for _, name := range sortedKeys(props) {
			child, _ := props[name].(jm)
			w.walk(child, path+"."+name, append(append([]string(nil), names...), name),
				append(append([]string(nil), segs...), normName(name)), required && reqd[name], stack)
		}
		return
	}
	if items, ok := n["items"].(jm); ok {
		w.walk(items, path+"[]", names, markLast(segs, "[]"), required, stack)
		return
	}
	// Maps: patternProperties or a typed additionalProperties.
	if pp, ok := n["patternProperties"].(jm); ok && len(pp) > 0 {
		k := sortedKeys(pp)[0]
		vs, _ := pp[k].(jm)
		w.walk(vs, path+"{}", names, markLast(segs, "{}"), required, stack)
		return
	}
	if ap, ok := n["additionalProperties"].(jm); ok {
		w.walk(ap, path+"{}", names, markLast(segs, "{}"), required, stack)
		return
	}
	t := typeString(n)
	w.leaf(path, names, segs, t, required, t == "object" || t == "")
}

func markLast(segs []string, m string) []string {
	out := append([]string(nil), segs...)
	out[len(out)-1] += m
	return out
}

func (w *cfnWalker) leaf(path string, names, segs []string, t string, required, free bool) {
	if w.isReadOnly(names) {
		w.roLeaves++
		return
	}
	if t == "" {
		t = "object"
	}
	w.out = append(w.out, attr{Path: path, Names: names, Segs: segs, Type: t, Required: required, Free: free})
}

func typeString(n jm) string {
	switch t := n["type"].(type) {
	case string:
		return t
	case []any:
		var s []string
		for _, e := range t {
			if x, ok := e.(string); ok {
				s = append(s, x)
			}
		}
		return strings.Join(s, "|")
	}
	return ""
}

func strList(v any) []string {
	var out []string
	if l, ok := v.([]any); ok {
		for _, e := range l {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func strSet(v any) map[string]bool {
	m := map[string]bool{}
	for _, s := range strList(v) {
		m[s] = true
	}
	return m
}

func sortedKeys(m jm) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
