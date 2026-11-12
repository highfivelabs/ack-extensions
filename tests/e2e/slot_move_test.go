// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/slot"
	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
)

// moveKind is one kind in a slot move: how to make its object, where its
// AWS identifier is, and what AWS says about it.
type moveKind struct {
	kind     string
	idField  string // status field, and the adoption-fields key
	spec     func(userTag string) map[string]any
	describe func(ctx context.Context, c *ec2.Client, id string) (moveCloud, error)
	count    func(ctx context.Context, c *ec2.Client) (int, error)
}

// moveCloud is the resource as AWS has it.
type moveCloud struct {
	tags    map[string]string
	created time.Time // zero where AWS keeps no creation time
	gone    bool
}

var moveKinds = []moveKind{
	{
		kind: "Volume", idField: "volumeID",
		spec: func(app string) map[string]any {
			return map[string]any{
				"availabilityZone": "us-west-2a", "size": int64(20), "volumeType": "gp3",
				"tags": []any{map[string]any{"key": "app", "value": app}},
			}
		},
		describe: func(ctx context.Context, c *ec2.Client, id string) (moveCloud, error) {
			v, err := cloudVolume(ctx, c, id)
			if err != nil {
				return moveCloud{}, err
			}
			return moveCloud{tags: ec2TagMap(v.Tags), created: aws.ToTime(v.CreateTime), gone: string(v.State) == "deleted"}, nil
		},
		count: func(ctx context.Context, c *ec2.Client) (int, error) {
			out, err := c.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{})
			if err != nil {
				return 0, err
			}
			return len(out.Volumes), nil
		},
	},
	{
		kind: "CustomerGateway", idField: "customerGatewayID",
		spec: func(site string) map[string]any {
			return map[string]any{
				"bgpASN": int64(65010), "ipAddress": "203.0.113.12", "type_": "ipsec.1",
				"tags": []any{map[string]any{"key": "app", "value": site}},
			}
		},
		describe: func(ctx context.Context, c *ec2.Client, id string) (moveCloud, error) {
			cg, err := cloudCustomerGateway(ctx, c, id)
			if err != nil {
				return moveCloud{}, err
			}
			return moveCloud{tags: ec2TagMap(cg.Tags), gone: aws.ToString(cg.State) == "deleted"}, nil
		},
		count: func(ctx context.Context, c *ec2.Client) (int, error) {
			out, err := c.DescribeCustomerGateways(ctx, &ec2.DescribeCustomerGatewaysInput{})
			if err != nil {
				return 0, err
			}
			n := 0
			for _, g := range out.CustomerGateways {
				if aws.ToString(g.State) != "deleted" {
					n++
				}
			}
			return n, nil
		},
	},
}

// TestSlotMove moves resources of ACK-generated kinds from one version
// slot to another (docs/slot-moves.md): two runs of the ec2 controller,
// slot "blue" (--slot) watching one namespace and slot "green" ($HFL_SLOT)
// another, against one moto. The sequence Bundt runs:
//
//  1. blue makes the resource, tagged hfl.dev/slot=blue;
//  2. blue's object is paused and released: read-only, deletion-policy retain;
//  3. green adopts it: adopt-or-create, adoption-fields, and
//     hfl.dev/slot-takeover=blue; the tag becomes green;
//  4. nothing was recreated (same ID, creation time, count) and green
//     settles with no diff;
//  5. blue's object, unpaused, is refused (SlotMismatch, no write), its
//     delete is refused too, and with retain it goes, leaving the resource;
//  6. a new blue object adopting the resource is refused.
func TestSlotMove(t *testing.T) {
	const blueNS, greenNS = "slot-blue", "slot-green"
	env, _ := harness.StartControllers(t, "ec2", runtime.NewScheme(),
		harness.Controller{Args: []string{"--watch-namespace", blueNS, "--slot", "blue"}},
		harness.Controller{Args: []string{"--watch-namespace", greenNS}, Env: []string{slot.EnvVar + "=green"}},
	)
	ctx := context.Background()
	cloud := ec2.NewFromConfig(env.AWS)
	for _, ns := range []string{blueNS, greenNS} {
		if err := env.Client.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
			t.Fatal(err)
		}
	}

	for _, k := range moveKinds {
		t.Run(k.kind, func(t *testing.T) {
			gvk := schema.GroupVersionKind{Group: "ec2.aws.hfl.dev", Version: "v1alpha1", Kind: k.kind}
			newObj := func(ns, name string, spec map[string]any, ann map[string]string) *unstructured.Unstructured {
				u := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
				u.SetGroupVersionKind(gvk)
				u.SetNamespace(ns)
				u.SetName(name)
				u.SetAnnotations(ann)
				return u
			}
			get := func(u *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				got := &unstructured.Unstructured{}
				got.SetGroupVersionKind(gvk)
				return got, env.Client.Get(ctx, client.ObjectKeyFromObject(u), got)
			}
			waitSynced := func(t *testing.T, u *unstructured.Unstructured) *unstructured.Unstructured {
				t.Helper()
				var got *unstructured.Unstructured
				harness.Eventually(t, 90*time.Second, u.GetNamespace()+"/"+u.GetName()+" synced", func() error {
					var err error
					if got, err = get(u); err != nil {
						return err
					}
					if ok, why := synced(uConditions(got)); !ok {
						return fmt.Errorf("not synced: %s", why)
					}
					return nil
				})
				return got
			}
			annotate := func(t *testing.T, u *unstructured.Unstructured, kv ...string) {
				t.Helper()
				harness.Eventually(t, 30*time.Second, "annotate "+u.GetName(), func() error {
					got, err := get(u)
					if err != nil {
						return err
					}
					a := got.GetAnnotations()
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
					got.SetAnnotations(a)
					return env.Client.Update(ctx, got)
				})
			}
			describe := func(t *testing.T, id string) moveCloud {
				t.Helper()
				c, err := k.describe(ctx, cloud, id)
				if err != nil {
					t.Fatal(err)
				}
				return c
			}
			gone := func(t *testing.T, u *unstructured.Unstructured) {
				t.Helper()
				harness.Eventually(t, 60*time.Second, u.GetName()+" gone", func() error {
					if _, err := get(u); err == nil {
						return fmt.Errorf("object still present")
					} else {
						return client.IgnoreNotFound(err)
					}
				})
			}
			lower := strings.ToLower(k.kind)

			// 1. blue makes it.
			blue := newObj(blueNS, lower, k.spec("db"), nil)
			if err := env.Client.Create(ctx, blue); err != nil {
				t.Fatal(err)
			}
			got := waitSynced(t, blue)
			id, _, _ := unstructured.NestedString(got.Object, "status", k.idField)
			if id == "" {
				t.Fatalf("no status.%s", k.idField)
			}
			before := describe(t, id)
			if before.tags[slot.TagKey] != "blue" {
				t.Fatalf("made by blue, tagged %v", before.tags)
			}
			if l := got.GetLabels()[slot.LabelKey]; l != "blue" {
				t.Errorf("blue's object labelled %q", l)
			}
			count, err := k.count(ctx, cloud)
			if err != nil {
				t.Fatal(err)
			}

			// 2. Pause and release on blue.
			annotate(t, blue, ackv1alpha1.AnnotationReadOnly, "true", ackv1alpha1.AnnotationDeletionPolicy, string(ackv1alpha1.DeletionPolicyRetain))
			time.Sleep(2 * time.Second)

			// 3. green adopts it, taking it over from blue.
			green := newObj(greenNS, lower, k.spec("db"), map[string]string{
				ackv1alpha1.AnnotationAdoptionPolicy: "adopt-or-create",
				ackv1alpha1.AnnotationAdoptionFields: fmt.Sprintf(`{%q: %q}`, k.idField, id),
				slot.TakeoverAnnotation:              "blue",
			})
			if err := env.Client.Create(ctx, green); err != nil {
				t.Fatal(err)
			}
			got = waitSynced(t, green)
			if gid, _, _ := unstructured.NestedString(got.Object, "status", k.idField); gid != id {
				t.Fatalf("green's status.%s %q, want the adopted %q", k.idField, gid, id)
			}
			if got.GetAnnotations()[ackv1alpha1.AnnotationAdopted] != "true" {
				t.Errorf("green's object not marked adopted: %v", got.GetAnnotations())
			}
			harness.Eventually(t, 60*time.Second, "the resource tagged green", func() error {
				if c := describe(t, id); c.tags[slot.TagKey] != "green" {
					return fmt.Errorf("tags %v", c.tags)
				}
				return nil
			})
			harness.Eventually(t, 30*time.Second, "green's object labelled", func() error {
				got, err := get(green)
				if err != nil {
					return err
				}
				if l := got.GetLabels()[slot.LabelKey]; l != "green" {
					return fmt.Errorf("label %q", l)
				}
				return nil
			})

			// 4. Nothing recreated; green settles with no diff.
			after := describe(t, id)
			if !after.created.Equal(before.created) || after.gone {
				t.Errorf("created %v -> %v, gone %v", before.created, after.created, after.gone)
			}
			if n, err := k.count(ctx, cloud); err != nil || n != count {
				t.Errorf("%d %s(s) before the move, %d after (%v)", count, k.kind, n, err)
			}
			time.Sleep(12 * time.Second) // two resyncs
			got = waitSynced(t, green)
			if c := describe(t, id); c.tags["app"] != "db" || c.tags[slot.TagKey] != "green" {
				t.Errorf("after settling, tags %v", c.tags)
			}
			if tags := slot.KeyValueTags(nestedSlice(got, "spec", "tags")); len(tags) != 1 || tags["app"] != "db" {
				t.Errorf("green's spec.tags %v: the object's own tags only", tags)
			}

			// 5. blue's object, unpaused and changed, is refused.
			want := "cloud resource is held by slot green; this controller is slot blue"
			annotate(t, blue, ackv1alpha1.AnnotationReadOnly, "")
			harness.Eventually(t, 30*time.Second, "blue's change", func() error {
				got, err := get(blue)
				if err != nil {
					return err
				}
				_ = unstructured.SetNestedSlice(got.Object, []any{map[string]any{"key": "app", "value": "web"}}, "spec", "tags")
				return env.Client.Update(ctx, got)
			})
			refused := func(t *testing.T, u *unstructured.Unstructured) {
				t.Helper()
				harness.Eventually(t, 60*time.Second, u.GetName()+" refused", func() error {
					got, err := get(u)
					if err != nil {
						return err
					}
					var term, ready *ackv1alpha1.Condition
					for _, c := range uConditions(got) {
						switch c.Type {
						case ackv1alpha1.ConditionTypeTerminal:
							term = c
						case ackv1alpha1.ConditionTypeReady:
							ready = c
						}
					}
					if term == nil || term.Status != corev1.ConditionTrue || aws.ToString(term.Reason) != slot.Reason || aws.ToString(term.Message) != want {
						return fmt.Errorf("terminal %+v", term)
					}
					if ready == nil || ready.Status != corev1.ConditionFalse {
						return fmt.Errorf("ready %+v", ready)
					}
					return nil
				})
				harness.Eventually(t, 30*time.Second, u.GetName()+" event", func() error {
					var evs corev1.EventList
					if err := env.Client.List(ctx, &evs, client.InNamespace(u.GetNamespace())); err != nil {
						return err
					}
					for _, e := range evs.Items {
						if e.InvolvedObject.Name == u.GetName() && e.InvolvedObject.Kind == k.kind && e.Type == corev1.EventTypeWarning && e.Reason == slot.Reason && e.Message == want {
							return nil
						}
					}
					return fmt.Errorf("no SlotMismatch event among %d", len(evs.Items))
				})
			}
			refused(t, blue)
			time.Sleep(6 * time.Second)
			if c := describe(t, id); c.tags["app"] != "db" || c.tags[slot.TagKey] != "green" {
				t.Errorf("blue wrote green's resource: tags %v", c.tags)
			}

			// ... its delete is refused (no finalizer removed, the resource stays) ...
			annotate(t, blue, ackv1alpha1.AnnotationDeletionPolicy, string(ackv1alpha1.DeletionPolicyDelete))
			time.Sleep(time.Second)
			if err := env.Client.Delete(ctx, blue); err != nil {
				t.Fatal(err)
			}
			time.Sleep(8 * time.Second)
			if _, err := get(blue); err != nil {
				t.Fatalf("blue's object went with deletion-policy delete: %v", err)
			}
			if c := describe(t, id); c.gone {
				t.Fatal("blue deleted green's resource")
			}
			// ... and released, it goes, leaving the resource.
			annotate(t, blue, ackv1alpha1.AnnotationDeletionPolicy, string(ackv1alpha1.DeletionPolicyRetain))
			gone(t, blue)
			if c := describe(t, id); c.gone || c.tags[slot.TagKey] != "green" {
				t.Fatalf("after blue's object went: gone %v tags %v", c.gone, c.tags)
			}

			// 6. A new blue object adopting it is refused.
			again := newObj(blueNS, lower+"-again", k.spec("web"), map[string]string{
				ackv1alpha1.AnnotationAdoptionPolicy: "adopt-or-create",
				ackv1alpha1.AnnotationAdoptionFields: fmt.Sprintf(`{%q: %q}`, k.idField, id),
			})
			if err := env.Client.Create(ctx, again); err != nil {
				t.Fatal(err)
			}
			refused(t, again)
			if c := describe(t, id); c.tags["app"] != "db" {
				t.Errorf("blue's adoption wrote the resource: tags %v", c.tags)
			}
			if err := env.Client.Delete(ctx, again); err != nil {
				t.Fatal(err)
			}
			gone(t, again)

			// green deletes it: the slot holding it may.
			if err := env.Client.Delete(ctx, green); err != nil {
				t.Fatal(err)
			}
			gone(t, green)
			harness.Eventually(t, 60*time.Second, "the resource deleted by green", func() error {
				c, err := k.describe(ctx, cloud, id)
				if err != nil || c.gone {
					return nil // not found, or in state deleted
				}
				return fmt.Errorf("still there")
			})
		})
	}
}

func uConditions(u *unstructured.Unstructured) []*ackv1alpha1.Condition {
	raw, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	var out []*ackv1alpha1.Condition
	for _, r := range raw {
		m, _ := r.(map[string]any)
		c := &ackv1alpha1.Condition{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(m, c); err == nil {
			out = append(out, c)
		}
	}
	return out
}

func nestedSlice(u *unstructured.Unstructured, path ...string) any {
	l, _, _ := unstructured.NestedSlice(u.Object, path...)
	return l
}
