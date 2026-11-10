// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package crdgen_test

import (
	"strings"
	"testing"

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
