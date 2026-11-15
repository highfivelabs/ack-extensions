// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package resource

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	ackcfg "github.com/aws-controllers-k8s/runtime/pkg/config"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
)

// ackObject is an object of an ACK kind as the API server holds it: a
// status value and its conditions.
func ackObject(kind, ns, name, field, value string, conds ...map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetAPIVersion("ec2.services.k8s.aws/v1alpha1")
	u.SetKind(kind)
	u.SetNamespace(ns)
	u.SetName(name)
	st := map[string]any{}
	if value != "" {
		st[field] = value
	}
	var cs []any
	for _, c := range conds {
		cs = append(cs, c)
	}
	if cs != nil {
		st["conditions"] = cs
	}
	u.Object["status"] = st
	return u
}

var synced = map[string]any{"type": "ACK.ResourceSynced", "status": "True"}

func refsManager(t *testing.T, cross bool, objs ...*unstructured.Unstructured) (*manager, client.Reader) {
	t.Helper()
	ty := registry.ByTypeName("AWS::EC2::SecurityGroupVpcAssociation")
	if len(ty.References) != 2 {
		t.Fatalf("SecurityGroupVpcAssociation has %d references, want 2 (cloudcontrol/types.yaml)", len(ty.References))
	}
	b := fake.NewClientBuilder().WithScheme(runtime.NewScheme())
	for _, o := range objs {
		b = b.WithObjects(o)
	}
	cfg := ackcfg.Config{EnableCrossNamespace: cross}
	return &manager{t: ty, s: ty.Schema(), cfg: cfg, log: logr.Discard()}, b.Build()
}

func assoc(spec map[string]any) *resource {
	ty := registry.ByTypeName("AWS::EC2::SecurityGroupVpcAssociation")
	u := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	u.SetNamespace("team-a")
	u.SetName("assoc")
	return &resource{t: ty, u: u}
}

func ref(name string, ns ...string) map[string]any {
	from := map[string]any{"name": name}
	if len(ns) > 0 {
		from["namespace"] = ns[0]
	}
	return map[string]any{"from": from}
}

// A reference is filled from the referenced object's status once it is
// synced; the object keeps only its reference after
// ClearResolvedReferences; Cloud Control's model never carries the
// reference fields.
func TestResolveReferences(t *testing.T) {
	m, b := refsManager(t, false,
		ackObject("SecurityGroup", "team-a", "web", "id", "sg-0123", synced),
		ackObject("VPC", "team-a", "main", "vpcID", "vpc-0456", synced),
	)
	r := assoc(map[string]any{"groupRef": ref("web"), "vpcRef": ref("main")})
	got, has, err := m.ResolveReferences(context.Background(), b, r)
	if err != nil || !has {
		t.Fatalf("ResolveReferences: has %v, err %v", has, err)
	}
	spec := got.(*resource).spec()
	if spec["groupId"] != "sg-0123" || spec["vpcId"] != "vpc-0456" {
		t.Errorf("resolved spec %v: want groupId sg-0123, vpcId vpc-0456", spec)
	}
	if _, ok := r.spec()["groupId"]; ok {
		t.Errorf("ResolveReferences changed its input")
	}
	model, _, err := m.model(context.Background(), got.(*resource))
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := json.Marshal(model)
	if string(b2) != `{"GroupId":"sg-0123","VpcId":"vpc-0456"}` {
		t.Errorf("Cloud Control model %s: want the IDs only", b2)
	}
	cleared := m.ClearResolvedReferences(got).(*resource).spec()
	if _, ok := cleared["groupId"]; ok {
		t.Errorf("cleared spec %v still has groupId", cleared)
	}
	if _, ok := cleared["vpcRef"]; !ok {
		t.Errorf("cleared spec %v lost vpcRef", cleared)
	}
	// An ID given directly is left alone.
	direct := assoc(map[string]any{"groupId": "sg-9", "vpcId": "vpc-9"})
	got, has, err = m.ResolveReferences(context.Background(), b, direct)
	if err != nil || has || got.(*resource).spec()["groupId"] != "sg-9" {
		t.Errorf("no references: has %v, err %v, spec %v", has, err, got.(*resource).spec())
	}
	if c := m.ClearResolvedReferences(got).(*resource).spec(); c["groupId"] != "sg-9" {
		t.Errorf("ClearResolvedReferences dropped an ID given directly: %v", c)
	}
}

// What the resolver refuses, each with ACK's own error.
func TestResolveReferencesRefuses(t *testing.T) {
	terminal := map[string]any{"type": "ACK.Terminal", "status": "True"}
	m, b := refsManager(t, false,
		ackObject("SecurityGroup", "team-a", "pending", "id", ""),
		ackObject("SecurityGroup", "team-a", "broken", "id", "", terminal),
		ackObject("SecurityGroup", "team-a", "noid", "id", "", synced),
		ackObject("SecurityGroup", "team-b", "other", "id", "sg-b", synced),
		ackObject("VPC", "team-a", "main", "vpcID", "vpc-0456", synced),
	)
	cases := []struct {
		name string
		spec map[string]any
		want string
	}{
		{"not synced", map[string]any{"groupRef": ref("pending"), "vpcId": "vpc-1"}, "not synced"},
		{"terminal", map[string]any{"groupRef": ref("broken"), "vpcId": "vpc-1"}, "terminal"},
		{"no value", map[string]any{"groupRef": ref("noid"), "vpcId": "vpc-1"}, "status.id"},
		{"missing", map[string]any{"groupRef": ref("nothere"), "vpcId": "vpc-1"}, "not found"},
		{"both", map[string]any{"groupRef": ref("pending"), "groupId": "sg-1", "vpcId": "vpc-1"}, "groupId"},
		{"no name", map[string]any{"groupRef": map[string]any{"from": map[string]any{}}, "vpcId": "vpc-1"}, "groupRef"},
		{"another namespace", map[string]any{"groupRef": ref("other", "team-b"), "vpcId": "vpc-1"}, "namespace"},
	}
	for _, c := range cases {
		_, has, err := m.ResolveReferences(context.Background(), b, assoc(c.spec))
		if err == nil || !has {
			t.Errorf("%s: has %v, err %v; want an error", c.name, has, err)
			continue
		}
		if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(c.want)) {
			t.Errorf("%s: error %q does not mention %q", c.name, err, c.want)
		}
	}
	// With --enable-cross-namespace, another namespace's object is read.
	m, b = refsManager(t, true, ackObject("SecurityGroup", "team-b", "other", "id", "sg-b", synced))
	got, _, err := m.ResolveReferences(context.Background(), b, assoc(map[string]any{"groupRef": ref("other", "team-b"), "vpcId": "vpc-1"}))
	if err != nil || got.(*resource).spec()["groupId"] != "sg-b" {
		t.Errorf("cross-namespace with the flag: %v, %v", err, got)
	}
}

// The delta never sees the reference fields, and latest keeps desired's
// references (observe), so the runtime's patch of the object never drops
// them.
func TestReferencesInDeltaAndObserve(t *testing.T) {
	m, _ := refsManager(t, false)
	desired := assoc(map[string]any{"groupRef": ref("web"), "groupId": "sg-1", "vpcId": "vpc-1"})
	latest := assoc(map[string]any{"groupId": "sg-1", "vpcId": "vpc-1"})
	m.observe(latest, desired, "sg-1|vpc-1", map[string]any{"GroupId": "sg-1", "VpcId": "vpc-1", "State": "associated"})
	if _, ok := latest.spec()["groupRef"]; !ok {
		t.Errorf("observe dropped groupRef: %v", latest.spec())
	}
	d := (&descriptor{t: m.t}).Delta(desired, latest)
	if len(d.Differences) != 0 {
		t.Errorf("delta %v: want none", d.Differences)
	}
}

// A reference whose object lists one of its refuseWhenSet fields
// (cloudcontrol/types.yaml: SecurityGroupIngress's groupRef and an ACK
// SecurityGroup's spec.ingressRules) resolves without an error but carries
// a terminal conflict, synced or not; ReadOne then refuses the object with
// ACK.Terminal and asks Cloud Control nothing (api is nil here), except
// while it is being deleted. An empty list is no conflict.
func TestResolveReferencesRefuseWhenSet(t *testing.T) {
	ty := registry.ByTypeName("AWS::EC2::SecurityGroupIngress")
	var groupRef registry.Reference
	for _, r := range ty.References {
		if r.RefField == "groupRef" {
			groupRef = r
		}
	}
	if len(groupRef.RefuseWhenSet) != 1 || groupRef.RefuseWhenSet[0] != "spec.ingressRules" {
		t.Fatalf("SecurityGroupIngress groupRef refuseWhenSet %v, want [spec.ingressRules] (cloudcontrol/types.yaml)", groupRef.RefuseWhenSet)
	}
	owner := ackObject("SecurityGroup", "team-a", "owner", "id", "")
	owner.Object["spec"] = map[string]any{"ingressRules": []any{map[string]any{"ipProtocol": "tcp"}}}
	empty := ackObject("SecurityGroup", "team-a", "empty", "id", "sg-e", synced)
	empty.Object["spec"] = map[string]any{"ingressRules": []any{}, "egressRules": []any{map[string]any{"ipProtocol": "-1"}}}
	b := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).WithObjects(owner, empty).Build()
	m := &manager{t: ty, s: ty.Schema(), log: logr.Discard()}
	rule := func(group string) *resource {
		u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
			"groupRef": ref(group), "ipProtocol": "tcp", "cidrIp": "10.0.0.0/8", "fromPort": int64(443), "toPort": int64(443),
		}}}
		u.SetNamespace("team-a")
		u.SetName("rule")
		return &resource{t: ty, u: u}
	}

	got, has, err := m.ResolveReferences(context.Background(), b, rule("owner"))
	if err != nil || !has {
		t.Fatalf("ResolveReferences: has %v, err %v; want the conflict carried, not an error", has, err)
	}
	r := got.(*resource)
	if r.conflict == nil || !strings.Contains(r.conflict.Error(), "SecurityGroup team-a/owner lists spec.ingressRules") ||
		!strings.Contains(r.conflict.Error(), "this SecurityGroupIngress would be undone") {
		t.Fatalf("conflict %v: want one naming the group and its field", r.conflict)
	}
	if _, ok := r.spec()["groupId"]; ok {
		t.Errorf("a refused reference filled groupId: %v", r.spec())
	}
	if c := r.DeepCopy().(*resource).conflict; c == nil {
		t.Errorf("DeepCopy dropped the conflict")
	}
	latest, err := m.ReadOne(context.Background(), r)
	if err == nil {
		t.Fatalf("ReadOne of a refused object: no error")
	}
	s, msg := "", ""
	for _, c := range latest.Conditions() {
		if c.Type == "ACK.Terminal" {
			s = string(c.Status)
			if c.Message != nil {
				msg = *c.Message
			}
		}
	}
	if s != "True" || !strings.Contains(msg, "lists spec.ingressRules") {
		t.Errorf("ACK.Terminal=%q %q; want True naming the conflict", s, msg)
	}

	// Egress rules do not matter to an inbound rule, and an empty list is
	// no conflict.
	got, _, err = m.ResolveReferences(context.Background(), b, rule("empty"))
	if err != nil || got.(*resource).conflict != nil || got.(*resource).spec()["groupId"] != "sg-e" {
		t.Errorf("empty ingressRules: err %v, conflict %v, spec %v; want resolved", err, got.(*resource).conflict, got.(*resource).spec())
	}
}
