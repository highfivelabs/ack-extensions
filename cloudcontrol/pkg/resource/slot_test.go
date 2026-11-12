// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package resource

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
	"github.com/highfivelabs/ack-extensions/pkg/slot"
)

// SlotTags reads the observed tags of a kind with a top-level Key/Value
// tag list, and refuses (false) a kind without one: its slot cannot be
// read from AWS, so the guard leaves it alone.
func TestSlotTags(t *testing.T) {
	ty := registry.ByTypeName("AWS::EC2::TransitGatewayRouteTable")
	r := &resource{t: ty, u: &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"tags": []any{
			map[string]any{"key": slot.TagKey, "value": "blue"},
			map[string]any{"key": "env", "value": "dev"},
		}},
	}}}
	tags, ok := SlotTags(ty.GVK().GroupKind(), r)
	if !ok || tags[slot.TagKey] != "blue" || tags["env"] != "dev" {
		t.Errorf("route table: %v %v", tags, ok)
	}
	ty = registry.ByTypeName("AWS::EC2::TransitGatewayRoute")
	if _, ok := SlotTags(ty.GVK().GroupKind(), &resource{t: ty, u: &unstructured.Unstructured{Object: map[string]any{}}}); ok {
		t.Error("TransitGatewayRoute has no tags; SlotTags said it had")
	}
}

// The kinds the slot guard cannot cover, listed for docs/slot-moves.md in
// cloudcontrol/slot-untagged.txt; UPDATE_SLOT_UNTAGGED=1 rewrites it.
func TestSlotUntaggedList(t *testing.T) {
	var names []string
	for _, ty := range registry.Types {
		if ty.Schema().TagProperty() == "" {
			names = append(names, ty.TypeName)
		}
	}
	sort.Strings(names)
	want := "# Cloud Control kinds the slot guard cannot cover: no top-level Key/Value\n" +
		"# tag list in the type's schema (docs/slot-moves.md). Written by\n" +
		"# UPDATE_SLOT_UNTAGGED=1 go test ./cloudcontrol/pkg/resource -run TestSlotUntaggedList\n" +
		strings.Join(names, "\n") + "\n"
	path := filepath.Join("..", "..", "slot-untagged.txt")
	if os.Getenv("UPDATE_SLOT_UNTAGGED") != "" {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("%s is out of date (%d untagged kinds); rerun with UPDATE_SLOT_UNTAGGED=1", path, len(names))
	}
}
