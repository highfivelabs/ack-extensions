// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package iamgen_test

import (
	"strings"
	"testing"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/iamgen"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
)

func TestPolicies(t *testing.T) {
	saml := iamgen.ForType(registry.ByTypeName("AWS::IAM::SAMLProvider").Schema())
	all := strings.Join(saml.Actions, ",")
	for _, want := range []string{"cloudformation:CreateResource", "iam:CreateSAMLProvider", "iam:TagSAMLProvider", "iam:DeleteSAMLProvider"} {
		if !strings.Contains(all, want) {
			t.Errorf("SAMLProvider policy lacks %s: %v", want, saml.Actions)
		}
	}
	if strings.Contains(all, "iam:ListSAMLProviders") || strings.Contains(all, "cloudformation:ListResources") {
		t.Errorf("list-only actions must be left out: %v", saml.Actions)
	}
	if len(saml.ListOnly) != 1 || saml.ListOnly[0] != "iam:ListSAMLProviders" {
		t.Errorf("list-only %v", saml.ListOnly)
	}
	gd := iamgen.ForType(registry.ByTypeName("AWS::GuardDuty::Detector").Schema())
	if len(gd.Sensitive) != 1 || gd.Sensitive[0] != "iam:CreateServiceLinkedRole" {
		t.Errorf("sensitive %v", gd.Sensitive)
	}
	for _, s := range gd.Policy.Statement {
		if s.Resource != "*" || s.Effect != "Allow" || len(s.Action) == 0 {
			t.Errorf("statement %+v", s)
		}
	}
}
