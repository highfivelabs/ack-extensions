// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// The ledger reads docs/live-runs.md's Kinds table, and generator.yaml's
// reference fields are found by their flattened names.
func TestUpjetLedgerInputs(t *testing.T) {
	root := filepath.Join("..", "..")
	live := readLiveKinds(root)
	if r, ok := live["iam/SAMLProvider"]; !ok || r.route != "Cloud Control" || !strings.HasPrefix(r.live, "PASS") {
		t.Errorf("iam/SAMLProvider: %+v %v", r, ok)
	}
	refs := ackGenRefs(root)
	if !refs["efs/MountTarget"][flat("SubnetId")] {
		t.Errorf("efs/MountTarget's SubnetId reference not found: %v", refs["efs/MountTarget"])
	}
	if flat("launch_template.launch_template_id") != "launchtemplatelaunchtemplateid" {
		t.Errorf("flat: %s", flat("launch_template.launch_template_id"))
	}
}
