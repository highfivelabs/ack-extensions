// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"
)

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
