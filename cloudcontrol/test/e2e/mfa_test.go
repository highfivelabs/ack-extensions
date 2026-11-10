// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// F03, a virtual MFA device enabled for a user: made with a path and tags,
// no update on resyncs, a tag change, a create-only change refused, then
// deleted (deactivated for the user first).
func TestVirtualMFADevice(t *testing.T) {
	e := start(t)
	const typ = "AWS::IAM::VirtualMFADevice"
	im := iam.NewFromConfig(e.moto)
	// The user is the test's fixture; on a live run it is deleted at the
	// end, after the device.
	const user = "ack-ext-e2e-mfa"
	// A user left by a live run cut off before its end is reused.
	if _, err := im.CreateUser(e.ctx, &iam.CreateUserInput{UserName: aws.String(user)}); err != nil && !strings.Contains(err.Error(), "EntityAlreadyExists") {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = im.DeleteUser(e.ctx, &iam.DeleteUserInput{UserName: aws.String(user)}) })

	u := obj("VirtualMFADevice", "alice-phone", map[string]any{
		"virtualMfaDeviceName": "alice-phone",
		"path":                 "/platform/",
		"users":                []any{user},
		"tags":                 []any{map[string]any{"key": "team", "value": "platform"}},
	})
	if err := e.c.Create(e.ctx, u); err != nil {
		t.Fatal(err)
	}
	got := e.waitSynced(t, u)
	serial, _, _ := unstructured.NestedString(got.Object, "status", "serialNumber")
	id, _, _ := unstructured.NestedString(got.Object, "status", "cloudControl", "identifier")
	if want := "arn:aws:iam::" + e.account + ":mfa/platform/alice-phone"; serial != want || id != want {
		t.Fatalf("status.serialNumber %q, identifier %q, want %q", serial, id, want)
	}
	devices, err := im.ListMFADevices(e.ctx, &iam.ListMFADevicesInput{UserName: aws.String(user)})
	if err != nil || len(devices.MFADevices) != 1 || aws.ToString(devices.MFADevices[0].SerialNumber) != serial {
		t.Fatalf("the user's MFA devices: %v %+v", err, devices)
	}

	// Resyncs of an unchanged device make no update.
	time.Sleep(12 * time.Second)
	if n := len(e.calls("UpdateResource", typ)); n != 0 {
		t.Errorf("%d update(s) of an unchanged device: %v", n, patchPaths(t, e.calls("UpdateResource", typ)))
	}

	// A tag change: one replace of the tags. moto keeps no MFA device tags
	// (the fake's backend does); on a live run IAM is asked.
	if err := e.update(t, u, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedSlice(o.Object, []any{
			map[string]any{"key": "team", "value": "security"},
		}, "spec", "tags")
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "the tag change sent", func() error {
		if p := patchPaths(t, e.calls("UpdateResource", typ)); strings.Join(p, ",") != "replace /Tags" {
			return fmt.Errorf("patches %v, want one replace of /Tags", p)
		}
		return nil
	})
	e.waitSynced(t, u)
	if e.live {
		tags, err := im.ListMFADeviceTags(e.ctx, &iam.ListMFADeviceTagsInput{SerialNumber: aws.String(serial)})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, tg := range tags.Tags {
			found = found || aws.ToString(tg.Key) == "team" && aws.ToString(tg.Value) == "security"
		}
		if !found {
			t.Errorf("tags in IAM: %+v", tags.Tags)
		}
	}

	// The name is create-only: the API server refuses the change.
	err = e.update(t, u, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, "bob-phone", "spec", "virtualMfaDeviceName")
	})
	if err == nil || !strings.Contains(err.Error(), "create-only") {
		t.Errorf("a new name was not refused as create-only: %v", err)
	}

	if err := e.c.Delete(e.ctx, u); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, u)
	if n := len(e.calls("DeleteResource", typ)); n != 1 {
		t.Errorf("%d DeleteResource calls, want 1", n)
	}
	all, err := im.ListVirtualMFADevices(e.ctx, &iam.ListVirtualMFADevicesInput{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range all.VirtualMFADevices {
		if aws.ToString(d.SerialNumber) == serial {
			t.Errorf("the device is still in IAM after delete")
		}
	}
	devices, err = im.ListMFADevices(e.ctx, &iam.ListMFADevicesInput{UserName: aws.String(user)})
	if err != nil || len(devices.MFADevices) != 0 {
		t.Errorf("the user's MFA devices after delete: %v %+v", err, devices)
	}
}
