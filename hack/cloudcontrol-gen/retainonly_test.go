// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	"sigs.k8s.io/yaml"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
)

// Every never-deleted type is in the retain-only admission policy
// (config/admission/retain-only.yaml), so an object of it must carry
// deletion-policy: retain and a delete says what it does.
func TestNeverDeletedAreRetainOnly(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "admission", "retain-only.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var policy admissionv1.ValidatingAdmissionPolicy
	if err := yaml.Unmarshal(bytes.Split(raw, []byte("\n---\n"))[0], &policy); err != nil {
		t.Fatal(err)
	}
	covered := map[string]bool{}
	for _, r := range policy.Spec.MatchConstraints.ResourceRules {
		for _, g := range r.APIGroups {
			for _, res := range r.Resources {
				covered[res+"."+g] = true
			}
		}
	}
	for _, ty := range registry.Types {
		if ty.NeverDelete && !covered[ty.Plural+"."+ty.Group] {
			t.Errorf("%s (%s.%s) is never deleted but not in retain-only.yaml", ty.TypeName, ty.Plural, ty.Group)
		}
	}
}
