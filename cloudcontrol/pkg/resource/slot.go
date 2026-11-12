// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package resource

import (
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/cfn"
	"github.com/highfivelabs/ack-extensions/pkg/slot"
)

// SlotTags is the slot guard's TagsFunc for the Cloud Control kinds
// (pkg/slot, docs/slot-moves.md): the observed tags of a kind whose
// schema has a top-level Key/Value tag list, the one EnsureTags writes
// ACK's default tags into. ReadOne fills spec from Cloud Control's model,
// so these are AWS's tags. Any other kind: false, unguarded
// (cloudcontrol/slot-untagged.txt).
func SlotTags(_ schema.GroupKind, observed acktypes.AWSResource) (map[string]string, bool) {
	r, ok := observed.(*resource)
	if !ok {
		return nil, false
	}
	tp := r.schema().TagProperty()
	if tp == "" {
		return nil, false
	}
	return slot.KeyValueTags(r.spec()[cfn.FieldName(tp)]), true
}
