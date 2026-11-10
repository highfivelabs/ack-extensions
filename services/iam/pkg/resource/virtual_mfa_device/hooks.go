// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package virtual_mfa_device

// The device's seed. CreateVirtualMFADevice returns the Base32 seed and the
// QR code (the same seed as a PNG) once and never again, so they go into a
// Kubernetes Secret in the object's namespace, owned by the object, and
// nowhere else: never into the object (the generator ignores both fields),
// never into a log, an error, an event or the status. The Secret is made
// once and never written over; a new seed is a new VirtualMFADevice
// (docs/plan.md, the VirtualMFADevice section). The pattern is AccessKey's
// (services/iam/pkg/resource/access_key/hooks.go).
//
// The Secret is also the record of which device the object made: it holds
// the serial number beside the seed, so an object whose status lost the
// serial number (a status write that failed after the create) finds its
// device again instead of making a second one.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/iam"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrlrt "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

// The manager may make and delete the Secrets it writes seeds into; ACK's
// runtime only reads and patches Secrets.
// +kubebuilder:rbac:groups="",resources=secrets,verbs=create;delete

const (
	// SerialField, SeedField and QRCodeField are the Secret's keys: the
	// device's serial number (its ARN, which a user's EnableMFADevice
	// names), the Base32 seed an authenticator takes as text, and the same
	// seed as a QR code PNG for an authenticator's camera.
	SerialField = "serialNumber"
	SeedField   = "base32StringSeed"
	QRCodeField = "qrCodePNG"
)

var (
	kubeOnce sync.Once
	kube     client.Client
	kubeErr  error
)

// kubeClient is a client of the manager's own cluster, from the same
// configuration (--kubeconfig, KUBECONFIG or in-cluster) as the manager's.
// ACK's runtime gives the resource manager no client that can make a Secret.
func kubeClient() (client.Client, error) {
	kubeOnce.Do(func() {
		cfg, err := ctrlrt.GetConfig()
		if err != nil {
			kubeErr = err
			return
		}
		kube, kubeErr = client.New(cfg, client.Options{})
	})
	return kube, kubeErr
}

// secretName is the spec's, or the object's name when the spec has none.
// The create writes it into the spec (sdk_create_post_set_output), where
// the CRD keeps it from changing: a field set from unset would pass the
// CRD's rule (self == oldSelf), and the device's Secret would be lost track
// of. Removing it changes nothing, since the default is the same name.
func secretName(ko *svcapitypes.VirtualMFADevice) string {
	if ko.Spec.SecretName != nil && *ko.Spec.SecretName != "" {
		return *ko.Spec.SecretName
	}
	return ko.Name
}

// ownedBy says whether the object owns the Secret.
func ownedBy(s *corev1.Secret, ko *svcapitypes.VirtualMFADevice) bool {
	for _, ref := range s.OwnerReferences {
		if ref.UID == ko.UID {
			return true
		}
	}
	return false
}

// getSecret reads the object's Secret; nil when there is none.
func getSecret(ctx context.Context, ko *svcapitypes.VirtualMFADevice) (*corev1.Secret, error) {
	c, err := kubeClient()
	if err != nil {
		return nil, err
	}
	s := &corev1.Secret{}
	err = c.Get(ctx, types.NamespacedName{Namespace: ko.Namespace, Name: secretName(ko)}, s)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// serialFromSecret is the serial number of the device the object made, from
// the Secret the object owns; empty when there is none. The read uses it
// when the status has no serial number.
func (rm *resourceManager) serialFromSecret(ctx context.Context, ko *svcapitypes.VirtualMFADevice) (string, error) {
	s, err := getSecret(ctx, ko)
	if err != nil || s == nil || !ownedBy(s, ko) {
		return "", err
	}
	return string(s.Data[SerialField]), nil
}

// listUntilFound reads the next pages of ListVirtualMFADevices until one
// holds the device, or there are no more; the generated read matches on
// the serial number in the page returned. The first page is the generated
// call's.
func (rm *resourceManager) listUntilFound(
	ctx context.Context,
	input *svcsdk.ListVirtualMFADevicesInput,
	resp *svcsdk.ListVirtualMFADevicesOutput,
	serial *string,
) (*svcsdk.ListVirtualMFADevicesOutput, error) {
	for serial != nil && !holds(resp, *serial) && resp.IsTruncated && resp.Marker != nil {
		next := *input
		next.Marker = resp.Marker
		page, err := rm.sdkapi.ListVirtualMFADevices(ctx, &next)
		rm.metrics.RecordAPICall("READ_MANY", "ListVirtualMFADevices", err)
		if err != nil {
			return nil, err
		}
		resp = page
	}
	return resp, nil
}

// holds says whether a page of the list has the device.
func holds(resp *svcsdk.ListVirtualMFADevicesOutput, serial string) bool {
	for _, d := range resp.VirtualMFADevices {
		if d.SerialNumber != nil && *d.SerialNumber == serial {
			return true
		}
	}
	return false
}

// secretFree refuses a create whose Secret exists already: a seed is never
// written over another. When the object owns that Secret, its device was
// deleted outside Kubernetes (or never read back); either way only a new
// object, or deleting the Secret, makes a new device.
func (rm *resourceManager) secretFree(ctx context.Context, ko *svcapitypes.VirtualMFADevice) error {
	s, err := getSecret(ctx, ko)
	if err != nil {
		return err
	}
	if s == nil {
		return nil
	}
	if ownedBy(s, ko) {
		return ackerr.NewTerminalError(fmt.Errorf(
			"Secret %s/%s holds the seed of MFA device %s, which IAM no longer has; it is never written over: delete the Secret to make a new device, or make a new VirtualMFADevice",
			s.Namespace, s.Name, string(s.Data[SerialField])))
	}
	return ackerr.NewTerminalError(fmt.Errorf(
		"Secret %s/%s exists and is not this VirtualMFADevice's; it is never written over: name another in spec.secretName",
		s.Namespace, s.Name))
}

// newSecret is the Secret holding the seed, owned by the object. The data
// are copies; the caller clears its own.
func newSecret(ko *svcapitypes.VirtualMFADevice, serial string, seed, qr []byte) *corev1.Secret {
	data := map[string][]byte{
		SerialField: []byte(serial),
		SeedField:   bytes.Clone(seed),
	}
	if len(qr) > 0 {
		data[QRCodeField] = bytes.Clone(qr)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName(ko),
			Namespace: ko.Namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "ack-extensions-iam-manager"},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: svcapitypes.GroupVersion.String(),
				Kind:       "VirtualMFADevice",
				Name:       ko.Name,
				UID:        ko.UID,
				Controller: func() *bool { b := true; return &b }(),
			}},
		},
		Type:      corev1.SecretTypeOpaque,
		Immutable: func() *bool { b := true; return &b }(),
		Data:      data,
	}
}

// storeSeed writes the new device's seed and QR code into the object's
// Secret and clears them from the response. If the Secret cannot be made,
// the device is deleted, since nobody could ever enroll it, and the create
// is tried again later. Its errors never carry the seed, and never wrap an
// AWS error (the runtime would take the object as unmanaged).
func (rm *resourceManager) storeSeed(ctx context.Context, ko *svcapitypes.VirtualMFADevice, dev *svcsdktypes.VirtualMFADevice) error {
	if dev == nil || dev.SerialNumber == nil || len(dev.Base32StringSeed) == 0 {
		return errors.New("CreateVirtualMFADevice returned no seed")
	}
	s := newSecret(ko, *dev.SerialNumber, dev.Base32StringSeed, dev.QRCodePNG)
	clear(dev.Base32StringSeed)
	clear(dev.QRCodePNG)
	dev.Base32StringSeed, dev.QRCodePNG = nil, nil
	c, err := kubeClient()
	if err == nil {
		err = c.Create(ctx, s)
	}
	for _, v := range s.Data {
		clear(v)
	}
	if err == nil {
		return nil
	}
	cause := fmt.Sprintf("the MFA device's Secret %s/%s could not be made (%s)", ko.Namespace, secretName(ko), err)
	_, derr := rm.sdkapi.DeleteVirtualMFADevice(ctx, &svcsdk.DeleteVirtualMFADeviceInput{SerialNumber: dev.SerialNumber})
	rm.metrics.RecordAPICall("DELETE", "DeleteVirtualMFADevice", derr)
	if derr != nil {
		// Its seed is lost, so the device can never be enrolled; it is left
		// for a person to delete rather than made again beside it.
		return ackerr.NewTerminalError(fmt.Errorf("%s, and the device %s could not be deleted (%s): delete it by hand", cause, *dev.SerialNumber, derr.Error()))
	}
	if apierrors.IsAlreadyExists(err) {
		return ackerr.NewTerminalError(fmt.Errorf("%s; the device was deleted; it is never written over: name another in spec.secretName", cause))
	}
	return fmt.Errorf("%s; the device was deleted and is made again", cause)
}

// deleteSecret deletes the object's Secret, if the object owns it.
func (rm *resourceManager) deleteSecret(ctx context.Context, ko *svcapitypes.VirtualMFADevice) error {
	s, err := getSecret(ctx, ko)
	if err != nil || s == nil || !ownedBy(s, ko) {
		return err
	}
	c, err := kubeClient()
	if err != nil {
		return err
	}
	return client.IgnoreNotFound(c.Delete(ctx, s))
}

// customUpdateVirtualMFADevice refuses every change: the fields are
// immutable (the CRD refuses a changed name, path or secretName), and a new
// seed is a new device.
func (rm *resourceManager) customUpdateVirtualMFADevice(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta *ackcompare.Delta,
) (*resource, error) {
	paths := make([]string, 0, len(delta.Differences))
	for _, d := range delta.Differences {
		b, _ := d.Path.MarshalJSON()
		paths = append(paths, string(b))
	}
	return nil, ackerr.NewTerminalError(fmt.Errorf(
		"a VirtualMFADevice cannot change (%v): make a new VirtualMFADevice and delete this one", paths))
}
