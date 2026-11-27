// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"strings"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/test/fake"
)

// TestReferencesDNSSEC: DNSSEC signing and a key-signing key for a hosted
// zone named by reference to ACK's route53 HostedZone (hostedZoneRef; the
// catalog's route53 chart, krm-packages PR #234), the key's KMS key by
// reference to ACK's kms Key (keyManagementServiceRef), on the schema
// fake: moto 5.2.5 has no DNSSEC calls. ACK's HostedZone keeps its ID as
// /hostedzone/Z...; Cloud Control is sent the bare ID (the reference's
// trimPrefix). Nothing is sent before the referenced objects sync.
func TestReferencesDNSSEC(t *testing.T) {
	e := start(t, "DNSSEC.route53", "KeySigningKey.route53")
	if e.live || e.emulator != nil {
		t.Skip("on the schema fake only: the kinds are unproven (cloudcontrol/types.yaml)")
	}
	srv := e.cc.(*fake.Server)
	const dnssecType, kskType = "AWS::Route53::DNSSEC", "AWS::Route53::KeySigningKey"
	dnssecStore, ok1 := srv.Backend(dnssecType).(*fake.SchemaStore)
	kskStore, ok2 := srv.Backend(kskType).(*fake.SchemaStore)
	if !ok1 || !ok2 {
		t.Fatalf("%s or %s is not on the schema fake", dnssecType, kskType)
	}

	standIn := func(group, kind, plural, name string) *unstructured.Unstructured {
		t.Helper()
		if err := e.c.Create(e.ctx, standInCRD(group, kind, plural)); err != nil && !strings.Contains(err.Error(), "already exists") {
			t.Fatal(err)
		}
		u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{}}}
		u.SetGroupVersionKind(schema.GroupVersionKind{Group: group, Version: "v1alpha1", Kind: kind})
		u.SetNamespace(envNS)
		u.SetName(name)
		eventually(t, 30*time.Second, "the stand-in "+kind+" served", func() error {
			if err := e.c.Create(e.ctx, u.DeepCopy()); err != nil && !strings.Contains(err.Error(), "already exists") {
				return err
			}
			return nil
		})
		return u
	}
	setSynced := func(u *unstructured.Unstructured, value string, path ...string) {
		t.Helper()
		eventually(t, 10*time.Second, "status of "+u.GetName(), func() error {
			cur := e.get(t, u)
			_ = unstructured.SetNestedField(cur.Object, value, append([]string{"status"}, path...)...)
			_ = unstructured.SetNestedSlice(cur.Object, []any{
				map[string]any{"type": string(ackv1alpha1.ConditionTypeResourceSynced), "status": "True"},
			}, "status", "conditions")
			return e.c.Status().Update(e.ctx, cur)
		})
	}
	zone := standIn("route53.services.k8s.aws", "HostedZone", "hostedzones", "example")
	key := standIn("kms.services.k8s.aws", "Key", "keys", "dnssec")

	both := objOf(dnssecType, "both", map[string]any{"hostedZoneRef": refTo("example"), "hostedZoneId": "Z1"})
	if err := e.c.Create(e.ctx, both); err == nil || !strings.Contains(err.Error(), "cannot both be given") {
		t.Errorf("an ID with its reference: %v; want the CRD's refusal", err)
	}

	const keyARN = "arn:aws:kms:us-east-1:123456789012:key/0f3e5a8c-1111-2222-3333-444455556666"
	ksk := objOf(kskType, "ksk", map[string]any{
		"hostedZoneRef": refTo("example"), "keyManagementServiceRef": refTo("dnssec"),
		"name": "example_ksk", "status": "ACTIVE",
	})
	signing := objOf(dnssecType, "signing", map[string]any{"hostedZoneRef": refTo("example")})
	for _, u := range []*unstructured.Unstructured{ksk, signing} {
		if err := e.c.Create(e.ctx, u.DeepCopy()); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(3 * time.Second)
	if n := len(e.calls("CreateResource", dnssecType)) + len(e.calls("CreateResource", kskType)); n != 0 {
		t.Fatalf("%d CreateResource calls before the references resolved", n)
	}

	setSynced(zone, "/hostedzone/Z0123456789ABCDEFGHIJ", "id")
	setSynced(key, keyARN, "ackResourceMetadata", "arn")

	for _, tc := range []struct {
		u     *unstructured.Unstructured
		store *fake.SchemaStore
		want  map[string]string
	}{
		{ksk, kskStore, map[string]string{"HostedZoneId": "Z0123456789ABCDEFGHIJ", "KeyManagementServiceArn": keyARN, "Name": "example_ksk"}},
		{signing, dnssecStore, map[string]string{"HostedZoneId": "Z0123456789ABCDEFGHIJ"}},
	} {
		got := e.waitSynced(t, tc.u)
		id, _, _ := unstructured.NestedString(got.Object, "status", "cloudControl", "identifier")
		model, ok := tc.store.Model(id)
		if !ok {
			t.Fatalf("%s: no model for identifier %q", tc.u.GetName(), id)
		}
		for k, v := range tc.want {
			if model[k] != v {
				t.Errorf("%s: Cloud Control got %s=%v, want %s (%s)", tc.u.GetName(), k, model[k], v, js(model))
			}
		}
		spec, _, _ := unstructured.NestedMap(got.Object, "spec")
		if _, ok := spec["hostedZoneId"]; ok {
			t.Errorf("%s: the object's spec %v holds the resolved hostedZoneId; it keeps the reference only", tc.u.GetName(), spec)
		}
	}
	for _, u := range []*unstructured.Unstructured{signing, ksk} {
		if err := e.c.Delete(e.ctx, u); err != nil {
			t.Fatal(err)
		}
		e.waitGone(t, u)
	}
}
