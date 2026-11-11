// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/aws/aws-sdk-go-v2/service/securityhub"
	shtypes "github.com/aws/aws-sdk-go-v2/service/securityhub/types"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// F16, Security Hub: the hub (tags as a map), a resync of an unchanged hub,
// a change of a setting; the organization configuration set by the
// delegated administrator, changed, and turned off by its delete; then the
// hub disabled by its delete.
func TestSecurityHub(t *testing.T) {
	if target(t) == "real" {
		t.Skip("the organization configuration needs an organization; a live run's sandbox account has none")
	}
	e := start(t)
	sh := securityhub.NewFromConfig(e.moto)
	const hubType, ocType = "AWS::SecurityHub::Hub", "AWS::SecurityHub::OrganizationConfiguration"

	hub := obj("Hub", "hub", map[string]any{
		"enableDefaultStandards":  false,
		"controlFindingGenerator": "STANDARD_CONTROL",
		"autoEnableControls":      true,
		"tags":                    map[string]any{"team": "security"},
	})
	if err := e.c.Create(e.ctx, hub); err != nil {
		t.Fatal(err)
	}
	got := e.waitSynced(t, hub)
	arn, _, _ := unstructured.NestedString(got.Object, "status", "arn")
	if want := fmt.Sprintf("arn:aws:securityhub:%s:%s:hub/default", region, e.account); arn != want {
		t.Fatalf("status.arn %q, want %q", arn, want)
	}
	if at, _, _ := unstructured.NestedString(got.Object, "status", "subscribedAt"); at == "" {
		t.Error("no status.subscribedAt")
	}
	if _, err := sh.DescribeHub(e.ctx, &securityhub.DescribeHubInput{HubArn: aws.String(arn)}); err != nil {
		t.Fatalf("moto has no hub: %v", err)
	}
	time.Sleep(12 * time.Second)
	if n := len(e.calls("UpdateResource", hubType)); n != 0 {
		t.Errorf("%d update(s) of an unchanged hub: %v", n, patchPaths(t, e.calls("UpdateResource", hubType)))
	}
	if err := e.update(t, hub, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, false, "spec", "autoEnableControls")
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "the update made", func() error {
		if p := patchPaths(t, e.calls("UpdateResource", hubType)); strings.Join(p, ",") != "replace /AutoEnableControls" {
			return fmt.Errorf("patches %v", p)
		}
		return nil
	})
	e.waitSynced(t, hub)

	// The organization, and this account as Security Hub's delegated
	// administrator (fixtures, through moto's own APIs).
	org := organizations.NewFromConfig(e.moto)
	if _, err := org.CreateOrganization(e.ctx, &organizations.CreateOrganizationInput{FeatureSet: orgtypes.OrganizationFeatureSetAll}); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.EnableOrganizationAdminAccount(e.ctx, &securityhub.EnableOrganizationAdminAccountInput{AdminAccountId: aws.String(e.account)}); err != nil {
		t.Fatal(err)
	}
	orgConfig := func() (*securityhub.DescribeOrganizationConfigurationOutput, error) {
		return sh.DescribeOrganizationConfiguration(e.ctx, &securityhub.DescribeOrganizationConfigurationInput{})
	}

	oc := obj("OrganizationConfiguration", "organization", map[string]any{"autoEnable": true, "autoEnableStandards": "DEFAULT"})
	if err := e.c.Create(e.ctx, oc); err != nil {
		t.Fatal(err)
	}
	got = e.waitSynced(t, oc)
	if id, _, _ := unstructured.NestedString(got.Object, "status", "organizationConfigurationIdentifier"); id != e.account+"/"+region+"/securityhub-organization-configuration" {
		t.Errorf("status.organizationConfigurationIdentifier %q", id)
	}
	if out, err := orgConfig(); err != nil || !aws.ToBool(out.AutoEnable) || out.AutoEnableStandards != shtypes.AutoEnableStandardsDefault {
		t.Fatalf("moto: %v %+v", err, out)
	}
	if err := e.update(t, oc, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, false, "spec", "autoEnable")
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "auto-enable off in moto", func() error {
		out, err := orgConfig()
		if err != nil {
			return err
		}
		if aws.ToBool(out.AutoEnable) {
			return fmt.Errorf("auto-enable on")
		}
		return nil
	})
	if p := patchPaths(t, e.calls("UpdateResource", ocType)); strings.Join(p, ",") != "replace /AutoEnable" {
		t.Errorf("patches: %v, want one replace of /AutoEnable", p)
	}
	e.waitSynced(t, oc)

	// Its delete turns auto-enable off, standards included.
	if err := e.update(t, oc, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, true, "spec", "autoEnable")
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "auto-enable on again in moto", func() error {
		out, err := orgConfig()
		if err != nil {
			return err
		}
		if !aws.ToBool(out.AutoEnable) {
			return fmt.Errorf("auto-enable off")
		}
		return nil
	})
	if err := e.c.Delete(e.ctx, oc); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, oc)
	if out, err := orgConfig(); err != nil || aws.ToBool(out.AutoEnable) || out.AutoEnableStandards != shtypes.AutoEnableStandardsNone {
		t.Errorf("after delete, moto: %v %+v", err, out)
	}

	if err := e.c.Delete(e.ctx, hub); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, hub)
	if _, err := sh.DescribeHub(e.ctx, &securityhub.DescribeHubInput{}); err == nil {
		t.Error("Security Hub is still enabled in moto after delete")
	}
}
