// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssoadmin"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	readOnly = "arn:aws:iam::aws:policy/ReadOnlyAccess"
	viewOnly = "arn:aws:iam::aws:policy/job-function/ViewOnlyAccess"
)

func managedPolicies(t *testing.T, e *env, inst, ps string) []string {
	t.Helper()
	out, err := ssoadmin.NewFromConfig(e.moto).ListManagedPoliciesInPermissionSet(e.ctx, &ssoadmin.ListManagedPoliciesInPermissionSetInput{InstanceArn: &inst, PermissionSetArn: &ps})
	if err != nil {
		t.Fatal(err)
	}
	var l []string
	for _, p := range out.AttachedManagedPolicies {
		l = append(l, aws.ToString(p.Arn))
	}
	sort.Strings(l)
	return l
}

// F02, IAM Identity Center: a permission set with a managed and an inline
// policy, a change to its policy list, and an assignment of it to a group
// in an account; then both deleted.
func TestIdentityCenter(t *testing.T) {
	e := start(t)
	sso := ssoadmin.NewFromConfig(e.moto)
	insts, err := sso.ListInstances(e.ctx, &ssoadmin.ListInstancesInput{})
	if err != nil || len(insts.Instances) == 0 {
		t.Fatalf("moto has no Identity Center instance: %v", err)
	}
	inst := aws.ToString(insts.Instances[0].InstanceArn)

	const psType = "AWS::SSO::PermissionSet"
	ps := obj("PermissionSet", "platform-readonly", map[string]any{
		"instanceArn":     inst,
		"name":            "PlatformReadOnly",
		"description":     "read-only access for the platform team",
		"sessionDuration": "PT4H",
		"managedPolicies": []any{readOnly},
		"inlinePolicy": map[string]any{
			"Version":   "2012-10-17",
			"Statement": []any{map[string]any{"Effect": "Deny", "Action": "s3:DeleteBucket", "Resource": "*"}},
		},
		"tags": []any{map[string]any{"key": "team", "value": "platform"}},
	})
	if err := e.c.Create(e.ctx, ps); err != nil {
		t.Fatal(err)
	}
	got := e.waitSynced(t, ps)
	arn, _, _ := unstructured.NestedString(got.Object, "status", "permissionSetArn")
	id, _, _ := unstructured.NestedString(got.Object, "status", "cloudControl", "identifier")
	if arn == "" || id != inst+"|"+arn {
		t.Fatalf("status.permissionSetArn %q, identifier %q", arn, id)
	}
	d, err := sso.DescribePermissionSet(e.ctx, &ssoadmin.DescribePermissionSetInput{InstanceArn: &inst, PermissionSetArn: &arn})
	if err != nil || aws.ToString(d.PermissionSet.Name) != "PlatformReadOnly" || aws.ToString(d.PermissionSet.SessionDuration) != "PT4H" {
		t.Fatalf("moto: %v %+v", err, d)
	}
	if l := managedPolicies(t, e, inst, arn); strings.Join(l, ",") != readOnly {
		t.Errorf("managed policies in moto: %v", l)
	}
	ip, err := sso.GetInlinePolicyForPermissionSet(e.ctx, &ssoadmin.GetInlinePolicyForPermissionSetInput{InstanceArn: &inst, PermissionSetArn: &arn})
	if err != nil || !strings.Contains(aws.ToString(ip.InlinePolicy), "s3:DeleteBucket") {
		t.Errorf("inline policy in moto: %v %v", err, aws.ToString(ip.InlinePolicy))
	}

	// Resyncs of an unchanged set (an unordered list, a free-form policy
	// compared as a document) make no update.
	time.Sleep(12 * time.Second)
	if n := len(e.calls("UpdateResource", psType)); n != 0 {
		t.Errorf("%d update(s) of an unchanged permission set: %v", n, patchPaths(t, e.calls("UpdateResource", psType)))
	}

	// A second managed policy: one replace of the list; moto has both.
	if err := e.update(t, ps, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedSlice(o.Object, []any{readOnly, viewOnly}, "spec", "managedPolicies")
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "two managed policies in moto", func() error {
		if l := managedPolicies(t, e, inst, arn); len(l) != 2 {
			return fmt.Errorf("%v", l)
		}
		return nil
	})
	if p := patchPaths(t, e.calls("UpdateResource", psType)); strings.Join(p, ",") != "replace /ManagedPolicies" {
		t.Errorf("patches: %v, want one replace of /ManagedPolicies", p)
	}
	e.waitSynced(t, ps)

	// The name is create-only: the API server refuses the change.
	err = e.update(t, ps, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, "Renamed", "spec", "name")
	})
	if err == nil || !strings.Contains(err.Error(), "create-only") {
		t.Errorf("a new name was not refused as create-only: %v", err)
	}

	// The set assigned to a group in an account.
	const asType = "AWS::SSO::Assignment"
	as := obj("Assignment", "platform-readonly-prod", map[string]any{
		"instanceArn": inst, "permissionSetArn": arn,
		"targetId": "111122223333", "targetType": "AWS_ACCOUNT",
		"principalType": "GROUP", "principalId": "90677d5c-1234-5678-9abc-def012345678",
	})
	if err := e.c.Create(e.ctx, as); err != nil {
		t.Fatal(err)
	}
	got = e.waitSynced(t, as)
	aid, _, _ := unstructured.NestedString(got.Object, "status", "cloudControl", "identifier")
	if want := strings.Join([]string{inst, "111122223333", "AWS_ACCOUNT", arn, "GROUP", "90677d5c-1234-5678-9abc-def012345678"}, "|"); aid != want {
		t.Errorf("assignment identifier %q, want %q", aid, want)
	}
	list := func() int {
		out, err := sso.ListAccountAssignments(e.ctx, &ssoadmin.ListAccountAssignmentsInput{InstanceArn: &inst, AccountId: aws.String("111122223333"), PermissionSetArn: &arn})
		if err != nil {
			t.Fatal(err)
		}
		return len(out.AccountAssignments)
	}
	if n := list(); n != 1 {
		t.Errorf("%d assignments in moto, want 1", n)
	}
	err = e.update(t, as, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, "222233334444", "spec", "targetId")
	})
	if err == nil || !strings.Contains(err.Error(), "create-only") {
		t.Errorf("a new account was not refused as create-only: %v", err)
	}

	if err := e.c.Delete(e.ctx, as); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, as)
	if n := list(); n != 0 {
		t.Errorf("%d assignments in moto after delete", n)
	}
	if n := len(e.calls("DeleteResource", asType)); n != 1 {
		t.Errorf("%d DeleteResource calls for the assignment, want 1", n)
	}
	if err := e.c.Delete(e.ctx, ps); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, ps)
	if _, err := sso.DescribePermissionSet(e.ctx, &ssoadmin.DescribePermissionSetInput{InstanceArn: &inst, PermissionSetArn: &arn}); err == nil {
		t.Error("the permission set is still in moto after delete")
	}
}
