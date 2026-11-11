// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package iamgen_test

import (
	"encoding/json"
	"fmt"
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

// An environment's policy is split to fit IAM's managed policy limit,
// keeps every statement once, and refuses a statement that cannot fit.
func TestEnvironmentSplit(t *testing.T) {
	// The spike's three types fit one document, named so the check does not
	// change as types.yaml grows.
	var results []iamgen.Result
	for _, n := range []string{"AWS::IAM::SAMLProvider", "AWS::EC2::TransitGatewayRouteTable", "AWS::GuardDuty::Detector"} {
		results = append(results, iamgen.ForType(registry.ByTypeName(n).Schema()))
	}
	whole, err := iamgen.Environment(results, iamgen.MaxManagedPolicySize)
	if err != nil || len(whole) != 1 {
		t.Fatalf("three types in one document: %d documents, %v", len(whole), err)
	}
	want := len(whole[0].Statement)
	small, err := iamgen.Environment(results, 900)
	if err != nil {
		t.Fatal(err)
	}
	if len(small) < 2 {
		t.Fatalf("a 900-character limit gave %d documents", len(small))
	}
	got := 0
	for _, p := range small {
		b, _ := json.Marshal(p)
		if len(b) > 900 {
			t.Errorf("document of %d characters", len(b))
		}
		got += len(p.Statement)
	}
	if got != want {
		t.Errorf("%d statements after the split, %d before", got, want)
	}
	if _, err := iamgen.Environment(results, 100); err == nil {
		t.Errorf("a statement over the limit was accepted")
	}

	// Every served type: as many documents as it takes, each within IAM's
	// limit, every statement kept once.
	var every []iamgen.Result
	for _, ty := range registry.Types {
		every = append(every, iamgen.ForType(ty.Schema()))
	}
	one, err := iamgen.Environment(every, 1<<20)
	if err != nil || len(one) != 1 {
		t.Fatalf("every type without a limit: %d documents, %v", len(one), err)
	}
	split, err := iamgen.Environment(every, iamgen.MaxManagedPolicySize)
	if err != nil {
		t.Fatal(err)
	}
	got = 0
	for _, p := range split {
		b, _ := json.Marshal(p)
		if len(b) > iamgen.MaxManagedPolicySize {
			t.Errorf("document of %d characters, over IAM's %d", len(b), iamgen.MaxManagedPolicySize)
		}
		got += len(p.Statement)
	}
	if got != len(one[0].Statement) {
		t.Errorf("every type: %d statements after the split, %d before", got, len(one[0].Statement))
	}
}

func TestTrustPolicy(t *testing.T) {
	b, _ := json.Marshal(iamgen.TrustPolicy(iamgen.Trust{AccountID: "111122223333", ClusterARN: "arn:aws:eks:us-west-2:111122223333:cluster/platform", Environment: "team-a-prod"}))
	for _, want := range []string{`"Service":"pods.eks.amazonaws.com"`, `"sts:TagSession"`, `"aws:RequestTag/kubernetes-namespace":"ack-extensions-system"`, `"aws:RequestTag/kubernetes-service-account":"ack-extensions-cloudcontrol-team-a-prod"`, `"aws:SourceArn":"arn:aws:eks:us-west-2:111122223333:cluster/platform"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("trust policy lacks %s: %s", want, b)
		}
	}
}

// A type that is never deleted gets no delete permissions: neither Cloud
// Control's DeleteResource nor its delete handler's actions. An
// environment keeps DeleteResource while one of its types may be deleted.
func TestNeverDeletePolicy(t *testing.T) {
	s := registry.ByTypeName("AWS::IAM::SAMLProvider").Schema()
	never := iamgen.ForTypeWith(s, iamgen.Options{NeverDelete: true})
	all := strings.Join(never.Actions, ",")
	for _, gone := range []string{"cloudformation:DeleteResource", "iam:DeleteSAMLProvider"} {
		if strings.Contains(all, gone) {
			t.Errorf("a never-deleted type's policy has %s: %v", gone, never.Actions)
		}
	}
	for _, st := range never.Policy.Statement {
		if strings.HasSuffix(st.Sid, "Delete") {
			t.Errorf("a never-deleted type's policy has a delete statement: %+v", st)
		}
	}
	if !strings.Contains(all, "iam:CreateSAMLProvider") || !strings.Contains(all, "cloudformation:GetResource") {
		t.Errorf("a never-deleted type's policy lost its other actions: %v", never.Actions)
	}
	has := func(p iamgen.Policy) bool {
		for _, a := range p.Statement[0].Action {
			if a == "cloudformation:DeleteResource" {
				return true
			}
		}
		return false
	}
	if has(iamgen.Merge([]iamgen.Result{never})) {
		t.Error("an environment of never-deleted types allows cloudformation:DeleteResource")
	}
	if !has(iamgen.Merge([]iamgen.Result{never, iamgen.ForType(s)})) {
		t.Error("an environment with a deletable type lacks cloudformation:DeleteResource")
	}
}

// Compact keeps exactly the allowed actions (all on "*") and makes the
// document smaller.
func TestCompact(t *testing.T) {
	var results []iamgen.Result
	for _, ty := range registry.Types {
		results = append(results, iamgen.ForType(ty.Schema()))
	}
	merged := iamgen.Merge(results)
	merged.Statement = append(merged.Statement, iamgen.Statement{Sid: "Other", Effect: "Allow", Action: []string{"s3:GetObject"}, Resource: "arn:aws:s3:::b/*"})
	compact := iamgen.Compact(merged)
	set := func(p iamgen.Policy) map[string]bool {
		out := map[string]bool{}
		for _, st := range p.Statement {
			for _, a := range st.Action {
				out[st.Effect+" "+a+" "+st.Resource] = true
			}
		}
		return out
	}
	before, after := set(merged), set(compact)
	if len(before) != len(after) {
		t.Errorf("%d allowed (action, resource) pairs before, %d after", len(before), len(after))
	}
	for k := range before {
		if !after[k] {
			t.Errorf("Compact dropped %s", k)
		}
	}
	size := func(p iamgen.Policy) int { b, _ := json.Marshal(p); return len(b) }
	if size(compact) >= size(merged) {
		t.Errorf("Compact made %d characters of %d", size(compact), size(merged))
	}
	t.Logf("%d types: %d characters merged, %d compact", len(registry.Types), size(merged), size(compact))

	// A service with more actions than one policy holds is cut.
	var many []string
	for i := 0; i < 400; i++ {
		many = append(many, fmt.Sprintf("ec2:DescribeSomethingLong%03d", i))
	}
	big := iamgen.Compact(iamgen.Policy{Version: "2012-10-17", Statement: []iamgen.Statement{{Sid: "A", Effect: "Allow", Action: many, Resource: "*"}}})
	if len(big.Statement) < 2 {
		t.Fatalf("400 actions in %d statement(s)", len(big.Statement))
	}
	n := 0
	for _, st := range big.Statement {
		n += len(st.Action)
		if b, _ := json.Marshal(iamgen.Policy{Version: "2012-10-17", Statement: []iamgen.Statement{st}}); len(b) > iamgen.MaxManagedPolicySize {
			t.Errorf("statement %s is %d characters", st.Sid, len(b))
		}
	}
	if n != 400 {
		t.Errorf("%d actions after the cut, want 400", n)
	}
}
