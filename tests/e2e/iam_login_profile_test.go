// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	iamv1 "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

// LoginProfile: an IAM user's console password, from a Kubernetes Secret.
// Terraform's aws_iam_user_login_profile. ACK's iam-controller (v1.11.3)
// has none; its PR #147 was closed.
func TestLoginProfile(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = iamv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	env := harness.Start(t, "iam", scheme)
	cloud := iam.NewFromConfig(env.AWS)
	ctx := context.Background()

	for _, u := range []string{"carol", "dave"} {
		if _, err := cloud.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(u)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := env.Client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "console", Namespace: "default"},
		StringData: map[string]string{"password": "not-a-real-password-1!"},
	}); err != nil {
		t.Fatal(err)
	}
	password := &ackv1alpha1.SecretKeyReference{SecretReference: corev1.SecretReference{Name: "console"}, Key: "password"}

	waitSynced := func(t *testing.T, obj *iamv1.LoginProfile) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.Name+" synced", func() error {
			got := &iamv1.LoginProfile{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
				return err
			}
			if ok, why := synced(got.Status.Conditions); !ok {
				return fmt.Errorf("not synced: %s", why)
			}
			return nil
		})
	}
	gone := func(t *testing.T, obj client.Object) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.GetName()+" gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &iamv1.LoginProfile{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}
	profile := func(user string) (*iamtypes.LoginProfile, error) {
		out, err := cloud.GetLoginProfile(ctx, &iam.GetLoginProfileInput{UserName: aws.String(user)})
		if err != nil {
			return nil, err
		}
		return out.LoginProfile, nil
	}

	carol := &iamv1.LoginProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "carol", Namespace: "default"},
		Spec:       iamv1.LoginProfileSpec{UserName: aws.String("carol"), Password: password},
	}

	t.Run("a login profile is made and settles", func(t *testing.T) {
		if err := env.Client.Create(ctx, carol); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, carol)
		if _, err := profile("carol"); err != nil {
			t.Fatalf("login profile in the cloud: %v", err)
		}
		updates := func() int {
			n := 0
			for _, line := range strings.Split(env.Logs(), "\n") {
				if strings.Contains(line, ">>> rm.sdkUpdate") && strings.Contains(line, `"name": "carol"`) {
					n++
				}
			}
			return n
		}
		before := updates()
		time.Sleep(15 * time.Second)
		if n := updates(); n != before {
			t.Fatalf("updated %d times across resyncs: IAM's defaults or the unreturned password read as drift", n-before)
		}
	})

	t.Run("passwordResetRequired is updated", func(t *testing.T) {
		harness.Eventually(t, 30*time.Second, "spec updated", func() error {
			got := &iamv1.LoginProfile{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(carol), got); err != nil {
				return err
			}
			got.Spec.PasswordResetRequired = aws.Bool(true)
			return env.Client.Update(ctx, got)
		})
		harness.Eventually(t, 60*time.Second, "reset required in the cloud", func() error {
			p, err := profile("carol")
			if err != nil {
				return err
			}
			if !p.PasswordResetRequired {
				return fmt.Errorf("passwordResetRequired false")
			}
			return nil
		})
	})

	t.Run("the user cannot change", func(t *testing.T) {
		got := &iamv1.LoginProfile{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(carol), got); err != nil {
			t.Fatal(err)
		}
		got.Spec.UserName = aws.String("dave")
		err := env.Client.Update(ctx, got)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("changing userName: got %v, want the CRD's immutability refusal", err)
		}
	})

	t.Run("deletion-policy retain leaves the profile", func(t *testing.T) {
		keep := &iamv1.LoginProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: "dave", Namespace: "default",
				Annotations: map[string]string{ackv1alpha1.AnnotationDeletionPolicy: string(ackv1alpha1.DeletionPolicyRetain)},
			},
			Spec: iamv1.LoginProfileSpec{UserName: aws.String("dave"), Password: password},
		}
		if err := env.Client.Create(ctx, keep); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, keep)
		if err := env.Client.Delete(ctx, keep); err != nil {
			t.Fatal(err)
		}
		gone(t, keep)
		if _, err := profile("dave"); err != nil {
			t.Fatalf("retained profile: %v", err)
		}
	})

	t.Run("delete deletes the profile", func(t *testing.T) {
		if err := env.Client.Delete(ctx, carol); err != nil {
			t.Fatal(err)
		}
		gone(t, carol)
		_, err := profile("carol")
		var nse *iamtypes.NoSuchEntityException
		if !errors.As(err, &nse) {
			t.Fatalf("profile still there, or another error: %v", err)
		}
	})
}
