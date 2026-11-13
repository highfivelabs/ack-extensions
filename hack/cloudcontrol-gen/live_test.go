// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/iamgen"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
	"sigs.k8s.io/yaml"
)

// The live-test role has no permissions for a never-deleted type (a
// landing zone): no live test may make one. Checked on the committed
// template: every action only such a type needs is absent.
func TestLiveRoleLeavesOutNeverDeleted(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "cloudcontrol", "config", "live", "sandbox-account.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := string(raw)
	others := map[string]bool{}
	var never []*registry.Type
	for _, ty := range registry.Types {
		if ty.NeverDelete {
			never = append(never, ty)
			continue
		}
		for _, a := range iamgen.ForType(ty.Schema()).Actions {
			others[a] = true
		}
	}
	if len(never) == 0 {
		t.Skip("no type is marked neverDelete")
	}
	for _, ty := range never {
		own := 0
		for _, a := range iamgen.ForType(ty.Schema()).Actions {
			if others[a] {
				continue
			}
			own++
			if strings.Contains(tmpl, "- "+a+"\n") {
				t.Errorf("the live-test role allows %s, which only %s needs", a, ty.TypeName)
			}
		}
		if own == 0 {
			t.Errorf("%s has no action of its own to check", ty.TypeName)
		}
	}
}

// The committed sandbox template grants the sweeper's and the tests' own
// actions: the first deployed role had neither (a one-statement result
// passed to iamgen.Merge was dropped), so the first live run's sweep was
// refused cloudformation:ListResources.
func TestLiveTemplateGrantsSweeperAndFixtures(t *testing.T) {
	root := filepath.Join("..", "..")
	var cfg config
	b, err := os.ReadFile(filepath.Join(root, "cloudcontrol/types.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(filepath.Join(root, "cloudcontrol/config/live/sandbox-account.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var tpl struct {
		Resources map[string]struct {
			Type       string `json:"Type"`
			Properties struct {
				PolicyDocument struct {
					Statement []struct {
						Action []string `json:"Action"`
					} `json:"Statement"`
				} `json:"PolicyDocument"`
			} `json:"Properties"`
		} `json:"Resources"`
	}
	if err := yaml.Unmarshal(b, &tpl); err != nil {
		t.Fatal(err)
	}
	granted := map[string]bool{}
	for _, r := range tpl.Resources {
		if r.Type != "AWS::IAM::ManagedPolicy" {
			continue
		}
		for _, st := range r.Properties.PolicyDocument.Statement {
			for _, a := range st.Action {
				granted[a] = true
			}
		}
	}
	for _, a := range append([]string{"cloudformation:ListResources"}, cfg.LiveTestActions...) {
		if !granted[a] {
			t.Errorf("the live-test role's policies do not grant %s", a)
		}
	}
}

// committedLiveRole reads the committed sandbox template: the actions its
// managed policies grant, how many managed policies it has, and how many
// the role attaches.
func committedLiveRole(t *testing.T) (granted map[string]bool, policies, attached int) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "cloudcontrol/config/live/sandbox-account.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var tpl struct {
		Resources map[string]struct {
			Type       string `json:"Type"`
			Properties struct {
				ManagedPolicyArns []any `json:"ManagedPolicyArns"`
				PolicyDocument    struct {
					Statement []struct {
						Action []string `json:"Action"`
					} `json:"Statement"`
				} `json:"PolicyDocument"`
			} `json:"Properties"`
		} `json:"Resources"`
	}
	if err := yaml.Unmarshal(b, &tpl); err != nil {
		t.Fatal(err)
	}
	granted = map[string]bool{}
	for _, r := range tpl.Resources {
		switch r.Type {
		case "AWS::IAM::ManagedPolicy":
			policies++
			for _, st := range r.Properties.PolicyDocument.Statement {
				for _, a := range st.Action {
					granted[a] = true
				}
			}
		case "AWS::IAM::Role":
			attached += len(r.Properties.ManagedPolicyArns)
		}
	}
	return granted, policies, attached
}

// The committed live-test role stays within IAM's default of 10 managed
// policies per role (plan question 21: the role holds the sampled kinds
// only, so the limit is kept, not raised).
func TestLiveRoleWithinManagedPolicyLimit(t *testing.T) {
	_, policies, attached := committedLiveRole(t)
	if policies == 0 {
		t.Fatal("the sandbox template has no managed policy")
	}
	if policies > 10 || attached > 10 {
		t.Errorf("the live-test role has %d managed policies (%d attached), over IAM's default of 10 per role", policies, attached)
	}
	if maxLivePolicies != 10 {
		t.Errorf("maxLivePolicies is %d; IAM's default per role is 10", maxLivePolicies)
	}
}

// The role holds every live kind's actions, and is built from the live
// kinds only: some served kind outside them has an action it lacks.
func TestLiveRoleHoldsTheLiveKindsOnly(t *testing.T) {
	granted, _, _ := committedLiveRole(t)
	live, other := 0, 0
	for _, ty := range registry.Types {
		acts := iamgen.ForType(ty.Schema()).Actions
		if ty.Live {
			live++
			for _, a := range acts {
				if !granted[a] {
					t.Errorf("the live-test role lacks %s, which live kind %s needs", a, ty.TypeName)
				}
			}
			continue
		}
		for _, a := range acts {
			if !granted[a] {
				other++
				break
			}
		}
	}
	if live == 0 {
		t.Error("no served type is Live (liveKinds in cloudcontrol/types.yaml)")
	}
	if other == 0 {
		t.Error("the live-test role grants every served kind's actions; it should hold the live kinds' only")
	}
}

// liveKinds refuses a list naming an unknown or never-deleted type, a
// type twice, or nothing.
func TestLiveKindsChecked(t *testing.T) {
	for _, tc := range []struct {
		name  string
		types map[string]bool // type name: neverDelete
		live  []string
	}{
		{"unknown", map[string]bool{"AWS::A::B": false}, []string{"AWS::A::C"}},
		{"never deleted", map[string]bool{"AWS::A::B": true}, []string{"AWS::A::B"}},
		{"twice", map[string]bool{"AWS::A::B": false}, []string{"AWS::A::B", "AWS::A::B"}},
		{"empty", map[string]bool{"AWS::A::B": false}, nil},
	} {
		var c config
		for n, nd := range tc.types {
			c.Types = append(c.Types, typeEntry{TypeName: n, NeverDelete: nd})
		}
		c.LiveKinds = tc.live
		if _, err := liveKinds(c); err == nil {
			t.Errorf("%s: liveKinds accepted %v", tc.name, tc.live)
		}
	}
}
