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
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	kmsv1 "github.com/highfivelabs/ack-extensions/services/kms/apis/v1alpha1"
)

// kmsIn is a KMS client for one Region of the fake cloud.
func kmsIn(env *harness.Env, region string) *kms.Client {
	return kms.NewFromConfig(env.AWS, func(o *kms.Options) { o.Region = region })
}

func keyState(ctx context.Context, c *kms.Client, key string) (kmstypes.KeyState, error) {
	out, err := c.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(key)})
	if err != nil {
		return "", err
	}
	return out.KeyMetadata.KeyState, nil
}

func keyTags(ctx context.Context, c *kms.Client, key string) (map[string]string, error) {
	out, err := c.ListResourceTags(ctx, &kms.ListResourceTagsInput{KeyId: aws.String(key)})
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, t := range out.Tags {
		m[aws.ToString(t.TagKey)] = aws.ToString(t.TagValue)
	}
	return m, nil
}

// ReplicaKey: a multi-Region primary key's replica in another Region. The
// manager acts in us-west-2, the primary key's Region, where ReplicateKey
// must be called; the replica lives, and is read, changed and deleted, in
// its own Region. A replica shares its key ID with the primary key, so
// the test checks the primary key in us-west-2 after every step that
// changes the replica.
//
// moto ignores ReplicateKey's Tags, so the replica's tags are put by the
// update that follows the create (the read finds none); on AWS they are
// there from the start.
func TestReplicaKey(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = kmsv1.AddToScheme(scheme)
	_ = ackkms.AddToScheme(scheme)
	env := harness.Start(t, "kms", scheme)
	ctx := context.Background()
	primaryKMS, euKMS := kmsIn(env, "us-west-2"), kmsIn(env, "eu-west-1")

	primary, err := primaryKMS.CreateKey(ctx, &kms.CreateKeyInput{MultiRegion: aws.Bool(true), Description: aws.String("primary")})
	if err != nil {
		t.Fatal(err)
	}
	keyID := aws.ToString(primary.KeyMetadata.KeyId)

	primaryUntouched := func(t *testing.T) {
		t.Helper()
		st, err := keyState(ctx, primaryKMS, keyID)
		if err != nil {
			t.Fatal(err)
		}
		if st != kmstypes.KeyStateEnabled {
			t.Fatalf("the primary key in us-west-2 is %s, want Enabled", st)
		}
		tags, err := keyTags(ctx, primaryKMS, keyID)
		if err != nil {
			t.Fatal(err)
		}
		if len(tags) != 0 {
			t.Fatalf("the primary key in us-west-2 has the replica's tags %v", tags)
		}
	}
	waitSynced := func(t *testing.T, obj *kmsv1.ReplicaKey) *kmsv1.ReplicaKey {
		t.Helper()
		got := &kmsv1.ReplicaKey{}
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
	gone := func(t *testing.T, obj client.Object) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.GetName()+" gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &kmsv1.ReplicaKey{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}

	eu := &kmsv1.ReplicaKey{
		ObjectMeta: metav1.ObjectMeta{Name: "app-eu", Namespace: "default"},
		Spec: kmsv1.ReplicaKeySpec{
			KeyID: aws.String(keyID), ReplicaRegion: aws.String("eu-west-1"), Description: aws.String("app key, eu"),
			Tags: []*ackkms.Tag{{TagKey: aws.String("team"), TagValue: aws.String("payments")}},
		},
	}

	t.Run("a replica is made in its Region", func(t *testing.T) {
		if err := env.Client.Create(ctx, eu); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t, eu)
		md := got.Status.ReplicaKeyMetadata
		if md == nil || !strings.HasPrefix(aws.ToString(md.ARN), "arn:aws:kms:eu-west-1:") || aws.ToString(md.KeyState) != "Enabled" {
			t.Fatalf("status.replicaKeyMetadata = %+v", md)
		}
		out, err := euKMS.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(keyID)})
		if err != nil {
			t.Fatal(err)
		}
		if out.KeyMetadata.MultiRegionConfiguration.MultiRegionKeyType != kmstypes.MultiRegionKeyTypeReplica {
			t.Fatalf("eu-west-1 key is %s", out.KeyMetadata.MultiRegionConfiguration.MultiRegionKeyType)
		}
		harness.Eventually(t, 60*time.Second, "the replica's tags in eu-west-1", func() error {
			tags, err := keyTags(ctx, euKMS, keyID)
			if err != nil {
				return err
			}
			if tags["team"] != "payments" {
				return fmt.Errorf("tags %v", tags)
			}
			return nil
		})
		primaryUntouched(t)
	})

	t.Run("a policy is put on the replica only", func(t *testing.T) {
		obj := &kmsv1.ReplicaKey{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(eu), obj); err != nil {
			t.Fatal(err)
		}
		policy := `{"Version":"2012-10-17","Statement":[{"Sid":"root","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:root"},"Action":"kms:*","Resource":"*"}]}`
		obj.Spec.Policy = aws.String(policy)
		if err := env.Client.Update(ctx, obj); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "the policy in eu-west-1", func() error {
			out, err := euKMS.GetKeyPolicy(ctx, &kms.GetKeyPolicyInput{KeyId: aws.String(keyID), PolicyName: aws.String("default")})
			if err != nil {
				return err
			}
			if !strings.Contains(aws.ToString(out.Policy), `"Sid":"root"`) {
				return fmt.Errorf("replica policy %s", aws.ToString(out.Policy))
			}
			return nil
		})
		out, err := primaryKMS.GetKeyPolicy(ctx, &kms.GetKeyPolicyInput{KeyId: aws.String(keyID), PolicyName: aws.String("default")})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(aws.ToString(out.Policy), `"Sid":"root"`) {
			t.Fatalf("the primary key's policy changed: %s", aws.ToString(out.Policy))
		}
		waitSynced(t, eu)
		primaryUntouched(t)
	})

	t.Run("the replica Region cannot change", func(t *testing.T) {
		obj := &kmsv1.ReplicaKey{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(eu), obj); err != nil {
			t.Fatal(err)
		}
		obj.Spec.ReplicaRegion = aws.String("ap-south-1")
		err := env.Client.Update(ctx, obj)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("changing replicaRegion: got %v, want the CRD's immutability refusal", err)
		}
	})

	t.Run("references resolve to ACK's Key", func(t *testing.T) {
		other, err := primaryKMS.CreateKey(ctx, &kms.CreateKeyInput{MultiRegion: aws.Bool(true), Description: aws.String("reports")})
		if err != nil {
			t.Fatal(err)
		}
		// A Key as ACK's kms-controller leaves it once synced; the
		// controller does not run here.
		key := &ackkms.Key{
			ObjectMeta: metav1.ObjectMeta{Name: "reports", Namespace: "default"},
			Spec:       ackkms.KeySpec{MultiRegion: aws.Bool(true), Description: aws.String("reports")},
		}
		if err := env.Client.Create(ctx, key); err != nil {
			t.Fatal(err)
		}
		key.Status.KeyID = other.KeyMetadata.KeyId
		key.Status.Conditions = []*ackv1alpha1.Condition{{Type: ackv1alpha1.ConditionTypeResourceSynced, Status: corev1.ConditionTrue}}
		if err := env.Client.Status().Update(ctx, key); err != nil {
			t.Fatal(err)
		}
		byRef := &kmsv1.ReplicaKey{
			ObjectMeta: metav1.ObjectMeta{Name: "reports-south", Namespace: "default"},
			Spec: kmsv1.ReplicaKeySpec{
				KeyRef:        &ackv1alpha1.AWSResourceReferenceWrapper{From: &ackv1alpha1.AWSResourceReference{Name: aws.String("reports")}},
				ReplicaRegion: aws.String("ap-south-1"),
			},
		}
		if err := env.Client.Create(ctx, byRef); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, byRef)
		if st, err := keyState(ctx, kmsIn(env, "ap-south-1"), aws.ToString(other.KeyMetadata.KeyId)); err != nil || st != kmstypes.KeyStateEnabled {
			t.Fatalf("replica in ap-south-1: %s %v", st, err)
		}
	})

	t.Run("deletion-policy retain leaves the replica", func(t *testing.T) {
		keep := &kmsv1.ReplicaKey{
			ObjectMeta: metav1.ObjectMeta{
				Name: "app-east", Namespace: "default",
				Annotations: map[string]string{ackv1alpha1.AnnotationDeletionPolicy: string(ackv1alpha1.DeletionPolicyRetain)},
			},
			Spec: kmsv1.ReplicaKeySpec{KeyID: aws.String(keyID), ReplicaRegion: aws.String("us-east-1")},
		}
		if err := env.Client.Create(ctx, keep); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, keep)
		if err := env.Client.Delete(ctx, keep); err != nil {
			t.Fatal(err)
		}
		gone(t, keep)
		if st, err := keyState(ctx, kmsIn(env, "us-east-1"), keyID); err != nil || st != kmstypes.KeyStateEnabled {
			t.Fatalf("retained replica in us-east-1: %s %v", st, err)
		}
		primaryUntouched(t)
	})

	t.Run("delete schedules the replica's deletion, never the primary key's", func(t *testing.T) {
		if err := env.Client.Delete(ctx, eu); err != nil {
			t.Fatal(err)
		}
		gone(t, eu)
		st, err := keyState(ctx, euKMS, keyID)
		if err != nil {
			t.Fatal(err)
		}
		if st != kmstypes.KeyStatePendingDeletion {
			t.Fatalf("the replica in eu-west-1 is %s, want PendingDeletion", st)
		}
		primaryUntouched(t)
	})
}
