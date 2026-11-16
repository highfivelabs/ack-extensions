// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package ackiam

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestRecommended(t *testing.T) {
	root := "../.."
	rds, ok, err := Recommended(root, "rds")
	if err != nil || !ok || !slices.Equal(rds.ManagedPolicyARNs, []string{"arn:aws:iam::aws:policy/AmazonRDSFullAccess"}) || rds.InlinePolicy != nil {
		t.Fatalf("rds: %+v ok=%v err=%v", rds, ok, err)
	}
	iam, ok, err := Recommended(root, "iam")
	if err != nil || !ok || iam.InlinePolicy == nil {
		t.Fatalf("iam: %+v ok=%v err=%v", iam, ok, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(iam.InlinePolicy, &doc); err != nil || doc["Statement"] == nil {
		t.Fatalf("iam's inline policy is not a policy document: %v", err)
	}
	if _, ok, err := Recommended(root, "scheduler"); ok || err != nil {
		t.Fatalf("scheduler (no ACK controller): ok=%v err=%v", ok, err)
	}
	if _, ok, _ := Recommended(root, "organizations"); ok {
		t.Fatal("organizations: never per environment, so no recommendation here")
	}
}

func TestIsACKKind(t *testing.T) {
	res, err := ACKResources("../..", "rds")
	if err != nil {
		t.Fatal(err)
	}
	for kind, want := range map[string]bool{"DBInstance": true, "DBParameterGroup": true, "GlobalCluster": true, "OptionGroup": false, "DBProxyTarget": false} {
		if got := IsACKKind(res, kind); got != want {
			t.Errorf("rds %s: %v, want %v (resources %v)", kind, got, want, res)
		}
	}
	res, err = ACKResources("../..", "iam")
	if err != nil {
		t.Fatal(err)
	}
	if !IsACKKind(res, "Policy") || !IsACKKind(res, "Role") || IsACKKind(res, "AccessKey") {
		t.Errorf("iam: Policy, Role are ACK's, AccessKey ours (resources %v)", res)
	}
}
