// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
)

// fakeDiscovery serves the kinds listed per group version; any other group
// version is not found, as the API server answers for a group with no CRD.
type fakeDiscovery struct {
	kinds map[string][]string
	err   error
	calls int
}

func (f *fakeDiscovery) ServerResourcesForGroupVersion(gv string) (*metav1.APIResourceList, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	ks, ok := f.kinds[gv]
	if !ok {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: gv}, "")
	}
	l := &metav1.APIResourceList{GroupVersion: gv}
	for _, k := range ks {
		l.APIResources = append(l.APIResources,
			metav1.APIResource{Name: k + "s", Kind: k},
			metav1.APIResource{Name: k + "s/status", Kind: k})
	}
	return l, nil
}

func refs(ts []*registry.Type) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Ref())
	}
	return out
}

func TestServedTypes(t *testing.T) {
	policy := registry.ByRef("ResourcePolicy.secretsmanager")
	eip := registry.ByRef("EIPAssociation.ec2")
	saml := registry.ByRef("SAMLProvider.iam")
	if policy == nil || eip == nil || saml == nil {
		t.Fatal("registry lacks a kind this test uses")
	}
	types := []*registry.Type{policy, eip, saml}
	// The catalog's cluster: two of the three CRDs installed, no iam group.
	d := &fakeDiscovery{kinds: map[string][]string{
		policy.GVK().GroupVersion().String(): {policy.Kind},
		eip.GVK().GroupVersion().String():    {eip.Kind, "SomethingElse"},
	}}

	served, missing, err := servedTypes(d, types, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := refs(served), []string{"ResourcePolicy.secretsmanager", "EIPAssociation.ec2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("served %v, want %v", got, want)
	}
	if want := []string{"SAMLProvider.iam"}; !reflect.DeepEqual(missing, want) {
		t.Errorf("missing %v, want %v", missing, want)
	}
	if d.calls != 3 {
		t.Errorf("%d discovery calls, want one per group version (3)", d.calls)
	}

	// Named on the command line: a kind without its CRD is an error.
	if _, missing, err := servedTypes(d, types, true); err == nil || !reflect.DeepEqual(missing, []string{"SAMLProvider.iam"}) {
		t.Errorf("explicit kinds with a CRD missing: err %v, missing %v; want an error naming SAMLProvider.iam", err, missing)
	}

	// Discovery failing for another reason stops the manager.
	if _, _, err := servedTypes(&fakeDiscovery{err: errors.New("connection refused")}, types, false); err == nil {
		t.Error("a discovery error was not returned")
	}
}

// Every kind in the registry with every CRD installed: nothing left out.
func TestServedTypesAllInstalled(t *testing.T) {
	d := &fakeDiscovery{kinds: map[string][]string{}}
	for _, ty := range registry.Types {
		gv := ty.GVK().GroupVersion().String()
		d.kinds[gv] = append(d.kinds[gv], ty.Kind)
	}
	served, missing, err := servedTypes(d, registry.Types, false)
	if err != nil || len(missing) != 0 || len(served) != len(registry.Types) {
		t.Fatalf("served %d of %d, missing %v, err %v", len(served), len(registry.Types), missing, err)
	}
}
