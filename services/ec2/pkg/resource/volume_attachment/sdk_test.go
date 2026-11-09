// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package volume_attachment

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
)

// The attachment is the one to this instance, and a detached one is none.
func TestAttachmentTo(t *testing.T) {
	v := svcsdktypes.Volume{Attachments: []svcsdktypes.VolumeAttachment{
		{InstanceId: aws.String("i-old"), Device: aws.String("/dev/sdf"), State: svcsdktypes.VolumeAttachmentStateDetached},
		{InstanceId: aws.String("i-other"), Device: aws.String("/dev/sdg"), State: svcsdktypes.VolumeAttachmentStateAttached},
	}}
	if _, ok := attachmentTo(v, "i-old"); ok {
		t.Fatal("a detached attachment reads as attached")
	}
	if _, ok := attachmentTo(v, "i-app"); ok {
		t.Fatal("another instance's attachment reads as this one's")
	}
	if a, ok := attachmentTo(v, "i-other"); !ok || aws.ToString(a.Device) != "/dev/sdg" {
		t.Fatalf("the attachment to i-other: %v %v", a, ok)
	}
}

// A device changed outside Kubernetes is drift; the same attachment is not.
func TestDeviceIsCompared(t *testing.T) {
	att := func(device string) *resource {
		ko := &svcapitypes.VolumeAttachment{}
		ko.Spec.VolumeID, ko.Spec.InstanceID, ko.Spec.DeviceName = aws.String("vol-1"), aws.String("i-app"), aws.String(device)
		return &resource{ko}
	}
	if d := newResourceDelta(att("/dev/sdf"), att("/dev/sdf")); len(d.Differences) != 0 {
		t.Fatalf("the same attachment differs at %v", d.Differences)
	}
	if d := newResourceDelta(att("/dev/sdf"), att("/dev/sdh")); !d.DifferentAt("Spec.DeviceName") {
		t.Fatal("a changed device is no drift")
	}
}

func TestAttachedIsSynced(t *testing.T) {
	for state, want := range map[string]bool{"attaching": false, "attached": true, "detaching": false, "busy": false} {
		if got := attachmentAttached(aws.String(state)); got != want {
			t.Errorf("%s: synced %v, want %v", state, got, want)
		}
	}
}
