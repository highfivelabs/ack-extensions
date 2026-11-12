// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package ackiam

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/iamgen"
)

const root = "../.."

func TestScan(t *testing.T) {
	kinds, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	byRef := map[string]Kind{}
	for _, k := range kinds {
		byRef[k.Ref()] = k
	}
	// Every kind with a CRD has its permissions.
	crds, err := filepath.Glob(filepath.Join(root, "config/crds/bases/*.aws.hfl.dev_*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	reKindLine := regexp.MustCompile(`(?m)^    kind: ([A-Za-z0-9]+)$`)
	for _, f := range crds {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		svc := strings.SplitN(filepath.Base(f), ".", 2)[0]
		m := reKindLine.FindSubmatch(b)
		if m == nil {
			t.Fatalf("%s: no kind", f)
		}
		if _, ok := byRef[string(m[1])+"."+svc]; !ok {
			t.Errorf("%s.%s has a CRD and no permissions", m[1], svc)
		}
	}
	for ref, want := range map[string][]string{
		// generated code: create, read, update, delete
		"OptionGroup.rds": {"rds:CreateOptionGroup", "rds:DeleteOptionGroup", "rds:DescribeOptionGroups", "rds:ModifyOptionGroup"},
		// a paginator
		"DBProxyTarget.rds": {"rds:DescribeDBProxyTargets", "rds:RegisterDBProxyTargets"},
		// ec2's tags package, imported by the kind
		"VPNGateway.ec2": {"ec2:CreateTags", "ec2:DeleteTags", "ec2:CreateVpnGateway"},
		// a client for another region, in a hook
		"ReplicaKey.kms": {"kms:ReplicateKey", "kms:PutKeyPolicy", "kms:ScheduleKeyDeletion", "kms:CreateKey"},
		// an IAM namespace that is not the service's name
		"APIDestination.eventbridge":             {"events:CreateApiDestination"},
		"ScheduledAction.applicationautoscaling": {"application-autoscaling:PutScheduledAction"},
		// actions IAM names otherwise than the operation (and the delete
		// authorized as the put)
		"PublicAccessBlock.s3control": {"s3:GetAccountPublicAccessBlock", "s3:PutAccountPublicAccessBlock"},
		// curated
		"DBRoleAssociation.rds": {"rds:AddRoleToDBInstance", "iam:PassRole"},
	} {
		k, ok := byRef[ref]
		if !ok {
			t.Errorf("%s: not found", ref)
			continue
		}
		for _, a := range want {
			if !slices.Contains(k.Actions(), a) {
				t.Errorf("%s lacks %s: %v", ref, a, k.Actions())
			}
		}
	}
	for _, k := range kinds {
		for _, a := range k.Generated {
			if !strings.HasPrefix(a, Prefix(k.Service)+":") {
				t.Errorf("%s: %s is not in %s's namespace", k.Ref(), a, k.Service)
			}
		}
	}
	// Curated names only kinds that exist.
	for ref := range Curated {
		if _, ok := byRef[ref]; !ok {
			t.Errorf("Curated names %s, which is not a kind", ref)
		}
	}
	if got := byRef["DBRoleAssociation.rds"].Sensitive(); !slices.Contains(got, "iam:PassRole") {
		t.Errorf("iam:PassRole not reported sensitive: %v", got)
	}
	// Every kind together fits IAM's limits as managed policies.
	docs, err := iamgen.Split(Policy(kinds), iamgen.MaxManagedPolicySize)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) > 10 {
		t.Errorf("every kind needs %d managed policies, more than a role's 10", len(docs))
	}
}
