// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"fmt"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/slot"
)

// TestSlotMove moves a Cloud Control resource from one version slot to
// another (docs/slot-moves.md): the manager for team-a runs as slot
// "blue" (--slot), a second manager for team-b as slot "green"
// ($HFL_SLOT), against the one fake Cloud Control. The same sequence as
// tests/e2e's TestSlotMove: blue makes it; blue pauses and releases it;
// green adopts it with the takeover annotation and re-tags it; nothing is
// recreated and green settles with no update; blue, unpaused, is refused
// (no Cloud Control write), its delete is refused, and with retain its
// object goes; a new blue adoption is refused; green deletes it.
func TestSlotMove(t *testing.T) {
	if target(t) == "real" {
		t.Skip("offline only: the slot move runs on the fake (docs/slot-moves.md)")
	}
	const typ = "AWS::EC2::TransitGatewayRouteTable"
	e := startWith(t, []string{"--slot", "blue"}, nil)
	e.runManager(t, otherNS, nil, []string{slot.EnvVar + "=green"})
	const blueNS, greenNS = envNS, otherNS

	ec := ec2.NewFromConfig(e.moto)
	tgw, err := ec.CreateTransitGateway(e.ctx, &ec2.CreateTransitGatewayInput{Description: aws.String("hub")})
	if err != nil {
		t.Fatal(err)
	}
	tgwID := aws.ToString(tgw.TransitGateway.TransitGatewayId)
	t.Cleanup(func() {
		_, _ = ec.DeleteTransitGateway(e.ctx, &ec2.DeleteTransitGatewayInput{TransitGatewayId: aws.String(tgwID)})
	})
	type table struct {
		tags    map[string]string
		created time.Time
		state   string
	}
	describe := func(t *testing.T, id string) table {
		t.Helper()
		out, err := ec.DescribeTransitGatewayRouteTables(e.ctx, &ec2.DescribeTransitGatewayRouteTablesInput{TransitGatewayRouteTableIds: []string{id}})
		if err != nil || len(out.TransitGatewayRouteTables) != 1 {
			t.Fatalf("describe %s: %v", id, err)
		}
		rt := out.TransitGatewayRouteTables[0]
		tags := map[string]string{}
		for _, tg := range rt.Tags {
			tags[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
		}
		return table{tags: tags, created: aws.ToTime(rt.CreationTime), state: string(rt.State)}
	}
	count := func(t *testing.T) int {
		t.Helper()
		out, err := ec.DescribeTransitGatewayRouteTables(e.ctx, &ec2.DescribeTransitGatewayRouteTablesInput{})
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, rt := range out.TransitGatewayRouteTables {
			if rt.State != "deleted" && aws.ToString(rt.TransitGatewayId) == tgwID {
				n++
			}
		}
		return n
	}
	writes := func(id string) int {
		n := 0
		for _, a := range []string{"UpdateResource", "DeleteResource"} {
			for _, c := range e.calls(a, typ) {
				if c.Identifier == id {
					n++
				}
			}
		}
		return n
	}
	spec := func(env string) map[string]any {
		return map[string]any{"transitGatewayId": tgwID, "tags": []any{map[string]any{"key": "env", "value": env}}}
	}
	inNS := func(u *unstructured.Unstructured, ns string, ann map[string]string) *unstructured.Unstructured {
		u.SetNamespace(ns)
		u.SetAnnotations(ann)
		return u
	}
	annotate := func(t *testing.T, u *unstructured.Unstructured, kv ...string) {
		t.Helper()
		if err := e.update(t, u, func(o *unstructured.Unstructured) {
			a := o.GetAnnotations()
			if a == nil {
				a = map[string]string{}
			}
			for i := 0; i < len(kv); i += 2 {
				if kv[i+1] == "" {
					delete(a, kv[i])
				} else {
					a[kv[i]] = kv[i+1]
				}
			}
			o.SetAnnotations(a)
		}); err != nil {
			t.Fatal(err)
		}
	}
	want := "cloud resource is held by slot green; this controller is slot blue"
	refused := func(t *testing.T, u *unstructured.Unstructured) {
		t.Helper()
		eventually(t, 60*time.Second, u.GetName()+" refused", func() error {
			got := e.get(t, u)
			cs, _, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
			var reason string
			for _, c := range cs {
				if m, _ := c.(map[string]any); m["type"] == string(ackv1alpha1.ConditionTypeTerminal) {
					reason, _ = m["reason"].(string)
				}
			}
			s, msg := condition(got, ackv1alpha1.ConditionTypeTerminal)
			r, _ := condition(got, ackv1alpha1.ConditionTypeReady)
			if s != "True" || reason != slot.Reason || msg != want || r != "False" {
				return fmt.Errorf("terminal=%q reason=%q %q ready=%q", s, reason, msg, r)
			}
			return nil
		})
		eventually(t, 30*time.Second, u.GetName()+" event", func() error {
			var evs corev1.EventList
			if err := e.c.List(e.ctx, &evs, client.InNamespace(u.GetNamespace())); err != nil {
				return err
			}
			for _, ev := range evs.Items {
				if ev.InvolvedObject.Name == u.GetName() && ev.Type == corev1.EventTypeWarning && ev.Reason == slot.Reason && ev.Message == want {
					return nil
				}
			}
			return fmt.Errorf("no SlotMismatch event among %d", len(evs.Items))
		})
	}

	// 1. blue makes it.
	blue := inNS(obj("TransitGatewayRouteTable", "spokes", spec("dev")), blueNS, nil)
	if err := e.c.Create(e.ctx, blue); err != nil {
		t.Fatal(err)
	}
	got := e.waitSynced(t, blue)
	id, _, _ := unstructured.NestedString(got.Object, "status", "transitGatewayRouteTableId")
	before := describe(t, id)
	if before.tags[slot.TagKey] != "blue" {
		t.Fatalf("made by blue, tagged %v", before.tags)
	}
	if l := got.GetLabels()[slot.LabelKey]; l != "blue" {
		t.Errorf("blue's object labelled %q", l)
	}
	n := count(t)

	// 2. Pause and release on blue.
	annotate(t, blue, ackv1alpha1.AnnotationReadOnly, "true", ackv1alpha1.AnnotationDeletionPolicy, string(ackv1alpha1.DeletionPolicyRetain))
	time.Sleep(2 * time.Second)

	// 3. green adopts it, taking it over from blue.
	green := inNS(obj("TransitGatewayRouteTable", "spokes", spec("dev")), greenNS, map[string]string{
		ackv1alpha1.AnnotationAdoptionPolicy: "adopt-or-create",
		ackv1alpha1.AnnotationAdoptionFields: fmt.Sprintf(`{"transitGatewayRouteTableId": %q}`, id),
		slot.TakeoverAnnotation:              "blue",
	})
	if err := e.c.Create(e.ctx, green); err != nil {
		t.Fatal(err)
	}
	eventually(t, 90*time.Second, "the table tagged green", func() error {
		if c := describe(t, id); c.tags[slot.TagKey] != "green" {
			return fmt.Errorf("tags %v", c.tags)
		}
		return nil
	})
	got = e.waitSynced(t, green)
	if s, _, _ := unstructured.NestedString(got.Object, "status", "cloudControl", "identifier"); s != id {
		t.Fatalf("green's identifier %q, want the adopted %q", s, id)
	}
	if got.GetLabels()[slot.LabelKey] != "green" {
		t.Errorf("green's object labels %v", got.GetLabels())
	}

	// 4. Nothing recreated; green settles with no update.
	after := describe(t, id)
	if !after.created.Equal(before.created) || after.state == "deleted" {
		t.Errorf("created %v -> %v, state %s", before.created, after.created, after.state)
	}
	if m := count(t); m != n {
		t.Errorf("%d route tables before the move, %d after", n, m)
	}
	if len(e.calls("CreateResource", typ)) != 1 {
		t.Errorf("CreateResource calls: %d, want blue's one", len(e.calls("CreateResource", typ)))
	}
	settled := writes(id)
	time.Sleep(12 * time.Second) // two resyncs
	if w := writes(id); w != settled {
		t.Errorf("green kept writing after the move: %d Cloud Control write(s)", w-settled)
	}

	// 5. blue's object, unpaused and changed, is refused: no write.
	annotate(t, blue, ackv1alpha1.AnnotationReadOnly, "")
	if err := e.update(t, blue, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedSlice(o.Object, []any{map[string]any{"key": "env", "value": "prod"}}, "spec", "tags")
	}); err != nil {
		t.Fatal(err)
	}
	refused(t, blue)
	time.Sleep(6 * time.Second)
	if w := writes(id); w != settled {
		t.Errorf("blue made %d Cloud Control write(s) to green's table", w-settled)
	}
	if c := describe(t, id); c.tags["env"] != "dev" {
		t.Errorf("tags %v", c.tags)
	}
	// ... its delete is refused ...
	annotate(t, blue, ackv1alpha1.AnnotationDeletionPolicy, string(ackv1alpha1.DeletionPolicyDelete))
	time.Sleep(time.Second)
	if err := e.c.Delete(e.ctx, blue); err != nil {
		t.Fatal(err)
	}
	time.Sleep(8 * time.Second)
	_ = e.get(t, blue) // still there
	if w := writes(id); w != settled {
		t.Errorf("blue made %d Cloud Control write(s) to green's table", w-settled)
	}
	// ... and released, its object goes, leaving the table.
	annotate(t, blue, ackv1alpha1.AnnotationDeletionPolicy, string(ackv1alpha1.DeletionPolicyRetain))
	e.waitGone(t, blue)
	if c := describe(t, id); c.state == "deleted" || c.tags[slot.TagKey] != "green" {
		t.Fatalf("after blue's object went: %+v", c)
	}

	// 6. A new blue object adopting it is refused.
	again := inNS(obj("TransitGatewayRouteTable", "spokes-again", spec("prod")), blueNS, map[string]string{
		ackv1alpha1.AnnotationAdoptionPolicy: "adopt-or-create",
		ackv1alpha1.AnnotationAdoptionFields: fmt.Sprintf(`{"transitGatewayRouteTableId": %q}`, id),
	})
	if err := e.c.Create(e.ctx, again); err != nil {
		t.Fatal(err)
	}
	refused(t, again)
	if w := writes(id); w != settled {
		t.Errorf("blue's adoption made %d Cloud Control write(s)", w-settled)
	}
	if err := e.c.Delete(e.ctx, again); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, again)

	// green deletes it: the slot holding it may.
	if err := e.c.Delete(e.ctx, green); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, green)
	if c := describe(t, id); c.state != "deleted" {
		t.Errorf("after green's delete the table is %q", c.state)
	}
}
