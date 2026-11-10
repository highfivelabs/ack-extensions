// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	ackiam "github.com/aws-controllers-k8s/iam-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	iamv1 "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

// AccessKey: an IAM user's access key, its secret written once into a
// Kubernetes Secret owned by the object, and never into a log, the status
// or an event. ACK's iam-controller (v1.11.3) ignores AccessKey.
func TestIAMAccessKey(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = iamv1.AddToScheme(scheme)
	_ = ackiam.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	env := harness.Start(t, "iam", scheme)
	cloud := iam.NewFromConfig(env.AWS)
	ctx := context.Background()

	for _, u := range []string{"ci", "ops"} {
		if _, err := cloud.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(u)}); err != nil {
			t.Fatal(err)
		}
	}
	// ACK's User, as ACK's iam-controller would leave it once synced (that
	// controller does not run here).
	ackUser := &ackiam.User{
		ObjectMeta: metav1.ObjectMeta{Name: "ci", Namespace: "default"},
		Spec:       ackiam.UserSpec{Name: aws.String("ci")},
	}
	if err := env.Client.Create(ctx, ackUser); err != nil {
		t.Fatal(err)
	}
	ackUser.Status.Conditions = []*ackv1alpha1.Condition{{Type: ackv1alpha1.ConditionTypeResourceSynced, Status: corev1.ConditionTrue}}
	if err := env.Client.Status().Update(ctx, ackUser); err != nil {
		t.Fatal(err)
	}

	keysOf := func(t *testing.T, user string) []string {
		t.Helper()
		out, err := cloud.ListAccessKeys(ctx, &iam.ListAccessKeysInput{UserName: aws.String(user)})
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, k := range out.AccessKeyMetadata {
			ids = append(ids, aws.ToString(k.AccessKeyId))
		}
		return ids
	}
	get := func(t *testing.T, obj *iamv1.AccessKey) *iamv1.AccessKey {
		t.Helper()
		got := &iamv1.AccessKey{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	waitSynced := func(t *testing.T, obj *iamv1.AccessKey) *iamv1.AccessKey {
		t.Helper()
		got := &iamv1.AccessKey{}
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
	waitTerminal := func(t *testing.T, obj *iamv1.AccessKey, want string) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.Name+" terminal", func() error {
			got := &iamv1.AccessKey{}
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
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &iamv1.AccessKey{})
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
	// nowhereBut checks the secret is in no log line, in nothing of the
	// object's (status included), and in no event in the namespace.
	nowhereBut := func(t *testing.T, secret string, obj *iamv1.AccessKey) {
		t.Helper()
		if secret == "" {
			t.Fatal("empty secret")
		}
		if strings.Contains(env.Logs(), secret) {
			t.Fatal("the secret access key is in the manager's log")
		}
		raw, err := json.Marshal(get(t, obj))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), secret) {
			t.Fatal("the secret access key is in the object")
		}
		events := &corev1.EventList{}
		if err := env.Client.List(ctx, events, client.InNamespace("default")); err != nil {
			t.Fatal(err)
		}
		for _, e := range events.Items {
			if strings.Contains(e.Message, secret) {
				t.Fatalf("the secret access key is in event %s", e.Name)
			}
		}
	}

	byRef := &iamv1.AccessKey{
		ObjectMeta: metav1.ObjectMeta{Name: "ci-deploy", Namespace: "default"},
		Spec: iamv1.AccessKeySpec{
			UserRef: &ackv1alpha1.AWSResourceReferenceWrapper{From: &ackv1alpha1.AWSResourceReference{Name: aws.String("ci")}},
		},
	}
	var keyID, secret, version string

	t.Run("a key is made for ACK's User, its secret in a Secret the object owns, and settles", func(t *testing.T) {
		if err := env.Client.Create(ctx, byRef); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t, byRef)
		keyID = aws.ToString(got.Status.AccessKeyID)
		if ids := keysOf(t, "ci"); len(ids) != 1 || ids[0] != keyID {
			t.Fatalf("ci's keys %v, status %q", ids, keyID)
		}
		if aws.ToString(got.Status.Status) != "Active" {
			t.Fatalf("status.status %q", aws.ToString(got.Status.Status))
		}
		s := secretOf(t, "ci-deploy") // the object's name: spec.secretName is unset
		secret, version = string(s.Data["AWS_SECRET_ACCESS_KEY"]), s.ResourceVersion
		if string(s.Data["AWS_ACCESS_KEY_ID"]) != keyID || secret == "" || len(s.Data) != 2 {
			t.Fatalf("Secret data keys %d, id %q", len(s.Data), s.Data["AWS_ACCESS_KEY_ID"])
		}
		if len(s.OwnerReferences) != 1 || s.OwnerReferences[0].UID != got.UID || s.OwnerReferences[0].Kind != "AccessKey" {
			t.Fatalf("Secret owners %+v", s.OwnerReferences)
		}
		if s.Immutable == nil || !*s.Immutable {
			t.Fatal("Secret is not immutable")
		}
		creates := logCount("ci-deploy", ">>> rm.sdkCreate")
		time.Sleep(15 * time.Second)
		if c := logCount("ci-deploy", ">>> rm.sdkCreate"); c != creates {
			t.Fatalf("across resyncs: %d more creates", c-creates)
		}
		if ids := keysOf(t, "ci"); len(ids) != 1 {
			t.Fatalf("ci's keys after resyncs %v", ids)
		}
		if s := secretOf(t, "ci-deploy"); s.ResourceVersion != version {
			t.Fatal("the Secret was written again")
		}
		nowhereBut(t, secret, byRef)
	})

	t.Run("a status that lost the key's ID takes it back from the Secret, with no second key", func(t *testing.T) {
		harness.Eventually(t, 30*time.Second, "status cleared", func() error {
			got := get(t, byRef)
			got.Status.AccessKeyID = nil
			return env.Client.Status().Update(ctx, got)
		})
		harness.Eventually(t, 60*time.Second, "the ID back", func() error {
			if id := aws.ToString(get(t, byRef).Status.AccessKeyID); id != keyID {
				return fmt.Errorf("status ID %q", id)
			}
			return nil
		})
		waitSynced(t, byRef)
		if ids := keysOf(t, "ci"); len(ids) != 1 || ids[0] != keyID {
			t.Fatalf("ci's keys %v", ids)
		}
		if s := secretOf(t, "ci-deploy"); s.ResourceVersion != version {
			t.Fatal("the Secret was written again")
		}
	})

	t.Run("the user and the Secret's name cannot change", func(t *testing.T) {
		got := get(t, byRef)
		got.Spec.SecretName = aws.String("elsewhere")
		if err := env.Client.Update(ctx, got); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("secretName changed: %v", err)
		}
	})

	foreign := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: "default"},
		StringData: map[string]string{"setting": "kept"},
	}
	clash := &iamv1.AccessKey{
		ObjectMeta: metav1.ObjectMeta{Name: "ops-clash", Namespace: "default"},
		Spec:       iamv1.AccessKeySpec{UserName: aws.String("ops"), SecretName: aws.String("app-config")},
	}

	t.Run("a Secret that is not the object's is never written over, and no key is made", func(t *testing.T) {
		if err := env.Client.Create(ctx, foreign); err != nil {
			t.Fatal(err)
		}
		before := secretOf(t, "app-config").ResourceVersion
		if err := env.Client.Create(ctx, clash); err != nil {
			t.Fatal(err)
		}
		waitTerminal(t, clash, "is not this AccessKey's")
		if ids := keysOf(t, "ops"); len(ids) != 0 {
			t.Fatalf("ops has keys %v", ids)
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

	byName := &iamv1.AccessKey{
		ObjectMeta: metav1.ObjectMeta{Name: "ops", Namespace: "default"},
		Spec:       iamv1.AccessKeySpec{UserName: aws.String("ops"), SecretName: aws.String("ops-aws")},
	}

	t.Run("a key deleted outside Kubernetes is not made again over its Secret", func(t *testing.T) {
		if err := env.Client.Create(ctx, byName); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t, byName)
		id := aws.ToString(got.Status.AccessKeyID)
		s := secretOf(t, "ops-aws")
		opsSecret, opsVersion := string(s.Data["AWS_SECRET_ACCESS_KEY"]), s.ResourceVersion
		if _, err := cloud.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{UserName: aws.String("ops"), AccessKeyId: aws.String(id)}); err != nil {
			t.Fatal(err)
		}
		waitTerminal(t, byName, "which IAM no longer has")
		if ids := keysOf(t, "ops"); len(ids) != 0 {
			t.Fatalf("ops has keys %v", ids)
		}
		if s := secretOf(t, "ops-aws"); s.ResourceVersion != opsVersion {
			t.Fatal("the Secret was written over")
		}
		nowhereBut(t, opsSecret, byName)
		if err := env.Client.Delete(ctx, byName); err != nil {
			t.Fatal(err)
		}
		gone(t, byName)
	})

	t.Run("delete deletes the key and its Secret", func(t *testing.T) {
		if err := env.Client.Delete(ctx, byRef); err != nil {
			t.Fatal(err)
		}
		gone(t, byRef)
		if ids := keysOf(t, "ci"); len(ids) != 0 {
			t.Fatalf("ci's keys %v", ids)
		}
		secretGone(t, "ci-deploy")
		if strings.Contains(env.Logs(), secret) {
			t.Fatal("the secret access key is in the manager's log")
		}
	})
}
