// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	"github.com/highfivelabs/ack-extensions/services/s3/pkg/extend/bucket"
)

// The Bucket extension (docs/extensions.md): a Bucket annotated
// s3.aws.hfl.dev/force-destroy is emptied when it is deleted, so ACK's
// DeleteBucket succeeds.
//
// ACK's s3-controller is not a Go module here (a new upstream dependency
// is the builder's call), so the test plays ACK's side, as ACK's runtime
// does it: the Bucket carries ACK's finalizer and the status ACK writes
// once it has made the bucket (status.ackResourceMetadata), and on delete
// the test calls DeleteBucket until it succeeds and then removes ACK's
// finalizer. ACK's CRD is the copy in hack/ack-crds (s3-controller v1.15.0,
// UPSTREAM.md).
func TestBucketExtension(t *testing.T) {
	env, _ := harness.StartControllers(t, "s3", nil, harness.Controller{Args: []string{"--aws-endpoint-use-path-style=true"}})
	ctx := context.Background()
	api := s3.NewFromConfig(env.AWS, func(o *s3.Options) { o.UsePathStyle = true })
	// Bucket names are unique across all of AWS: on a live run they take
	// the run's ID (live run ackx-11160402-b394: plain-bucket, marker-bucket
	// and force-bucket belong to other accounts).
	bucketName := func(n string) string {
		if id := os.Getenv("LIVE_RUN_ID"); harness.IsLive() && id != "" {
			return n + "-" + id
		}
		return n
	}
	plainBucket, markerBucket := bucketName("plain-bucket"), bucketName("marker-bucket")
	forceBucket, keptBucket := bucketName("force-bucket"), bucketName("kept-bucket")
	changedBucket, lockedBucket := bucketName("changed-bucket"), bucketName("locked-bucket")
	// kept-bucket is left in the cloud by design (its versions kept): it is
	// emptied and deleted after the test.
	t.Cleanup(func() {
		if out, err := api.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: aws.String(keptBucket)}); err == nil {
			for _, v := range out.Versions {
				_, _ = api.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(keptBucket), Key: v.Key, VersionId: v.VersionId})
			}
			for _, m := range out.DeleteMarkers {
				_, _ = api.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(keptBucket), Key: m.Key, VersionId: m.VersionId})
			}
		}
		if _, err := api.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(keptBucket)}); err != nil {
			t.Logf("cleanup: bucket %s: %v", keptBucket, err)
		}
	})
	// locked-bucket holds an object under governance retention: if its
	// subtest stops before the extension empties it, delete it here.
	t.Cleanup(func() {
		out, err := api.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: aws.String(lockedBucket)})
		if err != nil {
			return
		}
		for _, v := range out.Versions {
			_, _ = api.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(lockedBucket), Key: v.Key, VersionId: v.VersionId, BypassGovernanceRetention: aws.Bool(true)})
		}
		for _, m := range out.DeleteMarkers {
			_, _ = api.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(lockedBucket), Key: m.Key, VersionId: m.VersionId})
		}
		if _, err := api.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(lockedBucket)}); err != nil {
			t.Logf("cleanup: bucket %s: %v", lockedBucket, err)
		}
	})

	// newBucket makes ACK's Bucket and, when made, the bucket itself (with
	// Object Lock enabled when locked) and the status ACK would write.
	newBucket := func(t *testing.T, name string, annotations map[string]string, made bool, locked ...bool) *unstructured.Unstructured {
		t.Helper()
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(bucket.GVK)
		u.SetNamespace("default")
		u.SetName(name)
		u.SetAnnotations(annotations)
		u.SetFinalizers([]string{bucket.ACKFinalizer})
		_ = unstructured.SetNestedField(u.Object, name, "spec", "name")
		if err := env.Client.Create(ctx, u); err != nil {
			t.Fatal(err)
		}
		if made {
			var lock *bool
			if len(locked) > 0 && locked[0] {
				lock = aws.Bool(true)
			}
			if _, err := api.CreateBucket(ctx, &s3.CreateBucketInput{
				Bucket:                     aws.String(name),
				CreateBucketConfiguration:  &s3types.CreateBucketConfiguration{LocationConstraint: s3types.BucketLocationConstraint(harness.Region)},
				ObjectLockEnabledForBucket: lock,
			}); err != nil {
				t.Fatal(err)
			}
			// Our controller may have put its finalizer on meanwhile: write
			// the status on the latest version.
			harness.Eventually(t, 30*time.Second, name+" status", func() error {
				if err := env.Client.Get(ctx, client.ObjectKeyFromObject(u), u); err != nil {
					return err
				}
				_ = unstructured.SetNestedMap(u.Object, map[string]any{
					"ackResourceMetadata": map[string]any{
						"arn": "arn:aws:s3:::" + name, "ownerAccountID": env.Account, "region": harness.Region,
					},
				}, "status")
				return env.Client.Status().Update(ctx, u)
			})
		}
		return u
	}
	get := func(name string) (*unstructured.Unstructured, error) {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(bucket.GVK)
		return u, env.Client.Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, u)
	}
	waitFinalizer := func(t *testing.T, name string, want bool) {
		t.Helper()
		harness.Eventually(t, 30*time.Second, fmt.Sprintf("%s finalizer %v", name, want), func() error {
			u, err := get(name)
			if err != nil {
				return err
			}
			if controllerutil.ContainsFinalizer(u, bucket.Finalizer) != want {
				return fmt.Errorf("finalizers %v", u.GetFinalizers())
			}
			return nil
		})
	}
	fill := func(t *testing.T, name string) {
		t.Helper()
		if _, err := api.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
			Bucket: aws.String(name), VersioningConfiguration: &s3types.VersioningConfiguration{Status: s3types.BucketVersioningStatusEnabled},
		}); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			if _, err := api.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(name), Key: aws.String("data/report.csv")}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := api.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(name), Key: aws.String("logs/a")}); err != nil {
			t.Fatal(err)
		}
		// A delete marker.
		if _, err := api.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(name), Key: aws.String("logs/a")}); err != nil {
			t.Fatal(err)
		}
		if _, err := api.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(name), Key: aws.String("big/upload")}); err != nil {
			t.Fatal(err)
		}
	}
	versions := func(t *testing.T, name string) int {
		t.Helper()
		out, err := api.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: aws.String(name)})
		if err != nil {
			t.Fatal(err)
		}
		return len(out.Versions) + len(out.DeleteMarkers)
	}
	// actAsACK is ACK's delete: DeleteBucket until it succeeds, then ACK's
	// finalizer off.
	actAsACK := func(t *testing.T, name string) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, "ACK's DeleteBucket of "+name, func() error {
			_, err := api.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(name)})
			var ae smithy.APIError
			if errors.As(err, &ae) && ae.ErrorCode() == "NoSuchBucket" {
				return nil
			}
			return err
		})
		removeACKFinalizer(t, env, name)
	}
	gone := func(t *testing.T, name string) {
		t.Helper()
		harness.Eventually(t, 30*time.Second, name+" gone", func() error {
			_, err := get(name)
			if apierrors.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return err
			}
			return fmt.Errorf("still present")
		})
	}

	t.Run("a Bucket without the annotation gets no finalizer", func(t *testing.T) {
		newBucket(t, plainBucket, nil, true)
		// The annotated Bucket made next is handled after this one.
		newBucket(t, markerBucket, map[string]string{bucket.AnnotationForceDestroy: "true"}, false)
		waitFinalizer(t, markerBucket, true)
		waitFinalizer(t, plainBucket, false)
	})

	t.Run("force-destroy empties the bucket so ACK's DeleteBucket succeeds", func(t *testing.T) {
		u := newBucket(t, forceBucket, map[string]string{bucket.AnnotationForceDestroy: "true"}, true)
		waitFinalizer(t, forceBucket, true)
		fill(t, forceBucket)
		if n := versions(t, forceBucket); n != 5 {
			t.Fatalf("%d versions and delete markers before, want 5", n)
		}
		if _, err := api.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(forceBucket)}); err == nil {
			t.Fatal("DeleteBucket of a full bucket succeeded; the test proves nothing")
		}
		if err := env.Client.Delete(ctx, u); err != nil {
			t.Fatal(err)
		}
		actAsACK(t, forceBucket)
		gone(t, forceBucket)
		msg := waitEvent(t, env, "default", forceBucket, bucket.ReasonEmptied)
		if want := "deleted 5 object version(s) and delete marker(s) and aborted 1 multipart upload(s)"; !strings.Contains(msg, want) {
			t.Fatalf("event %q, want %q", msg, want)
		}
	})

	t.Run("force-destroy bypasses governance retention on an Object Lock bucket", func(t *testing.T) {
		u := newBucket(t, lockedBucket, map[string]string{bucket.AnnotationForceDestroy: "true"}, true, true)
		waitFinalizer(t, lockedBucket, true)
		// S3 needs Content-MD5 (or a checksum header) on a put with Object
		// Lock parameters (live run ackx-11160729-366f: InvalidRequest).
		body := []byte("year,total\n2026,0\n")
		sum := md5.Sum(body)
		if _, err := api.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(lockedBucket), Key: aws.String("ledger/2026.csv"),
			Body: bytes.NewReader(body), ContentMD5: aws.String(base64.StdEncoding.EncodeToString(sum[:])),
			ObjectLockMode:            s3types.ObjectLockModeGovernance,
			ObjectLockRetainUntilDate: aws.Time(time.Now().Add(24 * time.Hour)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := api.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(lockedBucket), Key: aws.String("ledger/2025.csv")}); err != nil {
			t.Fatal(err)
		}
		if err := env.Client.Delete(ctx, u); err != nil {
			t.Fatal(err)
		}
		actAsACK(t, lockedBucket)
		gone(t, lockedBucket)
		msg := waitEvent(t, env, "default", lockedBucket, bucket.ReasonEmptied)
		if want := "deleted 2 object version(s)"; !strings.Contains(msg, want) {
			t.Fatalf("event %q, want %q", msg, want)
		}
	})

	t.Run("the retain deletion policy keeps the objects", func(t *testing.T) {
		u := newBucket(t, keptBucket, map[string]string{
			bucket.AnnotationForceDestroy:      "true",
			"services.k8s.aws/deletion-policy": "retain",
		}, true)
		waitFinalizer(t, keptBucket, true)
		fill(t, keptBucket)
		if err := env.Client.Delete(ctx, u); err != nil {
			t.Fatal(err)
		}
		waitFinalizer(t, keptBucket, false)
		waitEvent(t, env, "default", keptBucket, bucket.ReasonNotEmptied)
		if n := versions(t, keptBucket); n != 5 {
			t.Fatalf("%d versions and delete markers after, want 5", n)
		}
		removeACKFinalizer(t, env, keptBucket)
		gone(t, keptBucket)
	})

	t.Run("removing the annotation releases the finalizer", func(t *testing.T) {
		newBucket(t, changedBucket, map[string]string{bucket.AnnotationForceDestroy: "true"}, true)
		waitFinalizer(t, changedBucket, true)
		harness.Eventually(t, 30*time.Second, "annotation off", func() error {
			u, err := get(changedBucket)
			if err != nil {
				return err
			}
			a := u.GetAnnotations()
			delete(a, bucket.AnnotationForceDestroy)
			u.SetAnnotations(a)
			return env.Client.Update(ctx, u)
		})
		waitFinalizer(t, changedBucket, false)
	})

	t.Run("a Bucket ACK never made is released at once", func(t *testing.T) {
		if err := env.Client.Delete(ctx, mustGet(t, get, markerBucket)); err != nil {
			t.Fatal(err)
		}
		waitFinalizer(t, markerBucket, false)
		removeACKFinalizer(t, env, markerBucket)
		gone(t, markerBucket)
	})
}

func mustGet(t *testing.T, get func(string) (*unstructured.Unstructured, error), name string) *unstructured.Unstructured {
	t.Helper()
	u, err := get(name)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func removeACKFinalizer(t *testing.T, env *harness.Env, name string) {
	t.Helper()
	harness.Eventually(t, 30*time.Second, "ACK's finalizer off "+name, func() error {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(bucket.GVK)
		if err := env.Client.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: name}, u); err != nil {
			return client.IgnoreNotFound(err)
		}
		controllerutil.RemoveFinalizer(u, bucket.ACKFinalizer)
		return env.Client.Update(context.Background(), u)
	})
}
