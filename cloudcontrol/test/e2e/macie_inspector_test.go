// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/inspector2"
	"github.com/aws/aws-sdk-go-v2/service/macie2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// F18, Macie: the account's Macie session and a findings filter, made,
// read back in moto, and deleted (moto 5.2.5 has no UpdateMacieSession or
// UpdateFindingsFilter).
func TestMacie(t *testing.T) {
	e := start(t)
	mc := macie2.NewFromConfig(e.moto)

	sess := objOf("AWS::Macie::Session", "macie-session", map[string]any{
		"findingPublishingFrequency": "ONE_HOUR",
		"status":                     "ENABLED",
	})
	if err := e.c.Create(e.ctx, sess); err != nil {
		t.Fatal(err)
	}
	got := e.waitSynced(t, sess)
	if acct, _, _ := unstructured.NestedString(got.Object, "status", "awsAccountId"); acct != e.account {
		t.Fatalf("status.awsAccountId %q, want %q", acct, e.account)
	}
	ms, err := mc.GetMacieSession(e.ctx, &macie2.GetMacieSessionInput{})
	if err != nil || ms.FindingPublishingFrequency != "ONE_HOUR" {
		t.Fatalf("moto's macie session: %v %+v", err, ms)
	}

	flt := objOf("AWS::Macie::FindingsFilter", "suppress-low", map[string]any{
		"name":        "suppress-low",
		"description": "suppress low severity findings",
		"action":      "ARCHIVE",
		"findingCriteria": map[string]any{
			"criterion": map[string]any{
				"severity.description": map[string]any{"eq": []any{"Low"}},
			},
		},
	})
	if err := e.c.Create(e.ctx, flt); err != nil {
		t.Fatal(err)
	}
	gotF := e.waitSynced(t, flt)
	fid, _, _ := unstructured.NestedString(gotF.Object, "status", "id")
	if fid == "" {
		t.Fatalf("no status.id on findings filter: %v", gotF.Object["status"])
	}
	gf, err := mc.GetFindingsFilter(e.ctx, &macie2.GetFindingsFilterInput{Id: aws.String(fid)})
	if err != nil || aws.ToString(gf.Name) != "suppress-low" {
		t.Fatalf("moto's findings filter: %v %+v", err, gf)
	}

	for _, u := range []*unstructured.Unstructured{flt, sess} {
		if err := e.c.Delete(e.ctx, u); err != nil {
			t.Fatal(err)
		}
		e.waitGone(t, u)
	}
	if _, err := mc.GetFindingsFilter(e.ctx, &macie2.GetFindingsFilterInput{Id: aws.String(fid)}); err == nil {
		t.Errorf("findings filter %s still in moto after delete", fid)
	}
}

// F18, Inspector v2: a findings filter, made, read back in moto, and
// deleted (moto 5.2.5 has no UpdateFilter).
func TestInspectorFilter(t *testing.T) {
	e := start(t)
	ic := inspector2.NewFromConfig(e.moto)

	flt := objOf("AWS::InspectorV2::Filter", "suppress-info", map[string]any{
		"name":         "suppress-info",
		"description":  "suppress informational findings",
		"filterAction": "SUPPRESS",
		"filterCriteria": map[string]any{
			"severity": []any{map[string]any{"comparison": "EQUALS", "value": "INFORMATIONAL"}},
		},
	})
	if err := e.c.Create(e.ctx, flt); err != nil {
		t.Fatal(err)
	}
	got := e.waitSynced(t, flt)
	arn, _, _ := unstructured.NestedString(got.Object, "status", "arn")
	if arn == "" {
		t.Fatalf("no status.arn on inspector filter: %v", got.Object["status"])
	}
	out, err := ic.ListFilters(e.ctx, &inspector2.ListFiltersInput{Arns: []string{arn}})
	if err != nil || len(out.Filters) != 1 || out.Filters[0].Action != "SUPPRESS" {
		t.Fatalf("moto's inspector filter: %v %+v", err, out)
	}

	if err := e.c.Delete(e.ctx, flt); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, flt)
	out, err = ic.ListFilters(e.ctx, &inspector2.ListFiltersInput{Arns: []string{arn}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Filters) != 0 {
		t.Errorf("inspector filter %s still in moto after delete", arn)
	}
}
