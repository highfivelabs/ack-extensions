// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package cfn_test

import (
	"testing"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/cfn"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
)

func TestFieldName(t *testing.T) {
	for in, want := range map[string]string{
		"SAMLProvider": "samlProvider", "SamlMetadataDocument": "samlMetadataDocument",
		"S3Logs": "s3Logs", "Arn": "arn", "ARN": "arn", "Id": "id", "KMSKeyId": "kmsKeyId",
		"EC2InstanceId": "ec2InstanceId", "TransitGatewayId": "transitGatewayId", "Tags": "tags",
		"IPv6CidrBlock": "iPv6CidrBlock", // known ugliness, see docs/cloudcontrol.md
	} {
		if got := cfn.FieldName(in); got != want {
			t.Errorf("FieldName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProvisioningAndSets(t *testing.T) {
	s := registry.ByTypeName("AWS::IAM::SAMLProvider").Schema()
	if p := s.Provisioning(); p != "FULLY_MUTABLE" {
		t.Errorf("provisioning %s", p)
	}
	sets := s.Sets()
	if !sets.ReadOnly.Has("/properties/Arn") || !sets.CreateOnly.Has("/properties/Name") || !sets.WriteOnly.Has("/properties/AddPrivateKey") {
		t.Errorf("sets: %+v", sets)
	}
	if got := s.PrimaryIdentifierNames(); len(got) != 1 || got[0] != "Arn" {
		t.Errorf("primary identifier %v", got)
	}
	if s.TagProperty() != "Tags" {
		t.Errorf("tag property %q", s.TagProperty())
	}
	if got := s.SecretCandidates(); len(got) != 2 {
		t.Errorf("secret candidates %v", got)
	}
}

// Secret references are opt-in per field; anything but a write-only string
// reachable through objects is refused, so a typo cannot quietly make a
// secret a plain field.
func TestSecretPathsOptIn(t *testing.T) {
	s := registry.ByTypeName("AWS::IAM::SAMLProvider").Schema()
	got, err := s.SecretPaths([]string{"/properties/AddPrivateKey"})
	if err != nil || len(got) != 1 || got[0][0] != "AddPrivateKey" {
		t.Errorf("opt-in AddPrivateKey: %v, %v", got, err)
	}
	if got, err := s.SecretPaths(nil); err != nil || len(got) != 0 {
		t.Errorf("no opt-in: %v, %v", got, err)
	}
	for _, bad := range [][]string{
		{"/properties/Name"},         // not write-only
		{"/properties/AddPrivatKey"}, // a typo
		{"AddPrivateKey"},            // not a pointer
		{"/properties/AddPrivateKey", "/properties/AddPrivateKey"},
	} {
		if _, err := s.SecretPaths(bad); err == nil {
			t.Errorf("SecretPaths(%v) accepted", bad)
		}
	}
}

func TestPointerSetCovers(t *testing.T) {
	ps := cfn.NewPointerSet([]string{"/properties/A/properties/B", "/properties/C/items/D"})
	for ptr, want := range map[string]bool{
		"/properties/A/B": true, "/properties/A/B/X": true, "/properties/A": false,
		"/properties/C/*/D": true, "/properties/E": false,
	} {
		if got := ps.Covers(ptr); got != want {
			t.Errorf("Covers(%s) = %v", ptr, got)
		}
	}
}

func TestConvertRoundTrip(t *testing.T) {
	s := registry.ByTypeName("AWS::GuardDuty::Detector").Schema()
	k8s := map[string]any{
		"enable":      true,
		"dataSources": map[string]any{"s3Logs": map[string]any{"enable": true}, "kubernetes": map[string]any{"auditLogs": map[string]any{"enable": false}}},
		"features":    []any{map[string]any{"name": "X", "status": "ENABLED", "additionalConfiguration": []any{map[string]any{"name": "Y", "status": "ENABLED"}}}},
	}
	c := s.ToCFN(s.Root(), k8s).(map[string]any)
	ds := c["DataSources"].(map[string]any)
	if _, ok := ds["S3Logs"]; !ok {
		t.Fatalf("ToCFN: %v", c)
	}
	f := c["Features"].([]any)[0].(map[string]any)
	if _, ok := f["AdditionalConfiguration"]; !ok {
		t.Fatalf("ToCFN nested array: %v", f)
	}
	back := s.FromCFN(s.Root(), c)
	if !s.Satisfies(s.Root(), cfn.Normalise(k8s), cfn.Normalise(back)) {
		t.Errorf("round trip lost something: %v", back)
	}
}

func TestSatisfies(t *testing.T) {
	s := registry.ByTypeName("AWS::GuardDuty::Detector").Schema()
	root := s.Root()
	desired := map[string]any{
		"DataSources": map[string]any{"S3Logs": map[string]any{"Enable": true}},
		"Features": []any{
			map[string]any{"Name": "A", "Status": "ENABLED"},
			map[string]any{"Name": "B", "Status": "DISABLED"},
		},
	}
	observed := map[string]any{
		"Id":          "d1",
		"DataSources": map[string]any{"S3Logs": map[string]any{"Enable": true}, "Kubernetes": map[string]any{"AuditLogs": map[string]any{"Enable": false}}},
		"Features": []any{
			map[string]any{"Name": "B", "Status": "DISABLED"},
			map[string]any{"Name": "A", "Status": "ENABLED"},
		},
	}
	if !s.Satisfies(root, desired, observed) {
		t.Errorf("a server default and a reordered unordered list should satisfy")
	}
	observed["Features"] = []any{map[string]any{"Name": "A", "Status": "ENABLED"}}
	if s.Satisfies(root, desired, observed) {
		t.Errorf("a missing feature should not satisfy")
	}
	// Scalars returned as strings.
	p := &cfn.Prop{Type: cfn.TypeList{"integer"}}
	if !s.Satisfies(p, 5.0, "5") {
		t.Errorf("5 and \"5\" should be equal for an integer")
	}
	// A free-form document is compared whole.
	free := &cfn.Prop{Type: cfn.TypeList{"object"}}
	if s.Satisfies(free, map[string]any{"a": 1.0}, map[string]any{"a": 1.0, "b": 2.0}) {
		t.Errorf("free-form objects compare whole")
	}
}
