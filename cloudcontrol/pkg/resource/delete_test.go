// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package resource

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
)

// deletes counts DeleteResource calls; every other call is not expected.
type deletes struct {
	CloudControlAPI
	n int
}

func (d *deletes) DeleteResource(context.Context, *cloudcontrol.DeleteResourceInput, ...func(*cloudcontrol.Options)) (*cloudcontrol.DeleteResourceOutput, error) {
	d.n++
	return &cloudcontrol.DeleteResourceOutput{ProgressEvent: &cctypes.ProgressEvent{OperationStatus: cctypes.OperationStatusSuccess}}, nil
}

// A type marked neverDelete is never deleted: Delete makes no call and
// reports the object done. Every other type is deleted.
func TestNeverDelete(t *testing.T) {
	base := registry.ByTypeName("AWS::GuardDuty::Detector")
	for _, never := range []bool{false, true} {
		ty := &registry.Type{
			Group: base.Group, Version: base.Version, Kind: base.Kind, Plural: base.Plural,
			TypeName: base.TypeName, SchemaFile: base.SchemaFile, NeverDelete: never,
		}
		api := &deletes{}
		m := &manager{t: ty, s: ty.Schema(), log: logr.Discard(), api: api}
		r := &resource{t: ty, u: &unstructured.Unstructured{Object: map[string]any{}}}
		r.setIdentifier("d-1")
		out, err := m.Delete(context.Background(), r)
		if err != nil || out != nil {
			t.Errorf("neverDelete=%v: Delete gave %v, %v; want nil, nil", never, out, err)
		}
		want := 1
		if never {
			want = 0
		}
		if api.n != want {
			t.Errorf("neverDelete=%v: %d DeleteResource calls, want %d", never, api.n, want)
		}
	}
}
