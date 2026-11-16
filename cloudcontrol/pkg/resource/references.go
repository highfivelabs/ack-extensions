// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package resource

import (
	"context"
	"fmt"
	"strings"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrt "github.com/aws-controllers-k8s/runtime/pkg/runtime"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
)

// ResolveReferences fills each ID property the overlay gives a reference
// for (cloudcontrol/types.yaml `references`) from the object it names, as
// ACK's generated references.go does for a typed kind: the object must be
// synced and not terminal, and must carry the value; the property and its
// reference cannot both be given; another namespace needs the manager's
// --enable-cross-namespace. The referenced kinds are read as unstructured
// objects, so a reference to ACK's kinds needs no Go module of ACK's.
func (m *manager) ResolveReferences(ctx context.Context, apiReader client.Reader, res acktypes.AWSResource) (acktypes.AWSResource, bool, error) {
	if len(m.t.References) == 0 {
		return res, false, nil
	}
	out := res.DeepCopy().(*resource)
	spec := out.spec()
	has := false
	for _, ref := range m.t.References {
		raw, ok := spec[ref.RefField]
		if !ok || raw == nil {
			continue
		}
		has = true
		if v, both := spec[ref.Field]; both && v != nil {
			return out, has, ackerr.ResourceReferenceAndIDNotSupportedFor(ref.Field, ref.RefField)
		}
		val, conflict, err := m.resolve(ctx, apiReader, out.u.GetNamespace(), ref, raw)
		if err != nil {
			return out, has, err
		}
		if conflict != "" {
			out.conflict = ackerr.NewTerminalError(fmt.Errorf("%s, which it syncs as a whole: this %s would be undone at that object's next sync. Add the entry to that list instead, or empty the list to keep this %s",
				conflict, m.t.Kind, m.t.Kind))
			continue
		}
		spec[ref.Field] = val
	}
	out.setSpec(spec)
	return out, has, nil
}

// listed reports whether the object's field at path holds a value that is
// not empty (a list or map with entries, a non-empty string, any other
// value).
func listed(obj *unstructured.Unstructured, path string) bool {
	v, found, _ := unstructured.NestedFieldNoCopy(obj.Object, strings.Split(path, ".")...)
	if !found || v == nil {
		return false
	}
	switch x := v.(type) {
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	case string:
		return x != ""
	}
	return true
}

// resolve reads the referenced object and returns the value its path
// holds; or, when it lists one of the reference's refuseWhenSet fields, a
// conflict naming it and that field (and no value): the object owns that
// list as a whole, whatever its sync state.
func (m *manager) resolve(ctx context.Context, apiReader client.Reader, owner string, ref registry.Reference, raw any) (string, string, error) {
	wrapper, _ := raw.(map[string]any)
	from, _ := wrapper["from"].(map[string]any)
	name, _ := from["name"].(string)
	if name == "" {
		return "", "", fmt.Errorf("provided resource reference is nil or empty: %s", ref.RefField)
	}
	var nsp *string
	if ns, _ := from["namespace"].(string); ns != "" {
		nsp = &ns
	}
	namespace, _, err := ackrt.ValidateCrossNamespaceReference(m.cfg.EnableCrossNamespace, owner, nsp, name)
	if err != nil {
		return "", "", err
	}
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(ref.GVK())
	if err := apiReader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, obj); err != nil {
		return "", "", err
	}
	for _, f := range ref.RefuseWhenSet {
		if listed(obj, f) {
			return "", fmt.Sprintf("%s %s/%s lists %s", ref.Kind, namespace, name, f), nil
		}
	}
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	synced := false
	for _, c := range conds {
		cm, _ := c.(map[string]any)
		t, _ := cm["type"].(string)
		s, _ := cm["status"].(string)
		if s != string(corev1.ConditionTrue) {
			continue
		}
		switch t {
		case string(ackv1alpha1.ConditionTypeTerminal):
			return "", "", ackerr.ResourceReferenceTerminalFor(ref.Kind, namespace, name)
		case string(ackv1alpha1.ConditionTypeResourceSynced):
			synced = true
		}
	}
	if !synced {
		return "", "", ackerr.ResourceReferenceNotSyncedFor(ref.Kind, namespace, name)
	}
	val, found, _ := unstructured.NestedString(obj.Object, ref.PathFields()...)
	if !found || val == "" {
		return "", "", ackerr.ResourceReferenceMissingTargetFieldFor(ref.Kind, namespace, name, ref.Path)
	}
	val = strings.TrimPrefix(val, ref.TrimPrefix)
	if val == "" {
		return "", "", ackerr.ResourceReferenceMissingTargetFieldFor(ref.Kind, namespace, name, ref.Path)
	}
	return val, "", nil
}

// ClearResolvedReferences returns a copy without the values references
// filled, as ACK's generated managers do: the runtime patches the object
// it gets back and writes the API server's answer into it, which must not
// overwrite the caller's observed state, and the object keeps only the
// reference.
func (m *manager) ClearResolvedReferences(res acktypes.AWSResource) acktypes.AWSResource {
	out := res.DeepCopy().(*resource)
	if len(m.t.References) == 0 {
		return out
	}
	spec := out.spec()
	for _, ref := range m.t.References {
		if v, ok := spec[ref.RefField]; ok && v != nil {
			delete(spec, ref.Field)
		}
	}
	out.setSpec(spec)
	return out
}

// withoutReferences is a spec without the reference fields, which are not
// CloudFormation properties: what Cloud Control is sent and compared with.
func withoutReferences(t *registry.Type, spec map[string]any) map[string]any {
	if len(t.References) == 0 {
		return spec
	}
	out := make(map[string]any, len(spec))
	for k, v := range spec {
		out[k] = v
	}
	for _, ref := range t.References {
		delete(out, ref.RefField)
	}
	return out
}
