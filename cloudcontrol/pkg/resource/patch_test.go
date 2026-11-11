// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package resource

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
)

func js(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestPatchNeverTouchesReadOnlyOrCreateOnly(t *testing.T) {
	s := registry.ByTypeName("AWS::EC2::TransitGatewayRouteTable").Schema()
	desired := map[string]any{"TransitGatewayId": "tgw-1", "Tags": []any{map[string]any{"Key": "a", "Value": "2"}}}
	observed := map[string]any{"TransitGatewayId": "tgw-1", "TransitGatewayRouteTableId": "tgw-rtb-1", "Tags": []any{map[string]any{"Key": "a", "Value": "1"}}}
	ops, err := Patch(s, desired, observed, false)
	if err != nil {
		t.Fatal(err)
	}
	if js(ops) != `[{"op":"replace","path":"/Tags","value":[{"Key":"a","Value":"2"}]}]` {
		t.Errorf("ops %s", js(ops))
	}
	// A create-only difference is an error, not an op.
	desired["TransitGatewayId"] = "tgw-2"
	_, err = Patch(s, desired, observed, false)
	var co *CreateOnlyChange
	if !errors.As(err, &co) || co.Property != "TransitGatewayId" {
		t.Errorf("want a CreateOnlyChange, got %v", err)
	}
}

func TestPatchUnorderedAndMerge(t *testing.T) {
	s := registry.ByTypeName("AWS::GuardDuty::Detector").Schema()
	observed := map[string]any{
		"Id": "d", "Enable": true,
		"DataSources": map[string]any{"S3Logs": map[string]any{"Enable": true}, "Kubernetes": map[string]any{"AuditLogs": map[string]any{"Enable": true}}},
		"Features":    []any{map[string]any{"Name": "B", "Status": "ENABLED"}, map[string]any{"Name": "A", "Status": "ENABLED"}},
	}
	desired := map[string]any{
		"Enable":      true,
		"DataSources": map[string]any{"S3Logs": map[string]any{"Enable": false}},
		"Features":    []any{map[string]any{"Name": "A", "Status": "ENABLED"}, map[string]any{"Name": "B", "Status": "ENABLED"}},
	}
	ops, err := Patch(s, desired, observed, false)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"op":"replace","path":"/DataSources","value":{"Kubernetes":{"AuditLogs":{"Enable":true}},"S3Logs":{"Enable":false}}}]`
	if js(ops) != want {
		t.Errorf("ops %s\nwant %s", js(ops), want)
	}
}

func TestPatchWriteOnly(t *testing.T) {
	s := registry.ByTypeName("AWS::IAM::SAMLProvider").Schema()
	desired := map[string]any{"Name": "x", "SamlMetadataDocument": "m", "AddPrivateKey": "secret"}
	observed := map[string]any{"Name": "x", "SamlMetadataDocument": "m", "Arn": "arn"}
	ops, err := Patch(s, desired, observed, true)
	if err != nil {
		t.Fatal(err)
	}
	// AddPrivateKey is write-only and create-only: never patched.
	if len(ops) != 0 {
		t.Errorf("ops %s", js(ops))
	}
}

func TestToken(t *testing.T) {
	ty := registry.ByTypeName("AWS::IAM::SAMLProvider")
	r := (&descriptor{t: ty}).ResourceFromRuntimeObject((&descriptor{t: ty}).EmptyRuntimeObject()).(*resource)
	r.u.SetUID("u1")
	a, b := token(r, "create", []byte("x")), token(r, "create", []byte("x"))
	if a != b || len(a) != 64 {
		t.Errorf("tokens %q %q", a, b)
	}
	r.setRequest(request{Token: "t1", Status: "FAILED"})
	if token(r, "create", []byte("x")) == a {
		t.Errorf("a retry after a failure must get a new token")
	}
	// The same patch as an earlier update that succeeded (a drift put
	// back) is a new request, not a replay of the old one.
	r.setRequest(request{Token: "t2", Operation: "UPDATE", Status: "SUCCESS"})
	u1 := token(r, "update", []byte("p"))
	r.setRequest(request{Token: "t3", Operation: "UPDATE", Status: "SUCCESS"})
	if u2 := token(r, "update", []byte("p")); u2 == u1 {
		t.Errorf("the same patch after another successful update got the old token")
	}
	// A request retried while its status still holds the one before is
	// the same request.
	if token(r, "update", []byte("p")) != token(r, "update", []byte("p")) {
		t.Errorf("a retry of the same request got a new token")
	}
}
