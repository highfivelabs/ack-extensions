// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package crdgen_test

import (
	"strings"
	"testing"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/cfn"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/crdcheck"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/crdgen"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
)

func TestGeneratedCRDs(t *testing.T) {
	for _, ty := range registry.Types {
		crd, rep, err := crdgen.Generate(ty.Schema(), crdgen.Options{Group: ty.Group, Version: ty.Version, Kind: ty.Kind, Secrets: ty.Secrets})
		if err != nil {
			t.Fatalf("%s: %v", ty.TypeName, err)
		}
		if err := crdcheck.Validate(crd); err != nil {
			t.Errorf("%s: the API server would refuse the CRD: %v", ty.TypeName, err)
		}
		root := crd.Spec.Versions[0].Schema.OpenAPIV3Schema
		spec, status := root.Properties["spec"], root.Properties["status"]
		for _, f := range []string{"ackResourceMetadata", "conditions", "cloudControl"} {
			if _, ok := status.Properties[f]; !ok {
				t.Errorf("%s: status.%s missing", ty.TypeName, f)
			}
		}
		switch ty.TypeName {
		case "AWS::IAM::SAMLProvider":
			if _, ok := spec.Properties["arn"]; ok {
				t.Errorf("read-only arn in spec")
			}
			if _, ok := status.Properties["arn"]; !ok {
				t.Errorf("read-only arn not in status")
			}
			ref := spec.Properties["addPrivateKey"]
			if ref.Type != "object" || ref.Properties["key"].Type != "string" {
				t.Errorf("write-only addPrivateKey is not a Secret reference: %+v", ref)
			}
			name := spec.Properties["name"]
			if len(name.XValidations) != 1 || name.XValidations[0].Rule != "self == oldSelf" {
				t.Errorf("create-only name has no CEL rule: %+v", name.XValidations)
			}
			var presence bool
			for _, v := range spec.XValidations {
				if strings.Contains(v.Rule, "has(self.name) == has(oldSelf.name)") {
					presence = true
				}
			}
			if !presence {
				t.Errorf("no presence rule for name on spec: %+v", spec.XValidations)
			}
			// Opted in: the private key. Not opted in: the key ID to
			// remove, write-only but no secret, a plain string.
			if len(rep.SecretRefs) != 1 || rep.SecretRefs[0] != "/properties/AddPrivateKey" {
				t.Errorf("secret refs %v", rep.SecretRefs)
			}
			if rm := spec.Properties["removePrivateKey"]; rm.Type != "string" {
				t.Errorf("write-only removePrivateKey not opted in, yet not a plain string: %+v", rm)
			}
		case "AWS::EC2::TransitGatewayRouteTable":
			if len(spec.Required) != 1 || spec.Required[0] != "transitGatewayId" {
				t.Errorf("required %v", spec.Required)
			}
			if _, ok := status.Properties["transitGatewayRouteTableId"]; !ok {
				t.Errorf("primary identifier not in status")
			}
		case "AWS::GuardDuty::Detector":
			ds := spec.Properties["dataSources"].Properties["s3Logs"].Properties["enable"]
			if ds.Type != "boolean" {
				t.Errorf("nested dataSources.s3Logs.enable: %+v", ds)
			}
			if spec.Properties["features"].Items.Schema.Properties["additionalConfiguration"].Type != "array" {
				t.Errorf("features[].additionalConfiguration not an array")
			}
		}
	}
}

// A never-deleted type's CRD says so in an annotation; others carry none.
func TestNeverDeleteAnnotation(t *testing.T) {
	ty := registry.ByTypeName("AWS::GuardDuty::Detector")
	for _, never := range []bool{false, true} {
		crd, _, err := crdgen.Generate(ty.Schema(), crdgen.Options{Group: ty.Group, Version: ty.Version, Kind: ty.Kind, NeverDelete: never})
		if err != nil {
			t.Fatal(err)
		}
		v, ok := crd.Annotations[crdgen.AnnotationNeverDelete]
		if ok != never || (never && v != "true") {
			t.Errorf("neverDelete=%v: annotation %q (present %v)", never, v, ok)
		}
	}
}

// The overlay's references: a reference field beside each ID property,
// the property no longer required on its own, CEL asking for one of the
// two and refusing both, and a create-only property's reference
// create-only too.
func TestReferenceFields(t *testing.T) {
	ty := registry.ByTypeName("AWS::EC2::SecurityGroupVpcAssociation")
	refs, err := ty.Schema().References([]cfn.Reference{
		{Property: "/properties/GroupId", Group: "ec2.services.k8s.aws", Kind: "SecurityGroup", Path: "status.id"},
		{Property: "/properties/VpcId", Group: "ec2.services.k8s.aws", Kind: "VPC", Path: "status.vpcID"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if refs[0].RefField != "groupRef" || refs[1].RefField != "vpcRef" || refs[1].Resource != "vpcs" || refs[0].Version != "v1alpha1" {
		t.Fatalf("defaults: %+v", refs)
	}
	crd, rep, err := crdgen.Generate(ty.Schema(), crdgen.Options{Group: ty.Group, Version: ty.Version, Kind: ty.Kind, References: refs})
	if err != nil {
		t.Fatal(err)
	}
	if err := crdcheck.Validate(crd); err != nil {
		t.Fatalf("the API server would refuse the CRD: %v", err)
	}
	spec := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
	if len(spec.Required) != 0 {
		t.Errorf("required %v: the IDs may come from references", spec.Required)
	}
	g := spec.Properties["groupRef"]
	if g.Properties["from"].Properties["name"].Type != "string" {
		t.Errorf("groupRef is not ACK's reference wrapper: %+v", g)
	}
	if len(g.XValidations) != 1 || g.XValidations[0].Rule != "self == oldSelf" {
		t.Errorf("groupRef of a create-only property is not create-only: %+v", g.XValidations)
	}
	want := map[string]bool{
		"has(self.groupId) || has(self.groupRef)":     false,
		"!(has(self.groupId) && has(self.groupRef))":  false,
		"has(self.groupRef) == has(oldSelf.groupRef)": false,
		"has(self.vpcId) || has(self.vpcRef)":         false,
		"!(has(self.vpcId) && has(self.vpcRef))":      false,
	}
	for _, v := range spec.XValidations {
		if _, ok := want[v.Rule]; ok {
			want[v.Rule] = true
		}
	}
	for r, ok := range want {
		if !ok {
			t.Errorf("no rule %q", r)
		}
	}
	if len(rep.References) != 2 {
		t.Errorf("report references %v", rep.References)
	}
}

// What the overlay refuses: a property that is not a writable top-level
// string, a reference listed twice, a missing target, a field clash.
func TestReferenceRefusals(t *testing.T) {
	s := registry.ByTypeName("AWS::EC2::SecurityGroupVpcAssociation").Schema()
	ok := cfn.Reference{Group: "g", Kind: "K", Path: "status.x"}
	with := func(p string) cfn.Reference { r := ok; r.Property = p; return r }
	for name, refs := range map[string][]cfn.Reference{
		"unknown":   {with("/properties/Nope")},
		"read-only": {with("/properties/State")},
		"nested":    {with("/properties/GroupId/Inner")},
		"twice":     {with("/properties/GroupId"), with("/properties/GroupId")},
		"no target": {{Property: "/properties/GroupId"}},
		"clash":     {func() cfn.Reference { r := with("/properties/GroupId"); r.RefField = "vpcId"; return r }()},
	} {
		if _, err := s.References(refs); err == nil {
			t.Errorf("%s: accepted %+v", name, refs)
		}
	}
}
