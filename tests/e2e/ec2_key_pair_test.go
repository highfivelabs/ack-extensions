// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	ec2v1 "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
)

// testKeyPairPublicKey is an OpenSSH public key made for this test (the
// public half only; its private key was never kept). ec2-controller pull
// request #396's unit test uses the same key.
const testKeyPairPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPfh4lRy/xJUbMAlfUO6qYyTTnKLDwducZGNA7VIgbz5 test"

func cloudKeyPair(ctx context.Context, c *ec2.Client, name string) (*ec2types.KeyPairInfo, error) {
	out, err := c.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{KeyNames: []string{name}})
	if err != nil {
		return nil, err
	}
	if len(out.KeyPairs) != 1 {
		return nil, fmt.Errorf("%d key pairs named %s", len(out.KeyPairs), name)
	}
	return &out.KeyPairs[0], nil
}

// KeyPair: an EC2 key pair imported from a public key (ImportKeyPair), so
// the private key never reaches AWS or the cluster. ACK's ec2-controller
// has it in pull request #396 only (docs/plan.md section 6.50).
func TestKeyPair(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = ec2v1.AddToScheme(scheme)
	env := harness.Start(t, "ec2", scheme)
	cloud := ec2.NewFromConfig(env.AWS)
	ctx := context.Background()
	name := "ackx-e2e-deploy"

	kp := &ec2v1.KeyPair{
		ObjectMeta: metav1.ObjectMeta{Name: "deploy", Namespace: "default"},
		Spec: ec2v1.KeyPairSpec{
			KeyName:           aws.String(name),
			PublicKeyMaterial: aws.String(testKeyPairPublicKey),
			Tags:              []*ec2v1.Tag{{Key: aws.String("team"), Value: aws.String("platform")}},
		},
	}
	waitSynced := func(t *testing.T) *ec2v1.KeyPair {
		t.Helper()
		got := &ec2v1.KeyPair{}
		harness.Eventually(t, 60*time.Second, "key pair synced", func() error {
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(kp), got); err != nil {
				return err
			}
			if ok, why := synced(got.Status.Conditions); !ok {
				return fmt.Errorf("not synced: %s", why)
			}
			return nil
		})
		return got
	}

	t.Run("a public key is imported, tagged, with its ID and fingerprint", func(t *testing.T) {
		if err := env.Client.Create(ctx, kp); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t)
		id := aws.ToString(got.Status.KeyPairID)
		if !strings.HasPrefix(id, "key-") || aws.ToString(got.Status.KeyFingerprint) == "" {
			t.Fatalf("status id %q fingerprint %q", id, aws.ToString(got.Status.KeyFingerprint))
		}
		k, err := cloudKeyPair(ctx, cloud, name)
		if err != nil {
			t.Fatal(err)
		}
		if aws.ToString(k.KeyPairId) != id || k.KeyType != ec2types.KeyTypeEd25519 || ec2TagMap(k.Tags)["team"] != "platform" {
			t.Fatalf("cloud key pair id %q type %q tags %v", aws.ToString(k.KeyPairId), k.KeyType, ec2TagMap(k.Tags))
		}
		// DescribeKeyPairs never returns the public key: the spec keeps it.
		if aws.ToString(got.Spec.PublicKeyMaterial) != testKeyPairPublicKey {
			t.Fatalf("spec.publicKeyMaterial %q", aws.ToString(got.Spec.PublicKeyMaterial))
		}
	})

	t.Run("its tags change", func(t *testing.T) {
		harness.Eventually(t, 30*time.Second, "tags updated in the spec", func() error {
			obj := &ec2v1.KeyPair{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(kp), obj); err != nil {
				return err
			}
			obj.Spec.Tags = []*ec2v1.Tag{{Key: aws.String("env"), Value: aws.String("prod")}}
			return env.Client.Update(ctx, obj)
		})
		harness.Eventually(t, 60*time.Second, "new tags in the cloud", func() error {
			k, err := cloudKeyPair(ctx, cloud, name)
			if err != nil {
				return err
			}
			if tags := ec2TagMap(k.Tags); tags["env"] != "prod" || tags["team"] != "" {
				return fmt.Errorf("tags %v", tags)
			}
			return nil
		})
		waitSynced(t)
	})

	t.Run("its name and key cannot change", func(t *testing.T) {
		obj := &ec2v1.KeyPair{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(kp), obj); err != nil {
			t.Fatal(err)
		}
		obj.Spec.KeyName = aws.String(name + "-2")
		if err := env.Client.Update(ctx, obj); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("changing keyName: got %v, want the CRD's immutability refusal", err)
		}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(kp), obj); err != nil {
			t.Fatal(err)
		}
		obj.Spec.PublicKeyMaterial = aws.String(strings.TrimSuffix(testKeyPairPublicKey, " test") + " other")
		if err := env.Client.Update(ctx, obj); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("changing publicKeyMaterial: got %v, want the CRD's immutability refusal", err)
		}
	})

	t.Run("delete deletes the key pair", func(t *testing.T) {
		if err := env.Client.Delete(ctx, kp); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "object gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(kp), &ec2v1.KeyPair{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
		out, err := cloud.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{
			Filters: []ec2types.Filter{{Name: aws.String("key-name"), Values: []string{name}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.KeyPairs) != 0 {
			t.Fatalf("key pair %s still in the cloud", name)
		}
	})
}
