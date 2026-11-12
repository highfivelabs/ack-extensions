// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/cfn"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/test/fake"
)

// TestEveryKindOnSchemaFake runs every kind moto has no backend for (marked
// unproven in cloudcontrol/types.yaml) through one manager on the schema
// fake (fake.SchemaStore; docs/testing.md): an object made from its CRD
// alone (every required field, the smallest valid value), synced and ready
// with its identifier in the status, the model Cloud Control received equal
// to the spec, no update on resyncs of the unchanged object, and the delete
// carried through. No per-type code: a type added to types.yaml is in this
// test at once. It proves our side (the CRD, the mapping, the reconciler),
// not the service's: the kinds stay unproven on the fake.
func TestEveryKindOnSchemaFake(t *testing.T) {
	e := start(t)
	if e.live {
		t.Skip("the schema fake is a fake; a live run tests the kinds through their own tests (docs/live-runs.md)")
	}
	if e.emulator != nil {
		t.Skip("ACK_TEST_BACKEND=aws-emulator: TestEveryKindOnAWSEmulator runs every kind there (docs/aws-emulator.md)")
	}
	srv, ok := e.cc.(*fake.Server)
	if !ok {
		t.Fatalf("the call log is a %T, not the fake", e.cc)
	}
	root := repoRoot(t)

	type item struct {
		ty    *registry.Type
		store *fake.SchemaStore
		u     *unstructured.Unstructured
		spec  map[string]any
		id    string
		bad   bool
	}
	var items []*item
	for i, ty := range registry.Types {
		store, ok := srv.Backend(ty.TypeName).(*fake.SchemaStore)
		if !ok {
			continue
		}
		spec, secrets := exampleSpec(t, root, ty, fmt.Sprintf("e2e-%d", i))
		for _, s := range secrets {
			if err := e.c.Create(e.ctx, s); err != nil {
				t.Fatal(err)
			}
		}
		u := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
		u.SetGroupVersionKind(ty.GVK())
		u.SetNamespace(envNS)
		u.SetName(fmt.Sprintf("%s-%d", strings.ToLower(ty.Kind), i))
		it := &item{ty: ty, store: store, u: u, spec: spec}
		if err := e.c.Create(e.ctx, u.DeepCopy()); err != nil {
			t.Errorf("%s: the API server refused the example object: %v\nspec: %s", ty.TypeName, err, js(spec))
			it.bad = true
		}
		items = append(items, it)
	}
	if len(items) == 0 {
		t.Skip("no type runs on the schema fake")
	}
	t.Logf("%d kinds on the schema fake", len(items))

	// Synced and ready, all at once: the manager reconciles them together.
	pending := func(f func(*item) error) map[*item]error {
		out := map[*item]error{}
		for _, it := range items {
			if it.bad {
				continue
			}
			if err := f(it); err != nil {
				out[it] = err
			}
		}
		return out
	}
	waitAll := func(what string, timeout time.Duration, f func(*item) error) {
		var left map[*item]error
		for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(time.Second) {
			if left = pending(f); len(left) == 0 {
				return
			}
		}
		for it, err := range left {
			t.Errorf("%s: %s: %v", it.ty.TypeName, what, err)
			it.bad = true
		}
	}
	waitAll("synced and ready", 4*time.Minute, func(it *item) error {
		got := &unstructured.Unstructured{}
		got.SetGroupVersionKind(it.u.GroupVersionKind())
		if err := e.c.Get(e.ctx, client.ObjectKeyFromObject(it.u), got); err != nil {
			return err
		}
		if s, msg := condition(got, ackv1alpha1.ConditionTypeTerminal); s == "True" {
			return fmt.Errorf("terminal: %s", msg)
		}
		s, msg := condition(got, ackv1alpha1.ConditionTypeResourceSynced)
		r, _ := condition(got, ackv1alpha1.ConditionTypeReady)
		if s != "True" || r != "True" {
			return fmt.Errorf("synced=%q ready=%q: %s", s, r, msg)
		}
		it.id, _, _ = unstructured.NestedString(got.Object, "status", "cloudControl", "identifier")
		if it.id == "" {
			return fmt.Errorf("no status.cloudControl.identifier")
		}
		return nil
	})

	// What Cloud Control received is the spec, in the type's names.
	for _, it := range items {
		if it.bad {
			continue
		}
		got, ok := it.store.Model(it.id)
		if !ok {
			t.Errorf("%s: the fake has no resource %q", it.ty.TypeName, it.id)
			continue
		}
		sc := it.ty.Schema()
		want, _ := sc.ToCFN(sc.Root(), cfn.Normalise(it.spec)).(map[string]any)
		skip := map[string]bool{}
		for _, p := range it.ty.SecretPaths() {
			skip[p[0]] = true
		}
		if sc.Tagging != nil && sc.Tagging.TagProperty != "" {
			skip[strings.TrimPrefix(sc.Tagging.TagProperty, "/properties/")] = true
		}
		for k, v := range want {
			if skip[k] {
				continue
			}
			if !reflect.DeepEqual(jsonRound(v), jsonRound(got[k])) {
				t.Errorf("%s: Cloud Control got %s = %s, the spec says %s", it.ty.TypeName, k, js(got[k]), js(v))
			}
		}
	}

	// Resyncs (every 5 seconds here) of unchanged objects make no update.
	time.Sleep(12 * time.Second)
	for _, it := range items {
		if it.bad {
			continue
		}
		if n := len(e.calls("UpdateResource", it.ty.TypeName)); n != 0 {
			t.Errorf("%s: %d update(s) of an unchanged object: %v", it.ty.TypeName, n, e.calls("UpdateResource", it.ty.TypeName))
		}
	}

	// The delete reaches Cloud Control and the object goes.
	for _, it := range items {
		if it.bad || it.ty.NeverDelete {
			continue
		}
		if err := e.c.Delete(e.ctx, it.u.DeepCopy()); err != nil {
			t.Errorf("%s: delete: %v", it.ty.TypeName, err)
			it.bad = true
		}
	}
	waitAll("deleted", 3*time.Minute, func(it *item) error {
		if it.ty.NeverDelete {
			return nil
		}
		cur := &unstructured.Unstructured{}
		cur.SetGroupVersionKind(it.u.GroupVersionKind())
		err := e.c.Get(e.ctx, client.ObjectKeyFromObject(it.u), cur)
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("object still there (err %v)", err)
		}
		if _, ok := it.store.Model(it.id); ok {
			return fmt.Errorf("the fake still has %q", it.id)
		}
		return nil
	})
}

func js(v any) string { b, _ := json.Marshal(v); return string(b) }

func jsonRound(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// exampleSpec is the smallest spec the kind's CRD accepts: every required
// field, at the least value its schema allows. Secret references get a
// Secret each, returned to be made first.
func exampleSpec(t *testing.T, root string, ty *registry.Type, name string) (map[string]any, []client.Object) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "cloudcontrol", "config", "crds", ty.Group+"_"+ty.Plural+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var crd apiextv1.CustomResourceDefinition
	if err := yaml.Unmarshal(b, &crd); err != nil {
		t.Fatalf("%s: %v", ty.TypeName, err)
	}
	specSchema := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
	spec, _ := synth(&specSchema, "spec").(map[string]any)
	if spec == nil {
		spec = map[string]any{}
	}
	var secrets []client.Object
	for i, p := range ty.SecretPaths() {
		fields := ty.Schema().FieldPath(p)
		parent := spec
		for _, f := range fields[:len(fields)-1] {
			next, _ := parent[f].(map[string]any)
			if next == nil {
				parent = nil
				break
			}
			parent = next
		}
		leaf := fields[len(fields)-1]
		if parent == nil {
			continue
		}
		if _, ok := parent[leaf]; !ok {
			continue
		}
		sn := fmt.Sprintf("%s-secret-%d", name, i)
		parent[leaf] = map[string]any{"name": sn, "key": "value"}
		secrets = append(secrets, secret(sn, map[string]string{"value": "not-a-real-secret"}))
	}
	return spec, secrets
}

// synth is the least value a structural schema node accepts.
func synth(p *apiextv1.JSONSchemaProps, path string) any {
	if len(p.Enum) > 0 {
		var v any
		_ = json.Unmarshal(p.Enum[0].Raw, &v)
		return v
	}
	switch p.Type {
	case "object":
		out := map[string]any{}
		if len(p.Properties) == 0 {
			if p.AdditionalProperties != nil && p.AdditionalProperties.Schema != nil {
				out["example"] = synth(p.AdditionalProperties.Schema, path+".example")
			}
			return out
		}
		req := append([]string(nil), p.Required...)
		sort.Strings(req)
		for _, k := range req {
			c := p.Properties[k]
			out[k] = synth(&c, path+"."+k)
		}
		return out
	case "array":
		n := int64(1)
		if p.MinItems != nil && *p.MinItems > n {
			n = *p.MinItems
		}
		var out []any
		for i := int64(0); i < n; i++ {
			var v any = map[string]any{}
			if p.Items != nil && p.Items.Schema != nil {
				v = synth(p.Items.Schema, fmt.Sprintf("%s[%d]", path, i))
			}
			if s, ok := v.(string); ok && i > 0 && len(p.Items.Schema.Enum) == 0 {
				v = fmt.Sprintf("%s%d", s, i)
			}
			out = append(out, v)
		}
		return out
	case "integer", "number":
		v := 1.0
		if p.Minimum != nil {
			v = *p.Minimum
			if p.ExclusiveMinimum {
				v++
			}
		}
		if p.Maximum != nil && v > *p.Maximum {
			v = *p.Maximum
		}
		if p.Type == "integer" {
			return int64(v)
		}
		return v
	case "boolean":
		return true
	case "string":
		s := "e2e" + strings.ReplaceAll(strings.ToLower(path[strings.LastIndex(path, ".")+1:]), "[", "")
		s = strings.ReplaceAll(s, "]", "")
		if p.Format == "date-time" {
			s = "2026-01-01T00:00:00Z"
		}
		if p.MinLength != nil {
			for int64(len(s)) < *p.MinLength {
				s += "x"
			}
		}
		if p.MaxLength != nil && int64(len(s)) > *p.MaxLength {
			s = s[:*p.MaxLength]
		}
		return s
	}
	// Free-form (x-kubernetes-preserve-unknown-fields): an empty object.
	if p.XPreserveUnknownFields != nil && *p.XPreserveUnknownFields {
		return map[string]any{}
	}
	return "e2e"
}
