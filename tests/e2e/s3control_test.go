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
	"github.com/aws/aws-sdk-go-v2/service/s3control"
	s3ctypes "github.com/aws/aws-sdk-go-v2/service/s3control/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	s3cv1 "github.com/highfivelabs/ack-extensions/services/s3control/apis/v1alpha1"
	"github.com/highfivelabs/ack-extensions/services/s3control/pkg/endpoint"
)

// PublicAccessBlock: the account's S3 Block Public Access settings,
// Terraform's aws_s3_account_public_access_block. ACK's s3control-controller
// (v1.9.0) has AccessPoint only, and ACK's s3-controller has a bucket's
// public access block, not the account's.
func TestPublicAccessBlock(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = s3cv1.AddToScheme(scheme)
	env := harness.Start(t, "s3control", scheme)
	// S3 Control puts the account ID in front of a custom endpoint's host
	// (123456789012.127.0.0.1); the test's client keeps the fake's host, as
	// the manager does with --allow-unsafe-aws-endpoint-urls.
	cloud := s3control.NewFromConfig(env.AWS, endpoint.Unprefixed())
	ctx := context.Background()
	account := aws.String(env.Account)

	type settings struct{ acls, ignore, policy, restrict bool }
	read := func() (settings, error) {
		out, err := cloud.GetPublicAccessBlock(ctx, &s3control.GetPublicAccessBlockInput{AccountId: account})
		if err != nil {
			return settings{}, err
		}
		c := out.PublicAccessBlockConfiguration
		if c == nil {
			return settings{}, errors.New("no configuration in the response")
		}
		return settings{aws.ToBool(c.BlockPublicAcls), aws.ToBool(c.IgnorePublicAcls), aws.ToBool(c.BlockPublicPolicy), aws.ToBool(c.RestrictPublicBuckets)}, nil
	}
	put := func(t *testing.T, s settings) {
		t.Helper()
		if _, err := cloud.PutPublicAccessBlock(ctx, &s3control.PutPublicAccessBlockInput{
			AccountId: account,
			PublicAccessBlockConfiguration: &s3ctypes.PublicAccessBlockConfiguration{
				BlockPublicAcls: aws.Bool(s.acls), IgnorePublicAcls: aws.Bool(s.ignore),
				BlockPublicPolicy: aws.Bool(s.policy), RestrictPublicBuckets: aws.Bool(s.restrict),
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	cloudIs := func(t *testing.T, want settings) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, fmt.Sprintf("the account's settings %+v", want), func() error {
			got, err := read()
			if err != nil {
				return err
			}
			if got != want {
				return fmt.Errorf("the account's settings are %+v", got)
			}
			return nil
		})
	}
	waitSynced := func(t *testing.T, obj *s3cv1.PublicAccessBlock) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.Name+" synced", func() error {
			got := &s3cv1.PublicAccessBlock{}
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
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &s3cv1.PublicAccessBlock{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}
	update := func(t *testing.T, obj *s3cv1.PublicAccessBlock, change func(*s3cv1.PublicAccessBlock)) {
		t.Helper()
		harness.Eventually(t, 30*time.Second, obj.Name+" updated", func() error {
			got := &s3cv1.PublicAccessBlock{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
				return err
			}
			change(got)
			return env.Client.Update(ctx, got)
		})
	}
	updates := func(name string) int {
		n := 0
		for _, line := range strings.Split(env.Logs(), "\n") {
			if strings.Contains(line, ">>> rm.sdkUpdate") && strings.Contains(line, fmt.Sprintf("%q", name)) {
				n++
			}
		}
		return n
	}

	// Two of the four set; the two left out are false, S3's default.
	block := &s3cv1.PublicAccessBlock{
		ObjectMeta: metav1.ObjectMeta{Name: "account", Namespace: "default"},
		Spec: s3cv1.PublicAccessBlockSpec{
			PublicAccessBlockConfiguration: &s3cv1.PublicAccessBlockConfiguration{
				BlockPublicACLs:   aws.Bool(true),
				BlockPublicPolicy: aws.Bool(true),
			},
		},
	}

	t.Run("the account has no settings before", func(t *testing.T) {
		_, err := read()
		var none *s3ctypes.NoSuchPublicAccessBlockConfiguration
		if !errors.As(err, &none) {
			t.Fatalf("want NoSuchPublicAccessBlockConfiguration, got %v", err)
		}
	})

	t.Run("create puts the settings, and the two left out are no drift", func(t *testing.T) {
		if err := env.Client.Create(ctx, block); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, block)
		cloudIs(t, settings{acls: true, policy: true})
		before := updates(block.Name)
		time.Sleep(15 * time.Second)
		if n := updates(block.Name); n != before {
			t.Fatalf("updated %d times across resyncs: S3's defaults read as drift", n-before)
		}
	})

	t.Run("an update is put", func(t *testing.T) {
		update(t, block, func(o *s3cv1.PublicAccessBlock) {
			o.Spec.PublicAccessBlockConfiguration.IgnorePublicACLs = aws.Bool(true)
		})
		cloudIs(t, settings{acls: true, ignore: true, policy: true})
	})

	t.Run("a change outside Kubernetes is put back", func(t *testing.T) {
		put(t, settings{restrict: true})
		cloudIs(t, settings{acls: true, ignore: true, policy: true})
	})

	t.Run("deletion-policy retain leaves the settings", func(t *testing.T) {
		update(t, block, func(o *s3cv1.PublicAccessBlock) {
			if o.Annotations == nil {
				o.Annotations = map[string]string{}
			}
			o.Annotations[ackv1alpha1.AnnotationDeletionPolicy] = string(ackv1alpha1.DeletionPolicyRetain)
		})
		// Let the controller see the annotation before the delete.
		time.Sleep(10 * time.Second)
		if err := env.Client.Delete(ctx, block); err != nil {
			t.Fatal(err)
		}
		gone(t, block)
		cloudIs(t, settings{acls: true, ignore: true, policy: true})
	})

	again := &s3cv1.PublicAccessBlock{
		ObjectMeta: metav1.ObjectMeta{Name: "account-2", Namespace: "default"},
		Spec: s3cv1.PublicAccessBlockSpec{
			PublicAccessBlockConfiguration: &s3cv1.PublicAccessBlockConfiguration{
				BlockPublicACLs: aws.Bool(true), IgnorePublicACLs: aws.Bool(true),
				BlockPublicPolicy: aws.Bool(true), RestrictPublicBuckets: aws.Bool(true),
			},
		},
	}

	t.Run("an existing configuration is taken over by a new object", func(t *testing.T) {
		// The retained object's settings are still there; a new object
		// takes them over rather than being refused as already existing.
		if err := env.Client.Create(ctx, again); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, again)
		cloudIs(t, settings{acls: true, ignore: true, policy: true, restrict: true})
	})

	t.Run("delete deletes the settings", func(t *testing.T) {
		if err := env.Client.Delete(ctx, again); err != nil {
			t.Fatal(err)
		}
		gone(t, again)
		harness.Eventually(t, 30*time.Second, "the account's settings gone", func() error {
			_, err := read()
			var none *s3ctypes.NoSuchPublicAccessBlockConfiguration
			if !errors.As(err, &none) {
				return fmt.Errorf("want NoSuchPublicAccessBlockConfiguration, got %v", err)
			}
			return nil
		})
	})
}
