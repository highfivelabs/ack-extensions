// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	ackkms "github.com/aws-controllers-k8s/kms-controller/apis/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	"github.com/highfivelabs/ack-extensions/services/kms/pkg/extend/key"
)

// ackKMSController is ACK's own kms-controller, built from the Go module
// go.mod pins (UPSTREAM.md), to run ACK's side of the Key extension.
const ackKMSController = "github.com/aws-controllers-k8s/kms-controller/cmd/controller"

// eventReasons are the reasons of the Events recorded on an object.
func eventReasons(ctx context.Context, c client.Client, ns, name string) (map[string]string, error) {
	events := &corev1.EventList{}
	if err := c.List(ctx, events, client.InNamespace(ns)); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range events.Items {
		if e.InvolvedObject.Name == name {
			out[e.Reason] = e.Message
		}
	}
	return out, nil
}

func waitEvent(t *testing.T, env *harness.Env, ns, name, reason string) string {
	t.Helper()
	var msg string
	harness.Eventually(t, 60*time.Second, name+" event "+reason, func() error {
		got, err := eventReasons(context.Background(), env.Client, ns, name)
		if err != nil {
			return err
		}
		m, ok := got[reason]
		if !ok {
			return fmt.Errorf("events %v", got)
		}
		msg = m
		return nil
	})
	return msg
}

// The Key extension (docs/extensions.md): ACK's own kms-controller makes and
// deletes the key from ACK's Key; our kms manager, beside it, sets what
// ACK's Key has no field for, from annotations in our group.
//
// moto takes EnableKeyRotation's RotationPeriodInDays and never returns it
// (GetKeyRotationStatus has no period; docs/plan.md section 4.4), so the
// period is shown here by the manager's Event and, for the request's
// shape, by the unit tests of services/kms/pkg/extend/key.
func TestKeyExtension(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = ackkms.AddToScheme(scheme)
	env, _ := harness.StartControllers(t, "kms", scheme,
		harness.Controller{},
		harness.Controller{Main: ackKMSController},
	)
	ctx := context.Background()
	api := kmsIn(env, harness.Region)

	waitKeyID := func(t *testing.T, name string) string {
		t.Helper()
		var id string
		harness.Eventually(t, 90*time.Second, name+" made by ACK", func() error {
			k := &ackkms.Key{}
			if err := env.Client.Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, k); err != nil {
				return err
			}
			if k.Status.KeyID == nil {
				return fmt.Errorf("no status.keyID yet")
			}
			id = *k.Status.KeyID
			return nil
		})
		return id
	}
	waitState := func(t *testing.T, id string, want kmstypes.KeyState) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, "key "+string(want), func() error {
			st, err := keyState(ctx, api, id)
			if err != nil {
				return err
			}
			if st != want {
				return fmt.Errorf("key is %s", st)
			}
			return nil
		})
	}
	annotate := func(t *testing.T, name string, set map[string]string) {
		t.Helper()
		harness.Eventually(t, 30*time.Second, "annotate "+name, func() error {
			k := &ackkms.Key{}
			if err := env.Client.Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, k); err != nil {
				return err
			}
			if k.Annotations == nil {
				k.Annotations = map[string]string{}
			}
			for a, v := range set {
				if v == "" {
					delete(k.Annotations, a)
				} else {
					k.Annotations[a] = v
				}
			}
			return env.Client.Update(ctx, k)
		})
	}

	k := &ackkms.Key{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default", Annotations: map[string]string{
			key.AnnotationRotationPeriod: "180",
			// ACK's own deletion window, which ScheduleKeyDeletion takes.
			ackkms.AnnotationDeletePendingWindow: "30",
		}},
		Spec: ackkms.KeySpec{Description: aws.String("app key"), EnableKeyRotation: aws.Bool(true)},
	}
	if err := env.Client.Create(ctx, k); err != nil {
		t.Fatal(err)
	}
	id := waitKeyID(t, "app")

	t.Run("the rotation period is set once ACK has made the key", func(t *testing.T) {
		msg := waitEvent(t, env, "default", "app", key.ReasonRotationPeriodSet)
		if !strings.Contains(msg, "180 days") {
			t.Fatalf("event %q", msg)
		}
		st, err := api.GetKeyRotationStatus(ctx, &kms.GetKeyRotationStatusInput{KeyId: aws.String(id)})
		if err != nil {
			t.Fatal(err)
		}
		if !st.KeyRotationEnabled {
			t.Fatal("rotation is off")
		}
	})

	t.Run("enabled: false disables the key, true enables it", func(t *testing.T) {
		annotate(t, "app", map[string]string{key.AnnotationEnabled: "false"})
		waitState(t, id, kmstypes.KeyStateDisabled)
		waitEvent(t, env, "default", "app", key.ReasonKeyDisabled)
		annotate(t, "app", map[string]string{key.AnnotationEnabled: "true"})
		waitState(t, id, kmstypes.KeyStateEnabled)
		waitEvent(t, env, "default", "app", key.ReasonKeyEnabled)
	})

	t.Run("a key disabled outside Kubernetes is enabled again", func(t *testing.T) {
		if _, err := api.DisableKey(ctx, &kms.DisableKeyInput{KeyId: aws.String(id)}); err != nil {
			t.Fatal(err)
		}
		// The harness gives the managers a five-second resync.
		waitState(t, id, kmstypes.KeyStateEnabled)
	})

	t.Run("an annotation that is not valid is an Event, and nothing changes", func(t *testing.T) {
		annotate(t, "app", map[string]string{key.AnnotationRotationPeriod: "30", key.AnnotationEnabled: ""})
		msg := waitEvent(t, env, "default", "app", key.ReasonInvalidAnnotation)
		if !strings.Contains(msg, "outside 90 to 2560") {
			t.Fatalf("event %q", msg)
		}
		if st, err := keyState(ctx, api, id); err != nil || st != kmstypes.KeyStateEnabled {
			t.Fatalf("key is %s (%v)", st, err)
		}
	})

	t.Run("a rotation period without enableKeyRotation is refused as an Event", func(t *testing.T) {
		other := &ackkms.Key{
			ObjectMeta: metav1.ObjectMeta{Name: "no-rotation", Namespace: "default", Annotations: map[string]string{key.AnnotationRotationPeriod: "365"}},
			Spec:       ackkms.KeySpec{Description: aws.String("no rotation")},
		}
		if err := env.Client.Create(ctx, other); err != nil {
			t.Fatal(err)
		}
		otherID := waitKeyID(t, "no-rotation")
		msg := waitEvent(t, env, "default", "no-rotation", key.ReasonRotationPeriodIgnored)
		if !strings.Contains(msg, "enableKeyRotation") {
			t.Fatalf("event %q", msg)
		}
		st, err := api.GetKeyRotationStatus(ctx, &kms.GetKeyRotationStatusInput{KeyId: aws.String(otherID)})
		if err != nil {
			t.Fatal(err)
		}
		if st.KeyRotationEnabled {
			t.Fatal("rotation was turned on without spec.enableKeyRotation")
		}
	})

	t.Run("delete schedules the deletion with ACK's window", func(t *testing.T) {
		if err := env.Client.Delete(ctx, k); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "key pending deletion", func() error {
			out, err := api.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(id)})
			if err != nil {
				return err
			}
			if out.KeyMetadata.KeyState != kmstypes.KeyStatePendingDeletion {
				return fmt.Errorf("key is %s", out.KeyMetadata.KeyState)
			}
			if d := out.KeyMetadata.DeletionDate; d == nil || d.Before(time.Now().Add(29*24*time.Hour)) {
				return fmt.Errorf("deletion date %v, want about 30 days from now", d)
			}
			return nil
		})
	})
}
