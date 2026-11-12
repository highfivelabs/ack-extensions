// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	iamv1 "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

// VirtualMFADevice: a virtual MFA device, its Base32 seed and QR code
// written once into a Kubernetes Secret owned by the object, and never into
// a log, the status or an event. ACK's iam-controller (v1.11.3) ignores
// VirtualMFADevice, and Cloud Control never returns the seed.
func TestIAMVirtualMFADevice(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = iamv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	env := harness.Start(t, "iam", scheme)
	cloud := iam.NewFromConfig(env.AWS)
	ctx := context.Background()

	// More devices than one page of the list (100), made first, so the
	// objects' devices are on a later page.
	for i := 0; i < 105; i++ {
		if _, err := cloud.CreateVirtualMFADevice(ctx, &iam.CreateVirtualMFADeviceInput{
			VirtualMFADeviceName: aws.String(fmt.Sprintf("filler-%03d", i)),
		}); err != nil {
			t.Fatal(err)
		}
	}

	devices := func(t *testing.T) map[string]bool {
		t.Helper()
		all := map[string]bool{}
		p := iam.NewListVirtualMFADevicesPaginator(cloud, &iam.ListVirtualMFADevicesInput{})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range page.VirtualMFADevices {
				if !strings.Contains(aws.ToString(d.SerialNumber), ":mfa/filler-") {
					all[aws.ToString(d.SerialNumber)] = true
				}
			}
		}
		return all
	}
	get := func(t *testing.T, obj *iamv1.VirtualMFADevice) *iamv1.VirtualMFADevice {
		t.Helper()
		got := &iamv1.VirtualMFADevice{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	waitSynced := func(t *testing.T, obj *iamv1.VirtualMFADevice) *iamv1.VirtualMFADevice {
		t.Helper()
		got := &iamv1.VirtualMFADevice{}
		harness.Eventually(t, 60*time.Second, obj.Name+" synced", func() error {
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
				return err
			}
			if ok, why := synced(got.Status.Conditions); !ok {
				return fmt.Errorf("not synced: %s", why)
			}
			return nil
		})
		return got
	}
	waitTerminal := func(t *testing.T, obj *iamv1.VirtualMFADevice, want string) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.Name+" terminal", func() error {
			got := &iamv1.VirtualMFADevice{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
				return err
			}
			if _, why := synced(got.Status.Conditions); !strings.HasPrefix(why, "terminal: ") || !strings.Contains(why, want) {
				return fmt.Errorf("%s", why)
			}
			return nil
		})
	}
	secretOf := func(t *testing.T, name string) *corev1.Secret {
		t.Helper()
		s := &corev1.Secret{}
		harness.Eventually(t, 60*time.Second, "Secret "+name, func() error {
			return env.Client.Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, s)
		})
		return s
	}
	secretGone := func(t *testing.T, name string) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, "Secret "+name+" gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, &corev1.Secret{})
			if err == nil {
				return fmt.Errorf("still present")
			}
			return client.IgnoreNotFound(err)
		})
	}
	gone := func(t *testing.T, obj client.Object) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.GetName()+" gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &iamv1.VirtualMFADevice{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}
	logCount := func(name, what string) int {
		n := 0
		for _, line := range strings.Split(env.Logs(), "\n") {
			if strings.Contains(line, what) && strings.Contains(line, `"name": "`+name+`"`) {
				n++
			}
		}
		return n
	}
	// nowhereBut checks the seed is in no log line, in nothing of the
	// object's (status included), and in no event in the namespace.
	nowhereBut := func(t *testing.T, seed string, obj *iamv1.VirtualMFADevice) {
		t.Helper()
		if len(seed) < 16 {
			t.Fatalf("seed of %d characters", len(seed))
		}
		if strings.Contains(env.Logs(), seed) {
			t.Fatal("the seed is in the manager's log")
		}
		raw, err := json.Marshal(get(t, obj))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), seed) {
			t.Fatal("the seed is in the object")
		}
		events := &corev1.EventList{}
		if err := env.Client.List(ctx, events, client.InNamespace("default")); err != nil {
			t.Fatal(err)
		}
		for _, e := range events.Items {
			if strings.Contains(e.Message, seed) {
				t.Fatalf("the seed is in event %s", e.Name)
			}
		}
	}

	breakGlass := &iamv1.VirtualMFADevice{
		ObjectMeta: metav1.ObjectMeta{Name: "break-glass", Namespace: "default"},
		Spec:       iamv1.VirtualMFADeviceSpec{Name: aws.String("break-glass"), Path: aws.String("/platform/")},
	}
	serial := "arn:aws:iam::" + env.Account + ":mfa/platform/break-glass"
	var seed, version string

	t.Run("a device is made, its seed in a Secret the object owns, found past the list's first page, and settles", func(t *testing.T) {
		if err := env.Client.Create(ctx, breakGlass); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t, breakGlass)
		if s := aws.ToString(got.Status.SerialNumber); s != serial {
			t.Fatalf("status.serialNumber %q", s)
		}
		if d := devices(t); len(d) != 1 || !d[serial] {
			t.Fatalf("devices %v", d)
		}
		s := secretOf(t, "break-glass") // the object's name: spec.secretName is unset
		seed, version = string(s.Data["base32StringSeed"]), s.ResourceVersion
		if string(s.Data["serialNumber"]) != serial || len(s.Data) != 3 || len(s.Data["qrCodePNG"]) == 0 {
			t.Fatalf("Secret data keys %d, serial %q, QR code %d bytes", len(s.Data), s.Data["serialNumber"], len(s.Data["qrCodePNG"]))
		}
		if _, err := base32.StdEncoding.DecodeString(seed); err != nil {
			t.Fatalf("the seed is not Base32 text: %v", err)
		}
		if len(s.OwnerReferences) != 1 || s.OwnerReferences[0].UID != got.UID || s.OwnerReferences[0].Kind != "VirtualMFADevice" {
			t.Fatalf("Secret owners %+v", s.OwnerReferences)
		}
		if s.Immutable == nil || !*s.Immutable {
			t.Fatal("Secret is not immutable")
		}
		creates := logCount("break-glass", ">>> rm.sdkCreate")
		time.Sleep(15 * time.Second)
		if c := logCount("break-glass", ">>> rm.sdkCreate"); c != creates {
			t.Fatalf("across resyncs: %d more creates", c-creates)
		}
		if d := devices(t); len(d) != 1 {
			t.Fatalf("devices after resyncs %v", d)
		}
		if s := secretOf(t, "break-glass"); s.ResourceVersion != version {
			t.Fatal("the Secret was written again")
		}
		nowhereBut(t, seed, breakGlass)
	})

	t.Run("a status that lost the serial number takes it back from the Secret, with no second device", func(t *testing.T) {
		harness.Eventually(t, 30*time.Second, "status cleared", func() error {
			got := get(t, breakGlass)
			got.Status.SerialNumber = nil
			return env.Client.Status().Update(ctx, got)
		})
		harness.Eventually(t, 60*time.Second, "the serial number back", func() error {
			if s := aws.ToString(get(t, breakGlass).Status.SerialNumber); s != serial {
				return fmt.Errorf("status serial %q", s)
			}
			return nil
		})
		waitSynced(t, breakGlass)
		if d := devices(t); len(d) != 1 || !d[serial] {
			t.Fatalf("devices %v", d)
		}
		if s := secretOf(t, "break-glass"); s.ResourceVersion != version {
			t.Fatal("the Secret was written again")
		}
	})

	t.Run("the Secret's name, defaulted into the spec, cannot change", func(t *testing.T) {
		harness.Eventually(t, 30*time.Second, "spec.secretName defaulted", func() error {
			if n := aws.ToString(get(t, breakGlass).Spec.SecretName); n != "break-glass" {
				return fmt.Errorf("spec.secretName %q", n)
			}
			return nil
		})
		harness.Eventually(t, 30*time.Second, "the CRD's immutability refusal", func() error {
			got := get(t, breakGlass)
			got.Spec.SecretName = aws.String("elsewhere")
			err := env.Client.Update(ctx, got)
			if err == nil {
				t.Fatal("secretName changed")
			}
			if !strings.Contains(err.Error(), "immutable") {
				return err
			}
			return nil
		})
	})

	foreign := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: "default"},
		StringData: map[string]string{"setting": "kept"},
	}
	clash := &iamv1.VirtualMFADevice{
		ObjectMeta: metav1.ObjectMeta{Name: "clash", Namespace: "default"},
		Spec:       iamv1.VirtualMFADeviceSpec{Name: aws.String("clash"), SecretName: aws.String("app-config")},
	}

	t.Run("a Secret that is not the object's is never written over, and no device is made", func(t *testing.T) {
		if err := env.Client.Create(ctx, foreign); err != nil {
			t.Fatal(err)
		}
		before := secretOf(t, "app-config").ResourceVersion
		if err := env.Client.Create(ctx, clash); err != nil {
			t.Fatal(err)
		}
		waitTerminal(t, clash, "is not this VirtualMFADevice's")
		if d := devices(t); len(d) != 1 {
			t.Fatalf("devices %v", d)
		}
		if s := secretOf(t, "app-config"); s.ResourceVersion != before || string(s.Data["setting"]) != "kept" || len(s.Data) != 1 {
			t.Fatal("the foreign Secret was changed")
		}
		if err := env.Client.Delete(ctx, clash); err != nil {
			t.Fatal(err)
		}
		gone(t, clash)
		if s := secretOf(t, "app-config"); s.ResourceVersion != before {
			t.Fatal("the foreign Secret was changed by the delete")
		}
	})

	ops := &iamv1.VirtualMFADevice{
		ObjectMeta: metav1.ObjectMeta{Name: "ops", Namespace: "default"},
		Spec:       iamv1.VirtualMFADeviceSpec{Name: aws.String("ops"), SecretName: aws.String("ops-mfa")},
	}

	t.Run("a device deleted outside Kubernetes is not made again over its Secret", func(t *testing.T) {
		if err := env.Client.Create(ctx, ops); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t, ops)
		opsSerial := aws.ToString(got.Status.SerialNumber)
		s := secretOf(t, "ops-mfa")
		opsSeed, opsVersion := string(s.Data["base32StringSeed"]), s.ResourceVersion
		if _, err := cloud.DeleteVirtualMFADevice(ctx, &iam.DeleteVirtualMFADeviceInput{SerialNumber: aws.String(opsSerial)}); err != nil {
			t.Fatal(err)
		}
		waitTerminal(t, ops, "which IAM no longer has")
		if d := devices(t); d[opsSerial] {
			t.Fatalf("devices %v", d)
		}
		if s := secretOf(t, "ops-mfa"); s.ResourceVersion != opsVersion {
			t.Fatal("the Secret was written over")
		}
		nowhereBut(t, opsSeed, ops)
		if err := env.Client.Delete(ctx, ops); err != nil {
			t.Fatal(err)
		}
		gone(t, ops)
	})

	t.Run("delete deletes the device and its Secret", func(t *testing.T) {
		if err := env.Client.Delete(ctx, breakGlass); err != nil {
			t.Fatal(err)
		}
		gone(t, breakGlass)
		if d := devices(t); len(d) != 0 {
			t.Fatalf("devices %v", d)
		}
		secretGone(t, "break-glass")
		if strings.Contains(env.Logs(), seed) {
			t.Fatal("the seed is in the manager's log")
		}
	})
}
