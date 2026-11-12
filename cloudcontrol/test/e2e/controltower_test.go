// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/test/fake"
)

const (
	deletionPolicy = "services.k8s.aws/deletion-policy"
	retainOnlyMsg  = "must carry the annotation services.k8s.aws/deletion-policy: retain"
)

// installRetainOnly applies config/admission/retain-only.yaml, the policy
// the install ships, to the test's API server.
func installRetainOnly(t *testing.T, e *env) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "config", "admission", "retain-only.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	docs := bytes.Split(raw, []byte("\n---\n"))
	if len(docs) != 2 {
		t.Fatalf("retain-only.yaml: want a policy and a binding, got %d documents", len(docs))
	}
	policy, binding := &admissionv1.ValidatingAdmissionPolicy{}, &admissionv1.ValidatingAdmissionPolicyBinding{}
	if err := yaml.UnmarshalStrict(docs[0], policy); err != nil {
		t.Fatal(err)
	}
	if err := yaml.UnmarshalStrict(docs[1], binding); err != nil {
		t.Fatal(err)
	}
	for _, o := range []client.Object{policy, binding} {
		if err := e.c.Create(e.ctx, o); err != nil {
			t.Fatal(err)
		}
	}
}

func landingZoneManifest(regions ...string) map[string]any {
	var rs []any
	for _, r := range regions {
		rs = append(rs, r)
	}
	return map[string]any{
		"governedRegions":       rs,
		"organizationStructure": map[string]any{"security": map[string]any{"name": "Security"}},
		"centralizedLogging": map[string]any{
			"accountId": "111111111111",
			"enabled":   true,
			"configurations": map[string]any{
				"loggingBucket":       map[string]any{"retentionDays": int64(365)},
				"accessLoggingBucket": map[string]any{"retentionDays": int64(3650)},
			},
		},
		"securityRoles":    map[string]any{"accountId": "222222222222"},
		"accessManagement": map[string]any{"enabled": true},
	}
}

// F07, Control Tower's landing zone, on a test double (no emulator has
// Control Tower). A landing zone is never deleted: deleting its object,
// even one made before the retain-only policy and without the annotation,
// keeps it, with no DeleteResource call. The policy then refuses an object
// without deletion-policy: retain and any change to it, and the kept
// landing zone is adopted again.
func TestLandingZone(t *testing.T) {
	if target(t) == "real" {
		t.Skip("a landing zone is never made on a live run: a test could not delete it")
	}
	e := start(t)
	if e.emulator != nil {
		t.Skip("ACK_TEST_BACKEND=aws-emulator: the landing zone is a test double of ours, which the emulator does not replace")
	}
	const lzType = "AWS::ControlTower::LandingZone"
	srv, ok := e.cc.(*fake.Server)
	if !ok {
		t.Fatalf("the fake is a %T", e.cc)
	}
	lz, ok := srv.Backend(lzType).(*fake.LandingZone)
	if !ok {
		t.Fatalf("%s is served by %T", lzType, srv.Backend(lzType))
	}

	// Made before any policy, without the annotation: the manager's
	// default deletion policy here is delete.
	u := obj("LandingZone", "landing-zone", map[string]any{
		"version":          "3.3",
		"manifest":         landingZoneManifest("us-west-2"),
		"remediationTypes": []any{"INHERITANCE_DRIFT"},
		"tags":             []any{map[string]any{"key": "owner", "value": "platform"}},
	})
	if err := e.c.Create(e.ctx, u); err != nil {
		t.Fatal(err)
	}
	got := e.waitSynced(t, u)
	id, _, _ := unstructured.NestedString(got.Object, "status", "landingZoneIdentifier")
	cid, _, _ := unstructured.NestedString(got.Object, "status", "cloudControl", "identifier")
	arn, _, _ := unstructured.NestedString(got.Object, "status", "arn")
	if id == "" || cid != id || arn != fmt.Sprintf("arn:aws:controltower:%s:%s:landingzone/%s", region, motoAccount, id) {
		t.Fatalf("status.landingZoneIdentifier %q, identifier %q, arn %q", id, cid, arn)
	}
	for f, want := range map[string]string{"status": "ACTIVE", "driftStatus": "IN_SYNC", "latestAvailableVersion": fake.LatestLandingZoneVersion} {
		if v, _, _ := unstructured.NestedString(got.Object, "status", f); v != want {
			t.Errorf("status.%s = %q, want %q", f, v, want)
		}
	}
	zone := lz.Zone(id)
	if zone == nil || zone["Version"] != "3.3" {
		t.Fatalf("the double: %v", zone)
	}
	if r := fmt.Sprint(zone["Manifest"].(map[string]any)["governedRegions"]); r != "[us-west-2]" {
		t.Errorf("governed regions in the double: %s", r)
	}

	// A second governed region: one replace of the manifest, compared
	// whole as a free-form document.
	if err := e.update(t, u, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, landingZoneManifest("us-west-2", "eu-west-1"), "spec", "manifest")
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "two governed regions in the double", func() error {
		if r := fmt.Sprint(lz.Zone(id)["Manifest"].(map[string]any)["governedRegions"]); r != "[us-west-2 eu-west-1]" {
			return fmt.Errorf("%s", r)
		}
		return nil
	})
	if p := patchPaths(t, e.calls("UpdateResource", lzType)); strings.Join(p, ",") != "replace /Manifest" {
		t.Errorf("patches: %v, want one replace of /Manifest", p)
	}
	e.waitSynced(t, u)

	// Deleted without the annotation, under a manager whose default is
	// delete: the object goes, the landing zone stays.
	if err := e.c.Delete(e.ctx, u); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, u)
	if n := len(e.calls("DeleteResource", lzType)); n != 0 {
		t.Errorf("%d DeleteResource calls for the landing zone", n)
	}
	if lz.Deletes() != 0 || lz.Zone(id) == nil {
		t.Fatalf("the landing zone was deleted (%d deletes)", lz.Deletes())
	}
	if !strings.Contains(e.logs.String(), "kept in AWS: the type is never deleted") {
		t.Error("the manager did not log that it kept the landing zone")
	}

	// The retain-only policy: an object must say what a delete does.
	installRetainOnly(t, e)
	bare := obj("LandingZone", "landing-zone-bare", map[string]any{"version": "3.3", "manifest": landingZoneManifest("us-west-2")})
	eventually(t, 60*time.Second, "the retain-only policy in force", func() error {
		err := e.c.Create(e.ctx, bare.DeepCopy(), client.DryRunAll)
		if err == nil || !strings.Contains(err.Error(), retainOnlyMsg) {
			return fmt.Errorf("a LandingZone without the annotation: %v", err)
		}
		return nil
	})
	del := obj("LandingZone", "landing-zone-delete", map[string]any{"version": "3.3", "manifest": landingZoneManifest("us-west-2")})
	del.SetAnnotations(map[string]string{deletionPolicy: "delete"})
	if err := e.c.Create(e.ctx, del); err == nil || !strings.Contains(err.Error(), retainOnlyMsg) {
		t.Errorf("a LandingZone with deletion-policy delete: %v, want the policy's refusal", err)
	}

	// Adopted again, retained: spec from AWS.
	a := obj("LandingZone", "landing-zone-adopted", map[string]any{"version": "3.3", "manifest": landingZoneManifest("us-west-2")})
	a.SetAnnotations(map[string]string{
		deletionPolicy:                     "retain",
		"services.k8s.aws/adoption-policy": "adopt",
		"services.k8s.aws/adoption-fields": fmt.Sprintf(`{"landingZoneIdentifier": %q}`, id),
	})
	if err := e.c.Create(e.ctx, a); err != nil {
		t.Fatal(err)
	}
	got = e.waitSynced(t, a)
	if got.GetAnnotations()[ackv1alpha1.AnnotationAdopted] != "true" {
		t.Errorf("not marked adopted: %v", got.GetAnnotations())
	}
	if s, _, _ := unstructured.NestedString(got.Object, "status", "cloudControl", "identifier"); s != id {
		t.Errorf("adopted identifier %q, want %q", s, id)
	}
	if r, _, _ := unstructured.NestedSlice(got.Object, "spec", "manifest", "governedRegions"); fmt.Sprint(r) != "[us-west-2 eu-west-1]" {
		t.Errorf("adopted spec.manifest.governedRegions = %v, want AWS's", r)
	}

	// The annotation cannot be changed or removed.
	for what, change := range map[string]func(map[string]string){
		"changed to delete": func(m map[string]string) { m[deletionPolicy] = "delete" },
		"removed":           func(m map[string]string) { delete(m, deletionPolicy) },
	} {
		err := e.update(t, a, func(o *unstructured.Unstructured) {
			m := o.GetAnnotations()
			change(m)
			o.SetAnnotations(m)
		})
		if err == nil || !strings.Contains(err.Error(), retainOnlyMsg) {
			t.Errorf("the annotation %s: %v, want the policy's refusal", what, err)
		}
	}

	if err := e.c.Delete(e.ctx, a); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, a)
	if n := len(e.calls("DeleteResource", lzType)); n != 0 || lz.Deletes() != 0 || lz.Zone(id) == nil {
		t.Errorf("after the adopted object's delete: %d DeleteResource calls, %d deletes, zone %v", n, lz.Deletes(), lz.Zone(id) != nil)
	}
}
