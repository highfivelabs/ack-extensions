// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package resource

import (
	"context"
	"testing"

	ackcfg "github.com/aws-controllers-k8s/runtime/pkg/config"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
)

func tag(k, v string) map[string]any { return map[string]any{"Key": k, "Value": v} }

// A type's tags are a set by key, whatever the schema's insertionOrder
// says: AWS::EC2::TransitGatewayPeeringAttachment's Tags leave
// insertionOrder out (so true), and a reordering alone must be no change.
// A changed value still is one.
func TestTagOrderIsNoChange(t *testing.T) {
	s := registry.ByTypeName("AWS::EC2::TransitGatewayPeeringAttachment").Schema()
	observed := map[string]any{"Id": "tgw-attach-1", "Tags": []any{tag("a", "1"), tag("b", "2")}}

	ops, err := Patch(s, map[string]any{"Tags": []any{tag("b", "2"), tag("a", "1")}}, observed, false)
	if err != nil || len(ops) != 0 {
		t.Errorf("reordered tags: ops %s, err %v; want none", js(ops), err)
	}
	ops, err = Patch(s, map[string]any{"Tags": []any{tag("b", "3"), tag("a", "1")}}, observed, false)
	if err != nil || len(ops) != 1 || ops[0].Path != "/Tags" {
		t.Errorf("changed tag value: ops %s, err %v; want one replace /Tags", js(ops), err)
	}

	ty := registry.ByTypeName("AWS::EC2::TransitGatewayPeeringAttachment")
	spec := func(tags ...any) *resource {
		return &resource{t: ty, u: &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{"tags": []any(tags)},
		}}}
	}
	ftag := func(k, v string) map[string]any { return map[string]any{"key": k, "value": v} }
	d := &descriptor{t: ty}
	if delta := d.Delta(spec(ftag("b", "2"), ftag("a", "1")), spec(ftag("a", "1"), ftag("b", "2"))); len(delta.Differences) != 0 {
		t.Errorf("reordered tags: delta %v; want none", delta.Differences)
	}
	if delta := d.Delta(spec(ftag("b", "3"), ftag("a", "1")), spec(ftag("a", "1"), ftag("b", "2"))); len(delta.Differences) != 1 {
		t.Errorf("changed tag value: delta %v; want one difference", delta.Differences)
	}
}

// ACK's default tags (--resource-tags) come as a map; EnsureTags appends
// them in key order, so every reconcile builds the same desired list.
func TestEnsureTagsKeyOrder(t *testing.T) {
	ty := registry.ByTypeName("AWS::EC2::TransitGatewayPeeringAttachment")
	cfg := ackcfg.Config{ResourceTags: []string{"e=5", "c=3", "a=1", "d=4", "b=2"}}
	m := &manager{t: ty, s: ty.Schema(), log: logr.Discard(), cfg: cfg}
	for i := 0; i < 50; i++ {
		r := &resource{t: ty, u: &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{"tags": []any{map[string]any{"key": "z", "value": "0"}}},
		}}}
		if err := m.EnsureTags(context.Background(), r, acktypes.ServiceControllerMetadata{ServiceAlias: "cloudcontrol"}); err != nil {
			t.Fatal(err)
		}
		var keys string
		for _, tg := range r.spec()["tags"].([]any) {
			keys += tg.(map[string]any)["key"].(string)
		}
		if keys != "zabcde" {
			t.Fatalf("run %d: tag keys in order %q, want %q (the object's own, then the defaults by key)", i, keys, "zabcde")
		}
	}
}
