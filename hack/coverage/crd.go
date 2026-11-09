// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	"sigs.k8s.io/yaml"
)

// crdKind is one CRD: a kind and the leaf paths of its spec.
type crdKind struct {
	Group string
	Kind  string
	Attrs []attr
	// Refs counts the *Ref fields (references to other objects) skipped:
	// each stands for an ID field already counted.
	Refs int
}

func parseCRD(b []byte) (*crdKind, error) {
	j, err := yaml.YAMLToJSON(b)
	if err != nil {
		return nil, err
	}
	var doc jm
	if err := jsonDecode(j, &doc); err != nil {
		return nil, err
	}
	spec, _ := doc["spec"].(jm)
	names, _ := spec["names"].(jm)
	k := &crdKind{Group: str(spec["group"]), Kind: str(names["kind"])}
	versions, _ := spec["versions"].([]any)
	if k.Kind == "" || len(versions) == 0 {
		return nil, fmt.Errorf("not a CRD")
	}
	// The served storage version; ACK and ours have one.
	var schema jm
	for _, v := range versions {
		vm, _ := v.(jm)
		if sch, ok := vm["schema"].(jm); ok {
			schema, _ = sch["openAPIV3Schema"].(jm)
			if vm["storage"] == true {
				break
			}
		}
	}
	props, _ := schema["properties"].(jm)
	sp, _ := props["spec"].(jm)
	if sp == nil {
		return k, nil
	}
	w := &crdWalker{}
	w.walk(sp, "", nil, nil)
	k.Attrs = w.out
	k.Refs = w.refs
	return k, nil
}

func str(v any) string { s, _ := v.(string); return s }

type crdWalker struct {
	out  []attr
	refs int
}

// isRef reports whether n is ACK's reference wrapper: an object whose only
// property is `from`, or an array of them.
func isRef(n jm) bool {
	if items, ok := n["items"].(jm); ok {
		return isRef(items)
	}
	props, _ := n["properties"].(jm)
	if len(props) != 1 {
		return false
	}
	_, ok := props["from"]
	return ok
}

// isSecretRef reports ACK's SecretKeyReference: {name, namespace, key}, a
// string value read from a Kubernetes Secret.
func isSecretRef(n jm) bool {
	props, _ := n["properties"].(jm)
	if len(props) != 3 {
		return false
	}
	for _, k := range []string{"key", "name", "namespace"} {
		if _, ok := props[k]; !ok {
			return false
		}
	}
	return true
}

func (w *crdWalker) walk(n jm, path string, names, segs []string) {
	if n == nil {
		n = jm{}
	}
	if isSecretRef(n) {
		w.out = append(w.out, attr{Path: path, Names: names, Segs: segs, Type: "string"})
		return
	}
	if props, ok := n["properties"].(jm); ok && len(props) > 0 {
		for _, name := range sortedKeys(props) {
			child, _ := props[name].(jm)
			if isRef(child) {
				w.refs++
				continue
			}
			p := name
			if path != "" {
				p = path + "." + name
			}
			nsegs := append(append([]string(nil), segs...), normName(name))
			if name == "items" && typeString(child) == "array" && len(segs) > 0 {
				// CloudFront-style {quantity, items[]} wrappers: the list is the parent.
				nsegs = segs
			}
			w.walk(child, p, append(append([]string(nil), names...), name), nsegs)
		}
		return
	}
	if items, ok := n["items"].(jm); ok {
		w.walk(items, path+"[]", names, markLast(segs, "[]"))
		return
	}
	if ap, ok := n["additionalProperties"].(jm); ok && n["x-kubernetes-preserve-unknown-fields"] != true {
		w.walk(ap, path+"{}", names, markLast(segs, "{}"))
		return
	}
	if path == "" {
		return
	}
	t := typeString(n)
	free := t == "object" || n["x-kubernetes-preserve-unknown-fields"] == true
	if t == "" {
		t = "object"
	}
	w.out = append(w.out, attr{Path: path, Names: names, Segs: segs, Type: t, Free: free})
}
