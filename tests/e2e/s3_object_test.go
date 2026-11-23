// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	s3v1 "github.com/highfivelabs/ack-extensions/services/s3/apis/v1alpha1"
	bucketext "github.com/highfivelabs/ack-extensions/services/s3/pkg/extend/bucket"
	"github.com/highfivelabs/ack-extensions/services/s3/pkg/resource/object"
)

// Object (docs/plan.md open question 27, answered 2026-11-23): one object in
// an S3 bucket, its content inline (content, contentBase64) or from a
// ConfigMap or Secret key (contentFrom), put again when that value changes;
// Terraform's aws_s3_object. Hand-written on ACK's runtime in our s3
// manager. bucketRef names ACK's Bucket, whose CRD is the copy in
// hack/ack-crds (s3-controller is not a module here): the test makes the
// bucket in the fake and ACK's Bucket as ACK leaves it once synced.
func TestS3Object(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = s3v1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	env, _ := harness.StartControllers(t, "s3", scheme, harness.Controller{Args: []string{"--aws-endpoint-use-path-style=true"}})
	ctx := context.Background()
	api := s3.NewFromConfig(env.AWS, func(o *s3.Options) { o.UsePathStyle = true })

	bucket := "site-objects"
	if id := os.Getenv("LIVE_RUN_ID"); harness.IsLive() && id != "" {
		bucket += "-" + id
	}
	if _, err := api.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket:                    aws.String(bucket),
		CreateBucketConfiguration: &s3types.CreateBucketConfiguration{LocationConstraint: s3types.BucketLocationConstraint(harness.Region)},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if out, err := api.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: aws.String(bucket)}); err == nil {
			for _, v := range out.Versions {
				_, _ = api.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: v.Key, VersionId: v.VersionId})
			}
		}
		if _, err := api.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)}); err != nil {
			t.Logf("cleanup: bucket %s: %v", bucket, err)
		}
	})

	// ACK's Bucket, synced, standing for the bucket made above.
	ackBucket := &unstructured.Unstructured{}
	ackBucket.SetGroupVersionKind(bucketext.GVK)
	ackBucket.SetNamespace("default")
	ackBucket.SetName("site")
	_ = unstructured.SetNestedField(ackBucket.Object, bucket, "spec", "name")
	if err := env.Client.Create(ctx, ackBucket); err != nil {
		t.Fatal(err)
	}
	_ = unstructured.SetNestedSlice(ackBucket.Object, []any{map[string]any{"type": string(ackv1alpha1.ConditionTypeResourceSynced), "status": "True"}}, "status", "conditions")
	if err := env.Client.Status().Update(ctx, ackBucket); err != nil {
		t.Fatal(err)
	}

	get := func(key string) (string, *s3.GetObjectOutput, error) {
		out, err := api.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
		if err != nil {
			return "", nil, err
		}
		defer out.Body.Close()
		b, err := io.ReadAll(out.Body)
		return string(b), out, err
	}
	waitContent := func(t *testing.T, key, want string) *s3.GetObjectOutput {
		t.Helper()
		var got *s3.GetObjectOutput
		harness.Eventually(t, 60*time.Second, key+" content", func() error {
			b, out, err := get(key)
			if err != nil {
				return err
			}
			if b != want {
				return fmt.Errorf("content %q", b)
			}
			got = out
			return nil
		})
		return got
	}
	waitSynced := func(t *testing.T, name string) *s3v1.Object {
		t.Helper()
		o := &s3v1.Object{}
		harness.Eventually(t, 60*time.Second, name+" synced", func() error {
			if err := env.Client.Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, o); err != nil {
				return err
			}
			if ok, why := synced(o.Status.Conditions); !ok {
				return fmt.Errorf("not synced: %s", why)
			}
			return nil
		})
		return o
	}
	update := func(t *testing.T, name string, f func(*s3v1.Object)) {
		t.Helper()
		harness.Eventually(t, 30*time.Second, "update "+name, func() error {
			o := &s3v1.Object{}
			if err := env.Client.Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, o); err != nil {
				return err
			}
			f(o)
			return env.Client.Update(ctx, o)
		})
	}

	t.Run("inline content, headers, metadata and tags, in ACK's Bucket by bucketRef", func(t *testing.T) {
		o := &s3v1.Object{
			ObjectMeta: metav1.ObjectMeta{Name: "index", Namespace: "default"},
			Spec: s3v1.ObjectSpec{
				BucketRef:    &ackv1alpha1.AWSResourceReferenceWrapper{From: &ackv1alpha1.AWSResourceReference{Name: aws.String("site")}},
				Key:          aws.String("index.html"),
				Content:      aws.String("<h1>hello</h1>"),
				ContentType:  aws.String("text/html"),
				CacheControl: aws.String("max-age=60"),
				Metadata:     map[string]*string{"owner": aws.String("web")},
				Tags:         []*s3v1.Tag{{Key: aws.String("team"), Value: aws.String("web")}},
			},
		}
		if err := env.Client.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t, "index")
		out := waitContent(t, "index.html", "<h1>hello</h1>")
		if aws.ToString(out.ContentType) != "text/html" || aws.ToString(out.CacheControl) != "max-age=60" || out.Metadata["owner"] != "web" {
			t.Fatalf("headers %q %q %v", aws.ToString(out.ContentType), aws.ToString(out.CacheControl), out.Metadata)
		}
		tags, err := api.GetObjectTagging(ctx, &s3.GetObjectTaggingInput{Bucket: aws.String(bucket), Key: aws.String("index.html")})
		if err != nil || len(tags.TagSet) != 1 || aws.ToString(tags.TagSet[0].Key) != "team" {
			t.Fatalf("tags %+v (%v)", tags, err)
		}
		if got.Spec.BucketName != nil {
			t.Fatalf("spec.bucketName %q written into the stored spec", aws.ToString(got.Spec.BucketName))
		}
		if got.Status.ETag == nil || got.Annotations[object.AnnotationLastContentSHA256] == "" {
			t.Fatalf("status %+v annotations %v", got.Status, got.Annotations)
		}
	})

	t.Run("a resync puts nothing", func(t *testing.T) {
		head := func() time.Time {
			out, err := api.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("index.html")})
			if err != nil {
				t.Fatal(err)
			}
			return aws.ToTime(out.LastModified)
		}
		before := head()
		// Two of the harness's five-second resyncs, and S3's one-second
		// LastModified.
		time.Sleep(11 * time.Second)
		if after := head(); !after.Equal(before) {
			t.Fatalf("put again on a resync: last modified %v, was %v", after, before)
		}
	})

	t.Run("a content change is put; a tag change alone is not a new put", func(t *testing.T) {
		update(t, "index", func(o *s3v1.Object) { o.Spec.Content = aws.String("<h1>hello again</h1>") })
		waitContent(t, "index.html", "<h1>hello again</h1>")
		before := waitSynced(t, "index").Status.ETag
		update(t, "index", func(o *s3v1.Object) {
			o.Spec.Tags = []*s3v1.Tag{{Key: aws.String("team"), Value: aws.String("platform")}}
		})
		harness.Eventually(t, 60*time.Second, "tag changed", func() error {
			tags, err := api.GetObjectTagging(ctx, &s3.GetObjectTaggingInput{Bucket: aws.String(bucket), Key: aws.String("index.html")})
			if err != nil {
				return err
			}
			if len(tags.TagSet) != 1 || aws.ToString(tags.TagSet[0].Value) != "platform" {
				return fmt.Errorf("tags %+v", tags.TagSet)
			}
			return nil
		})
		if after := waitSynced(t, "index").Status.ETag; aws.ToString(after) != aws.ToString(before) {
			t.Fatalf("ETag %q, was %q: the object was put again for a tag", aws.ToString(after), aws.ToString(before))
		}
	})

	t.Run("an object changed outside Kubernetes is put back", func(t *testing.T) {
		if _, err := api.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("index.html"), Body: strings.NewReader("defaced")}); err != nil {
			t.Fatal(err)
		}
		// The harness gives the managers a five-second resync.
		waitContent(t, "index.html", "<h1>hello again</h1>")
	})

	t.Run("contentFrom a ConfigMap is put again when the value changes", func(t *testing.T) {
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: "default"}, Data: map[string]string{"config.json": `{"v":1}`}}
		if err := env.Client.Create(ctx, cm); err != nil {
			t.Fatal(err)
		}
		o := &s3v1.Object{
			ObjectMeta: metav1.ObjectMeta{Name: "config", Namespace: "default"},
			Spec: s3v1.ObjectSpec{
				BucketName:  aws.String(bucket),
				Key:         aws.String("config/config.json"),
				ContentFrom: &s3v1.ObjectContentSource{ConfigMapKeyRef: &s3v1.ObjectKeyRef{Name: "app-config", Key: "config.json"}},
				ContentType: aws.String("application/json"),
			},
		}
		if err := env.Client.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
		waitContent(t, "config/config.json", `{"v":1}`)
		if got := waitSynced(t, "config"); got.Spec.ContentBase64 != nil {
			t.Fatal("the ConfigMap's value written into the stored spec")
		}
		harness.Eventually(t, 30*time.Second, "update ConfigMap", func() error {
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(cm), cm); err != nil {
				return err
			}
			cm.Data["config.json"] = `{"v":2}`
			return env.Client.Update(ctx, cm)
		})
		// No change to the Object: the resync finds the value's SHA-256
		// differs from the one last put.
		waitContent(t, "config/config.json", `{"v":2}`)
	})

	t.Run("contentFrom a Secret, binary", func(t *testing.T) {
		raw := []byte{0x89, 'P', 'N', 'G', 0, 1, 2}
		sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "blob", Namespace: "default"}, Data: map[string][]byte{"logo.png": raw}}
		if err := env.Client.Create(ctx, sec); err != nil {
			t.Fatal(err)
		}
		o := &s3v1.Object{
			ObjectMeta: metav1.ObjectMeta{Name: "logo", Namespace: "default"},
			Spec: s3v1.ObjectSpec{
				BucketName:  aws.String(bucket),
				Key:         aws.String("logo.png"),
				ContentFrom: &s3v1.ObjectContentSource{SecretKeyRef: &s3v1.ObjectKeyRef{Name: "blob", Key: "logo.png"}},
			},
		}
		if err := env.Client.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
		waitContent(t, "logo.png", string(raw))
	})

	t.Run("the API server refuses two contents and a changed key", func(t *testing.T) {
		bad := &s3v1.Object{
			ObjectMeta: metav1.ObjectMeta{Name: "two", Namespace: "default"},
			Spec: s3v1.ObjectSpec{BucketName: aws.String(bucket), Key: aws.String("two"), Content: aws.String("a"),
				ContentBase64: aws.String(base64.StdEncoding.EncodeToString([]byte("b")))},
		}
		if err := env.Client.Create(ctx, bad); err == nil || !strings.Contains(err.Error(), "exactly one of content") {
			t.Fatalf("create: %v", err)
		}
		o := &s3v1.Object{}
		if err := env.Client.Get(ctx, client.ObjectKey{Namespace: "default", Name: "logo"}, o); err != nil {
			t.Fatal(err)
		}
		o.Spec.Key = aws.String("other.png")
		if err := env.Client.Update(ctx, o); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("update: %v", err)
		}
	})

	t.Run("delete removes the object; retain keeps it", func(t *testing.T) {
		o := &s3v1.Object{}
		if err := env.Client.Get(ctx, client.ObjectKey{Namespace: "default", Name: "logo"}, o); err != nil {
			t.Fatal(err)
		}
		if err := env.Client.Delete(ctx, o); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "logo.png deleted", func() error {
			if _, _, err := get("logo.png"); err == nil {
				return fmt.Errorf("still there")
			}
			return nil
		})
		update(t, "config", func(o *s3v1.Object) {
			if o.Annotations == nil {
				o.Annotations = map[string]string{}
			}
			o.Annotations[ackv1alpha1.AnnotationDeletionPolicy] = string(ackv1alpha1.DeletionPolicyRetain)
		})
		cfg := &s3v1.Object{}
		if err := env.Client.Get(ctx, client.ObjectKey{Namespace: "default", Name: "config"}, cfg); err != nil {
			t.Fatal(err)
		}
		if err := env.Client.Delete(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "config gone from Kubernetes", func() error {
			if err := env.Client.Get(ctx, client.ObjectKey{Namespace: "default", Name: "config"}, cfg); err == nil {
				return fmt.Errorf("still there")
			}
			return nil
		})
		if b, _, err := get("config/config.json"); err != nil || b != `{"v":2}` {
			t.Fatalf("retained object: %q (%v)", b, err)
		}
	})
}
