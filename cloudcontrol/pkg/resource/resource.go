// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package resource is one generic implementation of ACK's runtime
// interfaces (AWSResource, AWSResourceDescriptor, AWSResourceManager and
// its factory) for every Cloud Control type in the registry. ACK's own
// runtime does the rest exactly as for ACK's generated kinds: the
// finalizer, deletion policy, adoption annotations, region and account
// selection (CARM, IAMRoleSelector), conditions (ACK.ResourceSynced,
// Ready, ACK.Terminal, ACK.Recoverable, ACK.Adopted), requeues and the
// resync. Objects are unstructured: there are no Go types per kind.
package resource

import (
	"encoding/json"
	"fmt"
	"strings"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	rtclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/cfn"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
)

// resource is one object of a Cloud Control kind.
type resource struct {
	t *registry.Type
	u *unstructured.Unstructured
}

var _ acktypes.AWSResource = &resource{}

func (r *resource) schema() *cfn.Schema { return r.t.Schema() }

// jsonValue converts any JSON-shaped Go value into the types unstructured
// objects hold (map[string]any, []any, string, bool, int64, float64).
func jsonValue(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

func (r *resource) spec() map[string]any {
	m, _ := r.u.Object["spec"].(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	return m
}

func (r *resource) setSpec(m map[string]any) { r.u.Object["spec"] = jsonValue(m) }

func (r *resource) status() map[string]any {
	m, _ := r.u.Object["status"].(map[string]any)
	if m == nil {
		m = map[string]any{}
		r.u.Object["status"] = m
	}
	return m
}

func (r *resource) cc() map[string]any {
	st := r.status()
	m, _ := st["cloudControl"].(map[string]any)
	if m == nil {
		m = map[string]any{}
		st["cloudControl"] = m
	}
	return m
}

func (r *resource) identifier() string {
	s, _, _ := unstructured.NestedString(r.u.Object, "status", "cloudControl", "identifier")
	return s
}

func (r *resource) setIdentifier(id string) {
	if id == "" {
		delete(r.cc(), "identifier")
		return
	}
	r.cc()["identifier"] = id
}

func (r *resource) digest() string {
	s, _, _ := unstructured.NestedString(r.u.Object, "status", "cloudControl", "writeOnlyDigest")
	return s
}

func (r *resource) setDigest(d string) {
	if d == "" {
		delete(r.cc(), "writeOnlyDigest")
		return
	}
	r.cc()["writeOnlyDigest"] = d
}

// request is status.cloudControl.request.
type request struct {
	Token     string `json:"token,omitempty"`
	Operation string `json:"operation,omitempty"`
	Status    string `json:"status,omitempty"`
	ErrorCode string `json:"errorCode,omitempty"`
	Message   string `json:"message,omitempty"`
	EventTime string `json:"eventTime,omitempty"`
}

func (q request) pending() bool {
	switch q.Status {
	case "PENDING", "IN_PROGRESS", "CANCEL_IN_PROGRESS":
		return q.Token != ""
	}
	return false
}

func (r *resource) request() request {
	var q request
	if m, ok, _ := unstructured.NestedMap(r.u.Object, "status", "cloudControl", "request"); ok {
		b, _ := json.Marshal(m)
		_ = json.Unmarshal(b, &q)
	}
	return q
}

func (r *resource) setRequest(q request) {
	r.cc()["request"] = jsonValue(q)
}

// Conditions are status.conditions as ACK's type.
func (r *resource) Conditions() []*ackv1alpha1.Condition {
	raw, ok, _ := unstructured.NestedFieldNoCopy(r.u.Object, "status", "conditions")
	if !ok {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var out []*ackv1alpha1.Condition
	_ = json.Unmarshal(b, &out)
	return out
}

// ReplaceConditions writes status.conditions.
func (r *resource) ReplaceConditions(cs []*ackv1alpha1.Condition) {
	if len(cs) == 0 {
		unstructured.RemoveNestedField(r.u.Object, "status", "conditions")
		return
	}
	r.status()["conditions"] = jsonValue(cs)
}

type identifiers struct{ m ackv1alpha1.ResourceMetadata }

func (i identifiers) OwnerAccountID() *ackv1alpha1.AWSAccountID { return i.m.OwnerAccountID }
func (i identifiers) ARN() *ackv1alpha1.AWSResourceName         { return i.m.ARN }
func (i identifiers) Region() *ackv1alpha1.AWSRegion            { return i.m.Region }
func (i identifiers) Partition() *ackv1alpha1.AWSPartition {
	p := ackv1alpha1.AWSPartition("aws")
	if i.m.ARN != nil {
		if parts := strings.SplitN(string(*i.m.ARN), ":", 3); len(parts) > 1 {
			p = ackv1alpha1.AWSPartition(parts[1])
		}
	}
	return &p
}

// Identifiers is status.ackResourceMetadata.
func (r *resource) Identifiers() acktypes.AWSResourceIdentifiers {
	var m ackv1alpha1.ResourceMetadata
	if raw, ok, _ := unstructured.NestedFieldNoCopy(r.u.Object, "status", "ackResourceMetadata"); ok {
		b, _ := json.Marshal(raw)
		_ = json.Unmarshal(b, &m)
	}
	return identifiers{m}
}

func (r *resource) setMetadata(account ackv1alpha1.AWSAccountID, region ackv1alpha1.AWSRegion, arn string) {
	m := ackv1alpha1.ResourceMetadata{OwnerAccountID: &account, Region: &region}
	if arn != "" {
		a := ackv1alpha1.AWSResourceName(arn)
		m.ARN = &a
	}
	r.status()["ackResourceMetadata"] = jsonValue(m)
}

// IsBeingDeleted reports a deletion timestamp.
func (r *resource) IsBeingDeleted() bool { return !r.u.GetDeletionTimestamp().IsZero() }

// RuntimeObject is the unstructured object.
func (r *resource) RuntimeObject() rtclient.Object { return r.u }

// MetaObject is the object's metadata.
func (r *resource) MetaObject() metav1.Object { return r.u }

// SetObjectMeta replaces the metadata.
func (r *resource) SetObjectMeta(meta metav1.ObjectMeta) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&meta)
	if err == nil {
		r.u.Object["metadata"] = m
	}
}

// SetIdentifiers takes an AdoptedResource's identifiers.
func (r *resource) SetIdentifiers(id *ackv1alpha1.AWSIdentifiers) error {
	if id == nil {
		return nil
	}
	switch {
	case id.NameOrID != "":
		r.setIdentifier(id.NameOrID)
	case id.ARN != nil:
		r.setIdentifier(string(*id.ARN))
	default:
		return fmt.Errorf("an identifier needs nameOrID or arn")
	}
	return nil
}

// SetStatus copies the other resource's status.
func (r *resource) SetStatus(o acktypes.AWSResource) {
	other := o.(*resource)
	if st, ok := other.u.Object["status"]; ok {
		r.u.Object["status"] = runtime.DeepCopyJSONValue(st)
	} else {
		delete(r.u.Object, "status")
	}
}

// DeepCopy copies the resource.
func (r *resource) DeepCopy() acktypes.AWSResource {
	return &resource{t: r.t, u: r.u.DeepCopy()}
}

// PopulateResourceFromAnnotation reads ACK's adoption-fields annotation:
// either {"identifier": "<Cloud Control identifier>"} or the primary
// identifier's properties by field name ({"transitGatewayRouteTableId":
// "tgw-rtb-..."}); writable ones are also set in spec.
func (r *resource) PopulateResourceFromAnnotation(fields map[string]string) error {
	if id := fields["identifier"]; id != "" {
		r.setIdentifier(id)
		return nil
	}
	s := r.schema()
	names, err := cfn.Names(s.Properties)
	if err != nil {
		return err
	}
	readOnly := cfn.NewPointerSet(s.ReadOnlyProperties)
	var parts, wanted []string
	spec := r.spec()
	for _, p := range s.PrimaryIdentifierNames() {
		f := names.ToField[p]
		wanted = append(wanted, f)
		v, ok := fields[f]
		if !ok || v == "" {
			return fmt.Errorf("adoption-fields needs %q (the primary identifier: %s) or \"identifier\"", f, strings.Join(wanted, ", "))
		}
		parts = append(parts, v)
		if !readOnly.Has(cfn.Pointer(p)) {
			spec[f] = v
		}
	}
	r.setSpec(spec)
	r.setIdentifier(strings.Join(parts, "|"))
	return nil
}
