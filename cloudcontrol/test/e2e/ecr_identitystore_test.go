// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"fmt"
	"strings"
	"testing"

	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/identitystore"
	istypes "github.com/aws/aws-sdk-go-v2/service/identitystore/types"
	"github.com/aws/aws-sdk-go-v2/service/ssoadmin"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
)

// objOf is obj for a type named by its CloudFormation name, for kinds
// whose name another service also uses.
func objOf(typeName, name string, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	u.SetGroupVersionKind(registry.ByTypeName(typeName).GVK())
	u.SetNamespace(envNS)
	u.SetName(name)
	return u
}

func registryPolicy(principal string) map[string]any {
	return map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{map[string]any{
			"Sid": "replicate", "Effect": "Allow",
			"Principal": map[string]any{"AWS": principal},
			"Action":    []any{"ecr:CreateRepository", "ecr:ReplicateImage"},
			"Resource":  "*",
		}},
	}
}

// F11, ECR: the registry's policy and its scanning configuration, two
// singletons per account and Region: made, changed, and deleted (the
// scanning configuration back to BASIC with no rules).
func TestECRRegistry(t *testing.T) {
	e := start(t)
	c := ecr.NewFromConfig(e.moto)
	src := fmt.Sprintf("arn:aws:iam::%s:root", "111122223333")
	other := fmt.Sprintf("arn:aws:iam::%s:root", "444455556666")

	pol := objOf("AWS::ECR::RegistryPolicy", "registry-policy", map[string]any{"policyText": registryPolicy(src)})
	if err := e.c.Create(e.ctx, pol); err != nil {
		t.Fatal(err)
	}
	got := e.waitSynced(t, pol)
	if id, _, _ := unstructured.NestedString(got.Object, "status", "registryId"); id != e.account {
		t.Errorf("status.registryId %q, want the account %q", id, e.account)
	}
	inMoto := func() (string, error) {
		out, err := c.GetRegistryPolicy(e.ctx, &ecr.GetRegistryPolicyInput{})
		if err != nil {
			return "", err
		}
		return aws.ToString(out.PolicyText), nil
	}
	if p, err := inMoto(); err != nil || !strings.Contains(p, src) {
		t.Fatalf("moto's registry policy: %v %s", err, p)
	}
	if err := e.update(t, pol, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, registryPolicy(other), "spec", "policyText")
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "the new principal in moto", func() error {
		p, err := inMoto()
		if err != nil || !strings.Contains(p, other) {
			return fmt.Errorf("%v %s", err, p)
		}
		return nil
	})

	scan := objOf("AWS::ECR::RegistryScanningConfiguration", "registry-scanning", map[string]any{
		"scanType": "ENHANCED",
		"rules": []any{map[string]any{
			"scanFrequency":     "SCAN_ON_PUSH",
			"repositoryFilters": []any{map[string]any{"filter": "prod-*", "filterType": "WILDCARD"}},
		}},
	})
	if err := e.c.Create(e.ctx, scan); err != nil {
		t.Fatal(err)
	}
	e.waitSynced(t, scan)
	scanning := func() (string, error) {
		out, err := c.GetRegistryScanningConfiguration(e.ctx, &ecr.GetRegistryScanningConfigurationInput{})
		if err != nil {
			return "", err
		}
		sc := out.ScanningConfiguration
		var b strings.Builder
		b.WriteString(string(sc.ScanType))
		for _, r := range sc.Rules {
			for _, f := range r.RepositoryFilters {
				fmt.Fprintf(&b, " %s:%s", r.ScanFrequency, aws.ToString(f.Filter))
			}
		}
		return b.String(), nil
	}
	if s, err := scanning(); err != nil || s != "ENHANCED SCAN_ON_PUSH:prod-*" {
		t.Fatalf("moto's scanning configuration: %v %q", err, s)
	}
	if err := e.update(t, scan, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedSlice(o.Object, []any{map[string]any{
			"scanFrequency":     "CONTINUOUS_SCAN",
			"repositoryFilters": []any{map[string]any{"filter": "*", "filterType": "WILDCARD"}},
		}}, "spec", "rules")
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "the new rule in moto", func() error {
		s, err := scanning()
		if err != nil || s != "ENHANCED CONTINUOUS_SCAN:*" {
			return fmt.Errorf("%v %q", err, s)
		}
		return nil
	})

	for _, u := range []*unstructured.Unstructured{pol, scan} {
		if err := e.c.Delete(e.ctx, u); err != nil {
			t.Fatal(err)
		}
		e.waitGone(t, u)
	}
	if p, err := inMoto(); err == nil {
		t.Errorf("after delete moto still has a registry policy: %s", p)
	}
	if s, err := scanning(); err != nil || s != "BASIC" {
		t.Errorf("after delete moto's scanning configuration is %q (%v), want BASIC with no rules", s, err)
	}
}

// F12, Identity Store: a group in the Identity Center directory and a
// user's membership of it, made and deleted. moto has no UpdateGroup, so a
// change to a group is not tested here.
func TestIdentityStore(t *testing.T) {
	e := start(t)
	insts, err := ssoadmin.NewFromConfig(e.moto).ListInstances(e.ctx, &ssoadmin.ListInstancesInput{})
	if err != nil || len(insts.Instances) == 0 {
		t.Fatalf("moto has no Identity Center instance: %v", err)
	}
	store := aws.ToString(insts.Instances[0].IdentityStoreId)
	is := identitystore.NewFromConfig(e.moto)

	g := objOf("AWS::IdentityStore::Group", "platform-admins", map[string]any{
		"identityStoreId": store, "displayName": "platform-admins", "description": "the platform team's administrators",
	})
	if err := e.c.Create(e.ctx, g); err != nil {
		t.Fatal(err)
	}
	got := e.waitSynced(t, g)
	gid, _, _ := unstructured.NestedString(got.Object, "status", "groupId")
	if gid == "" {
		t.Fatalf("no status.groupId: %v", got.Object["status"])
	}
	dg, err := is.DescribeGroup(e.ctx, &identitystore.DescribeGroupInput{IdentityStoreId: &store, GroupId: &gid})
	if err != nil || aws.ToString(dg.DisplayName) != "platform-admins" || aws.ToString(dg.Description) != "the platform team's administrators" {
		t.Fatalf("moto's group: %v %+v", err, dg)
	}

	user, err := is.CreateUser(e.ctx, &identitystore.CreateUserInput{
		IdentityStoreId: &store, UserName: aws.String("alice"), DisplayName: aws.String("Alice"),
		Name: &istypes.Name{GivenName: aws.String("Alice"), FamilyName: aws.String("Example")},
	})
	if err != nil {
		t.Fatal(err)
	}
	uid := aws.ToString(user.UserId)
	m := objOf("AWS::IdentityStore::GroupMembership", "alice-platform-admins", map[string]any{
		"identityStoreId": store, "groupId": gid, "memberId": map[string]any{"userId": uid},
	})
	if err := e.c.Create(e.ctx, m); err != nil {
		t.Fatal(err)
	}
	gotM := e.waitSynced(t, m)
	mid, _, _ := unstructured.NestedString(gotM.Object, "status", "membershipId")
	dm, err := is.DescribeGroupMembership(e.ctx, &identitystore.DescribeGroupMembershipInput{IdentityStoreId: &store, MembershipId: &mid})
	if err != nil || aws.ToString(dm.GroupId) != gid {
		t.Fatalf("moto's membership %q: %v %+v", mid, err, dm)
	}
	if u, ok := dm.MemberId.(*istypes.MemberIdMemberUserId); !ok || u.Value != uid {
		t.Errorf("membership's member %+v, want user %s", dm.MemberId, uid)
	}

	// The group is create-only on a membership.
	err = e.update(t, m, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, "another-group", "spec", "groupId")
	})
	if err == nil || !strings.Contains(err.Error(), "create-only") {
		t.Errorf("a new groupId was not refused as create-only: %v", err)
	}

	if err := e.c.Delete(e.ctx, m); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, m)
	if _, err := is.DescribeGroupMembership(e.ctx, &identitystore.DescribeGroupMembershipInput{IdentityStoreId: &store, MembershipId: &mid}); err == nil {
		t.Errorf("membership %s still in moto after delete", mid)
	}
	if err := e.c.Delete(e.ctx, g); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, g)
	if _, err := is.DescribeGroup(e.ctx, &identitystore.DescribeGroupInput{IdentityStoreId: &store, GroupId: &gid}); err == nil {
		t.Errorf("group %s still in moto after delete", gid)
	}
}
