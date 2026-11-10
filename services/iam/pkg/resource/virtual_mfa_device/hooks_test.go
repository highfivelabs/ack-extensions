// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package virtual_mfa_device

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/iam"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

func device(name string, secretName *string) *svcapitypes.VirtualMFADevice {
	return &svcapitypes.VirtualMFADevice{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team", UID: "uid-1"},
		Spec:       svcapitypes.VirtualMFADeviceSpec{Name: aws.String(name), SecretName: secretName},
	}
}

func TestSecretName(t *testing.T) {
	empty, set := "", "root-mfa"
	for _, c := range []struct {
		spec *string
		want string
	}{{nil, "root"}, {&empty, "root"}, {&set, "root-mfa"}} {
		if got := secretName(device("root", c.spec)); got != c.want {
			t.Errorf("secretName(%v) = %q, want %q", c.spec, got, c.want)
		}
	}
}

func TestNewSecret(t *testing.T) {
	ko := device("root", nil)
	seed, qr := []byte("NOTAREALSEEDNOTAREALSEED"), []byte("\x89PNG")
	s := newSecret(ko, "arn:aws:iam::123456789012:mfa/root", seed, qr)
	if s.Name != "root" || s.Namespace != "team" || s.Type != corev1.SecretTypeOpaque {
		t.Fatalf("secret %s/%s type %s", s.Namespace, s.Name, s.Type)
	}
	if s.Immutable == nil || !*s.Immutable {
		t.Error("not immutable")
	}
	if len(s.Data) != 3 || string(s.Data[SerialField]) != "arn:aws:iam::123456789012:mfa/root" ||
		string(s.Data[SeedField]) != string(seed) || string(s.Data[QRCodeField]) != string(qr) {
		t.Errorf("data keys %d", len(s.Data))
	}
	// The data are copies: clearing the response's seed leaves the Secret's.
	clear(seed)
	if string(s.Data[SeedField]) != "NOTAREALSEEDNOTAREALSEED" {
		t.Error("the Secret shares the response's seed")
	}
	if len(s.OwnerReferences) != 1 {
		t.Fatalf("owners %+v", s.OwnerReferences)
	}
	o := s.OwnerReferences[0]
	if o.UID != "uid-1" || o.Kind != "VirtualMFADevice" || o.APIVersion != "iam.aws.hfl.dev/v1alpha1" || o.Controller == nil || !*o.Controller || o.BlockOwnerDeletion != nil {
		t.Errorf("owner %+v", o)
	}
	if !ownedBy(s, ko) {
		t.Error("not owned by its object")
	}
	other := device("root", nil)
	other.UID = "uid-2"
	if ownedBy(s, other) {
		t.Error("owned by another object of the same name")
	}
	if s := newSecret(ko, "arn", []byte("SEED"), nil); len(s.Data) != 2 {
		t.Errorf("no QR code: data keys %d", len(s.Data))
	}
}

func TestHolds(t *testing.T) {
	page := &svcsdk.ListVirtualMFADevicesOutput{VirtualMFADevices: []svcsdktypes.VirtualMFADevice{
		{SerialNumber: aws.String("arn:aws:iam::123456789012:mfa/a")},
		{},
	}}
	if !holds(page, "arn:aws:iam::123456789012:mfa/a") || holds(page, "arn:aws:iam::123456789012:mfa/b") {
		t.Error("holds")
	}
}
