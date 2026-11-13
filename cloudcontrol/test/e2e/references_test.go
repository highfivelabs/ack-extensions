// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/test/fake"
)

// standInCRD is an ACK kind's CRD reduced to what a reference reads: any
// spec, and a status with ACK's conditions and the kind's ID field. ACK's
// ec2-controller is not a module here (UPSTREAM.md), so its CRDs are not at
// hand; the reconciler reads the referenced object as unstructured, and
// this is the shape it reads.
func standInCRD(group, kind, plural string) *unstructured.Unstructured {
	yes := true
	free := map[string]any{"type": "object", "x-kubernetes-preserve-unknown-fields": yes}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": plural + "." + group},
		"spec": map[string]any{
			"group": group,
			"names": map[string]any{"kind": kind, "listKind": kind + "List", "plural": plural, "singular": strings.ToLower(kind)},
			"scope": "Namespaced",
			"versions": []any{map[string]any{
				"name": "v1alpha1", "served": true, "storage": true,
				"subresources": map[string]any{"status": map[string]any{}},
				"schema": map[string]any{"openAPIV3Schema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"spec": free, "status": free},
				}},
			}},
		},
	}}
	return u
}

// TestReferences runs the overlay's references through the manager on the
// schema fake: a SecurityGroupVpcAssociation naming ACK's SecurityGroup
// and VPC by their Kubernetes names (groupRef, vpcRef; the catalog's
// security-group chart). It waits while the referenced objects are not
// synced (ACK.ReferencesResolved Unknown), then sends Cloud Control the IDs
// from their status, and the object keeps only the references. The
// manager reads the ACK objects with the rights the generated ClusterRole
// gives it, nothing more.
func TestReferences(t *testing.T) {
	e := start(t)
	if e.live || e.emulator != nil {
		t.Skip("on the schema fake only: the kind is unproven (cloudcontrol/types.yaml)")
	}
	srv := e.cc.(*fake.Server)
	const typeName = "AWS::EC2::SecurityGroupVpcAssociation"
	store, ok := srv.Backend(typeName).(*fake.SchemaStore)
	if !ok {
		t.Fatalf("%s is not on the schema fake", typeName)
	}

	ec2 := "ec2.services.k8s.aws"
	for _, c := range [][2]string{{"SecurityGroup", "securitygroups"}, {"VPC", "vpcs"}} {
		if err := e.c.Create(e.ctx, standInCRD(ec2, c[0], c[1])); err != nil {
			t.Fatal(err)
		}
	}
	ackObj := func(kind, name string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{}}}
		u.SetGroupVersionKind(schema.GroupVersionKind{Group: ec2, Version: "v1alpha1", Kind: kind})
		u.SetNamespace(envNS)
		u.SetName(name)
		return u
	}
	sg, vpc := ackObj("SecurityGroup", "web"), ackObj("VPC", "main")
	eventually(t, 30*time.Second, "the stand-in CRDs served", func() error {
		for _, u := range []*unstructured.Unstructured{sg, vpc} {
			if err := e.c.Create(e.ctx, u.DeepCopy()); err != nil && !strings.Contains(err.Error(), "already exists") {
				return err
			}
		}
		return nil
	})
	// ACK's controller would set these; here the test does.
	setStatus := func(u *unstructured.Unstructured, field, value string) {
		t.Helper()
		eventually(t, 10*time.Second, "status of "+u.GetName(), func() error {
			cur := e.get(t, u)
			_ = unstructured.SetNestedField(cur.Object, value, "status", field)
			_ = unstructured.SetNestedSlice(cur.Object, []any{
				map[string]any{"type": string(ackv1alpha1.ConditionTypeResourceSynced), "status": "True"},
			}, "status", "conditions")
			return e.c.Status().Update(e.ctx, cur)
		})
	}

	ref := func(name string) map[string]any { return map[string]any{"from": map[string]any{"name": name}} }
	a := obj("SecurityGroupVpcAssociation", "web-in-main", map[string]any{"groupRef": ref("web"), "vpcRef": ref("main")})

	// Both an ID and its reference: refused by the CRD.
	both := obj("SecurityGroupVpcAssociation", "both", map[string]any{"groupRef": ref("web"), "groupId": "sg-1", "vpcId": "vpc-1"})
	if err := e.c.Create(e.ctx, both); err == nil || !strings.Contains(err.Error(), "cannot both be given") {
		t.Errorf("an ID with its reference: %v; want the CRD's refusal", err)
	}
	// Neither: refused too.
	neither := obj("SecurityGroupVpcAssociation", "neither", map[string]any{"vpcId": "vpc-1"})
	if err := e.c.Create(e.ctx, neither); err == nil || !strings.Contains(err.Error(), "one of groupId or groupRef is required") {
		t.Errorf("no group: %v; want the CRD's refusal", err)
	}

	if err := e.c.Create(e.ctx, a.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	// The referenced objects are not synced: nothing is sent.
	// ACK's runtime: Unknown while a referenced object is not synced (False
	// only for a terminal or cross-namespace one), the error in the reason.
	eventually(t, 60*time.Second, "references not resolved", func() error {
		got := e.get(t, a)
		s, reason := conditionReason(got, ackv1alpha1.ConditionTypeReferencesResolved)
		if s != "Unknown" || !strings.Contains(reason, "not synced") {
			return fmt.Errorf("ACK.ReferencesResolved=%q: %s", s, reason)
		}
		return nil
	})
	if n := len(e.calls("CreateResource", typeName)); n != 0 {
		t.Fatalf("%d CreateResource calls before the references resolved", n)
	}

	setStatus(sg, "id", "sg-0123456789abcdef0")
	setStatus(vpc, "vpcID", "vpc-0123456789abcdef0")
	got := e.waitSynced(t, a)
	if s, _ := condition(got, ackv1alpha1.ConditionTypeReferencesResolved); s != "True" {
		t.Errorf("ACK.ReferencesResolved=%q after the objects synced", s)
	}
	id, _, _ := unstructured.NestedString(got.Object, "status", "cloudControl", "identifier")
	model, ok := store.Model(id)
	if !ok {
		t.Fatalf("no model for identifier %q", id)
	}
	if model["GroupId"] != "sg-0123456789abcdef0" || model["VpcId"] != "vpc-0123456789abcdef0" {
		t.Errorf("Cloud Control got %v: want the referenced IDs", js(model))
	}
	for _, k := range []string{"groupRef", "vpcRef", "GroupRef", "VpcRef"} {
		if _, ok := model[k]; ok {
			t.Errorf("Cloud Control got the reference field %s: %v", k, js(model))
		}
	}
	spec, _, _ := unstructured.NestedMap(got.Object, "spec")
	if _, ok := spec["groupId"]; ok {
		t.Errorf("the object's spec %v holds the resolved groupId; it keeps the reference only", spec)
	}
	if _, ok := spec["vpcRef"]; !ok {
		t.Errorf("the object's spec %v lost vpcRef", spec)
	}

	// Resyncs of the unchanged object send no update.
	time.Sleep(3 * time.Second)
	if n := len(e.calls("UpdateResource", typeName)); n != 0 {
		t.Errorf("%d UpdateResource calls for an unchanged object", n)
	}

	if err := e.c.Delete(e.ctx, a); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, a)
	if _, ok := store.Model(id); ok {
		t.Errorf("%s still in the fake after the delete", id)
	}
	_ = client.IgnoreNotFound(e.c.Delete(e.ctx, sg))
}

// conditionReason is a condition's status and reason.
func conditionReason(u *unstructured.Unstructured, typ ackv1alpha1.ConditionType) (string, string) {
	cs, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range cs {
		m, _ := c.(map[string]any)
		if m["type"] == string(typ) {
			s, _ := m["status"].(string)
			r, _ := m["reason"].(string)
			return s, r
		}
	}
	return "", ""
}
