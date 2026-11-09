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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	iamv1 "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

// AccountPasswordPolicy: the account's password policy for IAM users,
// Terraform's aws_iam_account_password_policy. ACK's iam-controller
// (v1.11.3) has none.
func TestAccountPasswordPolicy(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = iamv1.AddToScheme(scheme)
	env := harness.Start(t, "iam", scheme)
	cloud := iam.NewFromConfig(env.AWS)
	ctx := context.Background()

	policy := func() (*iamtypes.PasswordPolicy, error) {
		out, err := cloud.GetAccountPasswordPolicy(ctx, &iam.GetAccountPasswordPolicyInput{})
		if err != nil {
			return nil, err
		}
		return out.PasswordPolicy, nil
	}
	minLength := func(t *testing.T, want int32) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, fmt.Sprintf("minimum length %d", want), func() error {
			p, err := policy()
			if err != nil {
				return err
			}
			if aws.ToInt32(p.MinimumPasswordLength) != want {
				return fmt.Errorf("minimum length %d", aws.ToInt32(p.MinimumPasswordLength))
			}
			return nil
		})
	}
	waitSynced := func(t *testing.T, obj *iamv1.AccountPasswordPolicy) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.Name+" synced", func() error {
			got := &iamv1.AccountPasswordPolicy{}
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
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &iamv1.AccountPasswordPolicy{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}
	update := func(t *testing.T, obj *iamv1.AccountPasswordPolicy, change func(*iamv1.AccountPasswordPolicy)) {
		t.Helper()
		harness.Eventually(t, 30*time.Second, obj.Name+" updated", func() error {
			got := &iamv1.AccountPasswordPolicy{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
				return err
			}
			change(got)
			return env.Client.Update(ctx, got)
		})
	}

	strict := &iamv1.AccountPasswordPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "strict", Namespace: "default"},
		Spec: iamv1.AccountPasswordPolicySpec{
			MinimumPasswordLength: aws.Int64(14), RequireSymbols: aws.Bool(true), MaxPasswordAge: aws.Int64(90),
			AllowUsersToChangePassword: aws.Bool(true),
		},
	}

	t.Run("the policy is set, IAM's defaults are no drift, and it settles", func(t *testing.T) {
		if err := env.Client.Create(ctx, strict); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, strict)
		p, err := policy()
		if err != nil {
			t.Fatal(err)
		}
		if aws.ToInt32(p.MinimumPasswordLength) != 14 || !p.RequireSymbols || aws.ToInt32(p.MaxPasswordAge) != 90 || !p.AllowUsersToChangePassword {
			t.Fatalf("cloud policy %+v", p)
		}
		updates := func() int {
			n := 0
			for _, line := range strings.Split(env.Logs(), "\n") {
				if strings.Contains(line, ">>> rm.sdkUpdate") && strings.Contains(line, `"name": "strict"`) {
					n++
				}
			}
			return n
		}
		before := updates()
		time.Sleep(15 * time.Second)
		if n := updates(); n != before {
			t.Fatalf("updated %d times across resyncs: IAM's defaults read as drift", n-before)
		}
	})

	t.Run("a changed length is put", func(t *testing.T) {
		update(t, strict, func(o *iamv1.AccountPasswordPolicy) { o.Spec.MinimumPasswordLength = aws.Int64(16) })
		minLength(t, 16)
	})

	t.Run("a change outside Kubernetes is put back", func(t *testing.T) {
		if _, err := cloud.UpdateAccountPasswordPolicy(ctx, &iam.UpdateAccountPasswordPolicyInput{MinimumPasswordLength: aws.Int32(8)}); err != nil {
			t.Fatal(err)
		}
		minLength(t, 16)
	})

	t.Run("the CRD refuses what IAM would", func(t *testing.T) {
		bad := &iamv1.AccountPasswordPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "bad", Namespace: "default"},
			Spec:       iamv1.AccountPasswordPolicySpec{MinimumPasswordLength: aws.Int64(4)},
		}
		if err := env.Client.Create(ctx, bad); err == nil {
			t.Fatal("a minimum length of 4 was accepted")
		}
	})

	t.Run("deletion-policy retain leaves the policy", func(t *testing.T) {
		update(t, strict, func(o *iamv1.AccountPasswordPolicy) {
			if o.Annotations == nil {
				o.Annotations = map[string]string{}
			}
			o.Annotations[ackv1alpha1.AnnotationDeletionPolicy] = string(ackv1alpha1.DeletionPolicyRetain)
		})
		// Let the controller see the annotation before the delete.
		time.Sleep(10 * time.Second)
		if err := env.Client.Delete(ctx, strict); err != nil {
			t.Fatal(err)
		}
		gone(t, strict)
		minLength(t, 16)
	})

	t.Run("delete deletes the policy", func(t *testing.T) {
		again := &iamv1.AccountPasswordPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "strict-2", Namespace: "default"},
			Spec:       iamv1.AccountPasswordPolicySpec{MinimumPasswordLength: aws.Int64(12)},
		}
		if err := env.Client.Create(ctx, again); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, again)
		minLength(t, 12)
		if err := env.Client.Delete(ctx, again); err != nil {
			t.Fatal(err)
		}
		gone(t, again)
		_, err := policy()
		var nse *iamtypes.NoSuchEntityException
		if !errors.As(err, &nse) {
			t.Fatalf("policy still there, or another error: %v", err)
		}
	})
}
