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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	s3v1 "github.com/highfivelabs/ack-extensions/services/s3/apis/v1alpha1"
	"github.com/highfivelabs/ack-extensions/services/s3/pkg/extend/bucket"
)

// s3.aws.hfl.dev Bucket: ACK s3-controller v1.15.0's Bucket, copied and
// fixed (docs/plan.md section 6.38). s3-controller panics reading a
// bucket whose GetBucketEncryption answers
// ServerSideEncryptionConfigurationNotFoundError, which moto 5.2.5 does
// for every new bucket (section 8, item 15): the bucket made here is that
// case, so a Synced bucket read across resyncs is the fix proven.
func TestBucket(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = s3v1.AddToScheme(scheme)
	env, _ := harness.StartControllers(t, "s3", scheme, harness.Controller{Args: []string{
		"--aws-endpoint-use-path-style=true",
		// Resync every few seconds, so the read runs again and again.
		"--reconcile-default-resync-seconds=5",
	}})
	ctx := context.Background()
	api := s3.NewFromConfig(env.AWS, func(o *s3.Options) { o.UsePathStyle = true })

	errorCode := func(err error) string {
		var ae smithy.APIError
		if errors.As(err, &ae) {
			return ae.ErrorCode()
		}
		return ""
	}
	waitSynced := func(t *testing.T, name string) *s3v1.Bucket {
		t.Helper()
		got := &s3v1.Bucket{}
		harness.Eventually(t, 90*time.Second, name+" synced", func() error {
			if err := env.Client.Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, got); err != nil {
				return err
			}
			if ok, why := synced(got.Status.Conditions); !ok {
				return fmt.Errorf("not synced: %s", why)
			}
			return nil
		})
		return got
	}
	gone := func(t *testing.T, name string) {
		t.Helper()
		harness.Eventually(t, 90*time.Second, name+" gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, &s3v1.Bucket{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}
	noPanic := func(t *testing.T) {
		t.Helper()
		if logs := env.Logs(); strings.Contains(logs, "nil pointer dereference") || strings.Contains(logs, "panic:") {
			t.Fatalf("the manager panicked:\n%s", logs)
		}
	}
	tags := func(t *testing.T, name string) map[string]string {
		t.Helper()
		out, err := api.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: aws.String(name)})
		if errorCode(err) == "NoSuchTagSet" {
			return map[string]string{}
		}
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		for _, tag := range out.TagSet {
			m[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
		}
		return m
	}

	const name = "hfl-fixed-bucket"
	b := &s3v1.Bucket{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: s3v1.BucketSpec{
			Name:    aws.String(name),
			Tagging: &s3v1.Tagging{TagSet: []*s3v1.Tag{{Key: aws.String("team"), Value: aws.String("storage")}}},
		},
	}

	t.Run("made, and read with no default encryption, synced across resyncs", func(t *testing.T) {
		if err := env.Client.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, name)
		// The case the fix is for: the fake answers the error ACK's read
		// dereferenced after. AWS Emulator (and AWS) always report SSE-S3;
		// there the read is ordinary and this run proves only the copy.
		_, err := api.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: aws.String(name)})
		switch code := errorCode(err); {
		case code == "ServerSideEncryptionConfigurationNotFoundError":
		case err == nil:
			t.Log("the backend reports a default encryption: the not-found read is not exercised here (the unit test covers it)")
		default:
			t.Fatalf("GetBucketEncryption: %v", err)
		}
		if got := tags(t, name); got["team"] != "storage" {
			t.Fatalf("cloud tags %v", got)
		}
		// Several resyncs (5 s each): still synced, and no panic.
		time.Sleep(20 * time.Second)
		got := waitSynced(t, name)
		if got.Spec.Encryption != nil && err != nil {
			t.Fatalf("spec.encryption %+v for a bucket without one", got.Spec.Encryption)
		}
		noPanic(t)
	})

	t.Run("its tags change", func(t *testing.T) {
		obj := &s3v1.Bucket{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(b), obj); err != nil {
			t.Fatal(err)
		}
		obj.Spec.Tagging = &s3v1.Tagging{TagSet: []*s3v1.Tag{{Key: aws.String("team"), Value: aws.String("data")}}}
		if err := env.Client.Update(ctx, obj); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "new tags in the cloud", func() error {
			if got := tags(t, name); got["team"] != "data" {
				return fmt.Errorf("tags %v", got)
			}
			return nil
		})
		waitSynced(t, name)
		noPanic(t)
	})

	t.Run("force-destroy empties our Bucket too, and the delete deletes it", func(t *testing.T) {
		obj := &s3v1.Bucket{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(b), obj); err != nil {
			t.Fatal(err)
		}
		obj.SetAnnotations(map[string]string{bucket.AnnotationForceDestroy: "true"})
		if err := env.Client.Update(ctx, obj); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 30*time.Second, "the extension's finalizer", func() error {
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(b), obj); err != nil {
				return err
			}
			for _, f := range obj.GetFinalizers() {
				if f == bucket.Finalizer {
					return nil
				}
			}
			return fmt.Errorf("finalizers %v", obj.GetFinalizers())
		})
		if _, err := api.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(name), Key: aws.String("data/report.csv")}); err != nil {
			t.Fatal(err)
		}
		if err := env.Client.Delete(ctx, obj); err != nil {
			t.Fatal(err)
		}
		gone(t, name)
		_, err := api.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(name)})
		if err == nil {
			t.Fatal("the bucket is still in the cloud")
		}
		noPanic(t)
	})
}
