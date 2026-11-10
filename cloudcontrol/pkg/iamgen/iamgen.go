// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package iamgen writes the least-privilege IAM policy a Cloud Control
// manager needs for a resource type, from the schema's handlers.
//
// Cloud Control calls the type's handlers with the caller's own credentials
// (no RoleArn is passed), so the manager's role needs two things:
//
//  1. the Cloud Control actions the reconciler calls, which IAM names in
//     the cloudformation: namespace: CreateResource, GetResource,
//     UpdateResource, DeleteResource and GetResourceRequestStatus (never
//     ListResources: the reconciler reads by identifier);
//  2. each handler's own permissions, for the handlers those actions run:
//     create, read, update and delete. The list handler's are left out for
//     the same reason.
//
// Statements are split per handler so a reviewer sees why each action is
// there. Resources are "*": the schemas name actions, not resource ARNs;
// scoping by ARN is a reviewed step after generation. Actions that grant
// more than the resource (iam:PassRole, iam:CreateServiceLinkedRole, kms:*
// on keys the type uses) are listed as sensitive.
package iamgen

import (
	"sort"
	"strings"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/cfn"
)

// Handlers the reconciler causes Cloud Control to run.
var Handlers = []string{"create", "read", "update", "delete"}

// CloudControlActions are the Cloud Control API actions the reconciler
// calls. ListResources is not one of them.
var CloudControlActions = []string{
	"cloudformation:CreateResource",
	"cloudformation:DeleteResource",
	"cloudformation:GetResource",
	"cloudformation:GetResourceRequestStatus",
	"cloudformation:UpdateResource",
}

// Statement is one IAM policy statement.
type Statement struct {
	Sid      string   `json:"Sid"`
	Effect   string   `json:"Effect"`
	Action   []string `json:"Action"`
	Resource string   `json:"Resource"`
}

// Policy is an IAM policy document.
type Policy struct {
	Version   string      `json:"Version"`
	Statement []Statement `json:"Statement"`
}

// Result is a type's policy and what a reviewer should look at.
type Result struct {
	Policy Policy
	// Actions is every action in the policy, sorted.
	Actions []string
	// Sensitive are actions that reach beyond the resource itself.
	Sensitive []string
	// ListOnly are actions only the list handler needs, left out.
	ListOnly []string
}

var sensitivePrefixes = []string{"iam:PassRole", "iam:CreateServiceLinkedRole", "iam:Put", "iam:Attach", "kms:CreateGrant", "kms:Decrypt", "sts:AssumeRole"}

func sid(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ForType is the policy for one resource type.
func ForType(s *cfn.Schema) Result {
	res := Result{Policy: Policy{Version: "2012-10-17"}}
	all := map[string]bool{}
	res.Policy.Statement = append(res.Policy.Statement, Statement{
		Sid: "CloudControl" + sid(s.Service()+s.Resource()), Effect: "Allow",
		Action: append([]string(nil), CloudControlActions...), Resource: "*",
	})
	for _, a := range CloudControlActions {
		all[a] = true
	}
	used := map[string]bool{}
	for _, h := range Handlers {
		hd := s.Handlers[h]
		if hd == nil || len(hd.Permissions) == 0 {
			continue
		}
		acts := dedupe(hd.Permissions)
		for _, a := range acts {
			all[a] = true
			used[a] = true
		}
		res.Policy.Statement = append(res.Policy.Statement, Statement{
			Sid:    sid(s.Service()+s.Resource()) + strings.ToUpper(h[:1]) + h[1:],
			Effect: "Allow", Action: acts, Resource: "*",
		})
	}
	if l := s.Handlers["list"]; l != nil {
		for _, a := range dedupe(l.Permissions) {
			if !used[a] {
				res.ListOnly = append(res.ListOnly, a)
			}
		}
	}
	for a := range all {
		res.Actions = append(res.Actions, a)
		for _, p := range sensitivePrefixes {
			if strings.HasPrefix(a, p) {
				res.Sensitive = append(res.Sensitive, a)
				break
			}
		}
	}
	sort.Strings(res.Actions)
	sort.Strings(res.Sensitive)
	return res
}

// Merge is one policy for a manager serving several types: one statement
// per type and handler, kept apart so each action's reason stays visible.
func Merge(results []Result) Policy {
	out := Policy{Version: "2012-10-17"}
	out.Statement = append(out.Statement, Statement{
		Sid: "CloudControl", Effect: "Allow", Action: append([]string(nil), CloudControlActions...), Resource: "*",
	})
	for _, r := range results {
		out.Statement = append(out.Statement, r.Policy.Statement[1:]...)
	}
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range in {
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}
