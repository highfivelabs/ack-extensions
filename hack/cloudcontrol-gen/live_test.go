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
