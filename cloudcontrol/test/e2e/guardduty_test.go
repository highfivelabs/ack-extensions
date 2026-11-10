// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/guardduty"
	gdtypes "github.com/aws/aws-sdk-go-v2/service/guardduty/types"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// F15, GuardDuty: a findings filter on a detector (a map of conditions
// keyed by finding field), a resync of an unchanged filter, a change of its
// action and criteria, drift made in GuardDuty put back, a create-only name
// refused; then both deleted.
func TestGuardDutyFilter(t *testing.T) {
	e := start(t)
	gd := guardduty.NewFromConfig(e.moto)
	const fType = "AWS::GuardDuty::Filter"

	det := obj("Detector", "filter-detector", map[string]any{"enable": true})
	if err := e.c.Create(e.ctx, det); err != nil {
		t.Fatal(err)
	}
	got := e.waitSynced(t, det)
	detID, _, _ := unstructured.NestedString(got.Object, "status", "id")
	if detID == "" {
		t.Fatalf("no detector id: %v", got.Object["status"])
	}

	f := obj("Filter", "archive-low-severity", map[string]any{
		"detectorId":  detID,
		"name":        "archive-low-severity",
		"action":      "ARCHIVE",
		"description": "archive low-severity port probes",
		"rank":        int64(1),
		"findingCriteria": map[string]any{"criterion": map[string]any{
			"severity": map[string]any{"lt": int64(4)},
			"type":     map[string]any{"eq": []any{"Recon:EC2/PortProbeUnprotectedPort"}},
		}},
		"tags": []any{map[string]any{"key": "team", "value": "security"}},
	})
	if err := e.c.Create(e.ctx, f); err != nil {
		t.Fatal(err)
	}
	got = e.waitSynced(t, f)
	if id, _, _ := unstructured.NestedString(got.Object, "status", "cloudControl", "identifier"); id != detID+"|archive-low-severity" {
		t.Errorf("identifier %q", id)
	}
	getFilter := func() (*guardduty.GetFilterOutput, error) {
		return gd.GetFilter(e.ctx, &guardduty.GetFilterInput{DetectorId: aws.String(detID), FilterName: aws.String("archive-low-severity")})
	}
	out, err := getFilter()
	if err != nil {
		t.Fatalf("moto: %v", err)
	}
	if out.Action != gdtypes.FilterActionArchive || aws.ToString(out.Description) != "archive low-severity port probes" {
		t.Errorf("moto: action %s, description %q", out.Action, aws.ToString(out.Description))
	}
	sev, typ := out.FindingCriteria.Criterion["severity"], out.FindingCriteria.Criterion["type"]
	if aws.ToInt32(sev.Lt) != 4 || strings.Join(typ.Eq, ",") != "Recon:EC2/PortProbeUnprotectedPort" {
		t.Errorf("moto's criteria: severity %+v, type %+v", sev, typ)
	}

	// Resyncs of an unchanged filter (a map of objects, integers) make no
	// update.
	time.Sleep(12 * time.Second)
	if n := len(e.calls("UpdateResource", fType)); n != 0 {
		t.Errorf("%d update(s) of an unchanged filter: %v", n, patchPaths(t, e.calls("UpdateResource", fType)))
	}

	// The action and a criterion changed: replaces of both.
	if err := e.update(t, f, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, "NOOP", "spec", "action")
		_ = unstructured.SetNestedField(o.Object, int64(3), "spec", "findingCriteria", "criterion", "severity", "lt")
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "the new action and criteria in moto", func() error {
		out, err := getFilter()
		if err != nil {
			return err
		}
		if out.Action != gdtypes.FilterActionNoop || aws.ToInt32(out.FindingCriteria.Criterion["severity"].Lt) != 3 {
			return fmt.Errorf("action %s, severity %+v", out.Action, out.FindingCriteria.Criterion["severity"])
		}
		return nil
	})
	if p := strings.Join(patchPaths(t, e.calls("UpdateResource", fType)), ","); p != "replace /Action,replace /FindingCriteria" && p != "replace /FindingCriteria,replace /Action" {
		t.Errorf("patches: %s, want replaces of /Action and /FindingCriteria", p)
	}
	e.waitSynced(t, f)

	// Drift: the description changed in GuardDuty; the resync puts it back.
	before := len(e.calls("UpdateResource", fType))
	cur, err := getFilter()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gd.UpdateFilter(e.ctx, &guardduty.UpdateFilterInput{
		DetectorId: aws.String(detID), FilterName: aws.String("archive-low-severity"),
		Description: aws.String("changed by hand"), Action: cur.Action, FindingCriteria: cur.FindingCriteria, Rank: cur.Rank,
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "drift put back in moto", func() error {
		out, err := getFilter()
		if err != nil {
			return err
		}
		if d := aws.ToString(out.Description); d != "archive low-severity port probes" {
			return fmt.Errorf("description %q", d)
		}
		return nil
	})
	if p := patchPaths(t, e.calls("UpdateResource", fType)[before:]); strings.Join(p, ",") != "replace /Description" {
		t.Errorf("drift patches: %v, want one replace of /Description", p)
	}

	// The name is create-only.
	err = e.update(t, f, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, "renamed", "spec", "name")
	})
	if err == nil || !strings.Contains(err.Error(), "create-only") {
		t.Errorf("a new name was not refused as create-only: %v", err)
	}

	if err := e.c.Delete(e.ctx, f); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, f)
	if _, err := getFilter(); err == nil {
		t.Error("the filter is still in moto after delete")
	}
	if err := e.c.Delete(e.ctx, det); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, det)
}
