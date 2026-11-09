// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	iamv1 "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

// AccountAlias: the account's sign-in alias, Terraform's
// aws_iam_account_alias. ACK's iam-controller (v1.11.3) has none.
func TestAccountAlias(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = iamv1.AddToScheme(scheme)
	env := harness.Start(t, "iam", scheme)
	cloud := iam.NewFromConfig(env.AWS)
	ctx := context.Background()

	alias := func() (string, error) {
		out, err := cloud.ListAccountAliases(ctx, &iam.ListAccountAliasesInput{})
		if err != nil {
			return "", err
		}
		return strings.Join(out.AccountAliases, ","), nil
	}
	cloudAlias := func(t *testing.T, want string) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, "alias "+want, func() error {
			got, err := alias()
			if err != nil {
				return err
			}
			if got != want {
				return fmt.Errorf("alias %q", got)
			}
			return nil
		})
	}
	waitSynced := func(t *testing.T, obj *iamv1.AccountAlias) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.Name+" synced", func() error {
			got := &iamv1.AccountAlias{}
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
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &iamv1.AccountAlias{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}

	acme := &iamv1.AccountAlias{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-prod", Namespace: "default"},
		Spec:       iamv1.AccountAliasSpec{AccountAlias: aws.String("acme-prod")},
	}

	t.Run("the alias is set and settles", func(t *testing.T) {
		if err := env.Client.Create(ctx, acme); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, acme)
		cloudAlias(t, "acme-prod")
		updates := func() int {
			n := 0
			for _, line := range strings.Split(env.Logs(), "\n") {
				if strings.Contains(line, ">>> rm.sdkUpdate") && strings.Contains(line, `"name": "acme-prod"`) {
					n++
				}
			}
			return n
		}
		before := updates()
		time.Sleep(15 * time.Second)
		if n := updates(); n != before {
			t.Fatalf("updated %d times across resyncs", n-before)
		}
	})

	t.Run("an alias set outside Kubernetes is put back", func(t *testing.T) {
		if _, err := cloud.CreateAccountAlias(ctx, &iam.CreateAccountAliasInput{AccountAlias: aws.String("someone-else")}); err != nil {
			t.Fatal(err)
		}
		cloudAlias(t, "acme-prod")
	})

	t.Run("the CRD refuses what IAM would", func(t *testing.T) {
		bad := &iamv1.AccountAlias{
			ObjectMeta: metav1.ObjectMeta{Name: "bad", Namespace: "default"},
			Spec:       iamv1.AccountAliasSpec{AccountAlias: aws.String("Acme_Prod")},
		}
		if err := env.Client.Create(ctx, bad); err == nil {
			t.Fatal("an alias with capitals and an underscore was accepted")
		}
	})

	t.Run("the alias cannot change", func(t *testing.T) {
		got := &iamv1.AccountAlias{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(acme), got); err != nil {
			t.Fatal(err)
		}
		got.Spec.AccountAlias = aws.String("acme-staging")
		err := env.Client.Update(ctx, got)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("changing accountAlias: got %v, want the CRD's immutability refusal", err)
		}
	})

	t.Run("deletion-policy retain leaves the alias", func(t *testing.T) {
		keep := acme.DeepCopy()
		harness.Eventually(t, 30*time.Second, "annotated", func() error {
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(acme), keep); err != nil {
				return err
			}
			if keep.Annotations == nil {
				keep.Annotations = map[string]string{}
			}
			keep.Annotations[ackv1alpha1.AnnotationDeletionPolicy] = string(ackv1alpha1.DeletionPolicyRetain)
			return env.Client.Update(ctx, keep)
		})
		// Let the controller see the annotation before the delete.
		time.Sleep(10 * time.Second)
		if err := env.Client.Delete(ctx, keep); err != nil {
			t.Fatal(err)
		}
		gone(t, keep)
		cloudAlias(t, "acme-prod")
	})

	t.Run("delete deletes the alias", func(t *testing.T) {
		again := &iamv1.AccountAlias{
			ObjectMeta: metav1.ObjectMeta{Name: "acme-prod-2", Namespace: "default"},
			Spec:       iamv1.AccountAliasSpec{AccountAlias: aws.String("acme-prod")},
		}
		if err := env.Client.Create(ctx, again); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, again)
		if err := env.Client.Delete(ctx, again); err != nil {
			t.Fatal(err)
		}
		gone(t, again)
		cloudAlias(t, "")
	})
}
