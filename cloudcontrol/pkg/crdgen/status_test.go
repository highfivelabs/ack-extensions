// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package crdgen_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/crdgen"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
)

// valueChecks lists the keywords that refuse a value rather than describe
// its shape. Status is written by the manager from what AWS returns, so a
// value check there can only refuse a status update (docs/plan.md, open
// question 20); spec keeps them.
func valueChecks(s *apiextv1.JSONSchemaProps, path string, out *[]string) {
	if s == nil {
		return
	}
	add := func(ok bool, k string) {
		if ok {
			*out = append(*out, path+": "+k)
		}
	}
	add(len(s.Enum) > 0, "enum")
	add(s.MinLength != nil, "minLength")
	add(s.MaxLength != nil, "maxLength")
	add(s.Pattern != "", "pattern")
	add(s.Minimum != nil, "minimum")
	add(s.Maximum != nil, "maximum")
	add(s.ExclusiveMinimum, "exclusiveMinimum")
	add(s.ExclusiveMaximum, "exclusiveMaximum")
	add(s.MultipleOf != nil, "multipleOf")
	add(s.MinItems != nil, "minItems")
	add(s.MaxItems != nil, "maxItems")
	add(s.MinProperties != nil, "minProperties")
	add(s.MaxProperties != nil, "maxProperties")
	names := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		p := s.Properties[k]
		valueChecks(&p, path+"."+k, out)
	}
	if s.Items != nil {
		valueChecks(s.Items.Schema, path+"[]", out)
		for i := range s.Items.JSONSchemas {
			valueChecks(&s.Items.JSONSchemas[i], path+"[]", out)
		}
	}
	if s.AdditionalProperties != nil {
		valueChecks(s.AdditionalProperties.Schema, path+"{}", out)
	}
}

func statusValueChecks(t *testing.T, crd *apiextv1.CustomResourceDefinition) []string {
	t.Helper()
	var out []string
	for _, v := range crd.Spec.Versions {
		if v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
			continue
		}
		st, ok := v.Schema.OpenAPIV3Schema.Properties["status"]
		if !ok {
			continue
		}
		valueChecks(&st, v.Name+": status", &out)
	}
	return out
}

// No committed CRD carries a value check under status: the Cloud Control
// kinds (cloudcontrol/config/crds) and the ACK-generated kinds
// (config/crds/bases).
func TestNoValueChecksInStatus(t *testing.T) {
	var files []string
	for _, dir := range []string{"../../config/crds", "../../../config/crds/bases"} {
		m, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if len(m) == 0 {
			t.Fatalf("no CRDs in %s", dir)
		}
		files = append(files, m...)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var crd apiextv1.CustomResourceDefinition
		if err := yaml.Unmarshal(b, &crd); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if crd.Kind != "CustomResourceDefinition" {
			continue
		}
		for _, c := range statusValueChecks(t, &crd) {
			t.Errorf("%s: %s", filepath.Base(f), c)
		}
	}
}

// Generate drops value checks from status and keeps types there, while
// spec keeps its value checks. Security Hub's DelegatedAdmin has an enum on
// its read-only Status (ENABLED, DISABLE_IN_PROGRESS): status.status stays
// a string with no enum.
func TestGenerateDropsValueChecksFromStatusOnly(t *testing.T) {
	specChecks := 0
	for _, ty := range registry.Types {
		crd, _, err := crdgen.Generate(ty.Schema(), crdgen.Options{Group: ty.Group, Version: ty.Version, Kind: ty.Kind, Secrets: ty.Secrets})
		if err != nil {
			t.Fatalf("%s: %v", ty.TypeName, err)
		}
		if c := statusValueChecks(t, crd); len(c) > 0 {
			t.Errorf("%s: value checks under status: %v", ty.TypeName, c)
		}
		root := crd.Spec.Versions[0].Schema.OpenAPIV3Schema
		sp := root.Properties["spec"]
		var c []string
		valueChecks(&sp, "spec", &c)
		specChecks += len(c)
		if ty.TypeName == "AWS::SecurityHub::DelegatedAdmin" {
			if st := root.Properties["status"].Properties["status"]; st.Type != "string" {
				t.Errorf("DelegatedAdmin status.status lost its type: %+v", st)
			}
		}
	}
	if specChecks == 0 {
		t.Errorf("no value checks left on any spec")
	}
}
