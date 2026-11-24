// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package object

import (
	"context"
	"encoding/base64"
	"net/url"
	"testing"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/s3/apis/v1alpha1"
)

func TestBody(t *testing.T) {
	b, err := body(&svcapitypes.Object{Spec: svcapitypes.ObjectSpec{Content: aws.String("hello")}})
	if err != nil || string(b) != "hello" {
		t.Fatalf("content: %q (%v)", b, err)
	}
	b, err = body(&svcapitypes.Object{Spec: svcapitypes.ObjectSpec{ContentBase64: aws.String(base64.StdEncoding.EncodeToString([]byte{0, 1, 2}))}})
	if err != nil || len(b) != 3 || b[2] != 2 {
		t.Fatalf("contentBase64: %v (%v)", b, err)
	}
	if _, err := body(&svcapitypes.Object{Spec: svcapitypes.ObjectSpec{ContentBase64: aws.String("not base64!")}}); err == nil {
		t.Fatal("bad base64 accepted")
	}
	if _, err := body(&svcapitypes.Object{}); err == nil {
		t.Fatal("no content accepted")
	}
	if sha([]byte("hello")) != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatal("sha256")
	}
}

func TestTaggingAndCompare(t *testing.T) {
	tags := []*svcapitypes.Tag{{Key: aws.String("team"), Value: aws.String("a&b")}, {Key: aws.String("env"), Value: aws.String("prod")}}
	v, err := url.ParseQuery(aws.ToString(tagging(tags)))
	if err != nil || v.Get("team") != "a&b" || v.Get("env") != "prod" {
		t.Fatalf("tagging %q (%v)", aws.ToString(tagging(tags)), err)
	}
	if tagging(nil) != nil {
		t.Fatal("no tags is no header")
	}
	if !equalTags(tags, []*svcapitypes.Tag{tags[1], tags[0]}) || equalTags(tags, tags[:1]) {
		t.Fatal("tags compared as a set")
	}
	if !equalMetadata(map[string]*string{"Owner": aws.String("x")}, map[string]*string{"owner": aws.String("x")}) ||
		equalMetadata(map[string]*string{"owner": aws.String("x")}, nil) || !equalMetadata(nil, map[string]*string{}) {
		t.Fatal("metadata compared with S3's lower-case keys")
	}
	if optional(nil, aws.String("binary/octet-stream")) != nil || aws.ToString(optional(aws.String("text/plain"), nil)) != "" {
		t.Fatal("optional")
	}
}

func TestDelta(t *testing.T) {
	a := &resource{&svcapitypes.Object{Spec: svcapitypes.ObjectSpec{BucketName: aws.String("b"), Key: aws.String("k"), Content: aws.String("x"),
		Tags: []*svcapitypes.Tag{{Key: aws.String("t"), Value: aws.String("1")}}}}}
	b := &resource{a.ko.DeepCopy()}
	if d := newResourceDelta(a, b); len(d.Differences) != 0 {
		t.Fatalf("differences %v", d.Differences)
	}
	b.ko.Spec.Content = nil // what sdkFind leaves when the content changed
	b.ko.Spec.Tags = nil
	d := newResourceDelta(a, b)
	if !d.DifferentAt("Spec.Content") || !d.DifferentAt("Spec.Tags") || d.DifferentAt("Spec.Key") {
		t.Fatalf("differences %v", d.Differences)
	}
}

func TestReferences(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	bucket := &unstructured.Unstructured{}
	bucket.SetGroupVersionKind(ackBucketGVK)
	bucket.SetNamespace("default")
	bucket.SetName("logs")
	_ = unstructured.SetNestedField(bucket.Object, "logs-bucket-123", "spec", "name")
	_ = unstructured.SetNestedSlice(bucket.Object, []any{map[string]any{"type": string(ackv1alpha1.ConditionTypeResourceSynced), "status": "True"}}, "status", "conditions")
	unsynced := bucket.DeepCopy()
	unsynced.SetName("new")
	_ = unstructured.SetNestedSlice(unsynced.Object, []any{}, "status", "conditions")
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		bucket, unsynced,
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "site"}, Data: map[string]string{"index.html": "<h1>hi</h1>"}, BinaryData: map[string][]byte{"logo": {0x89, 0x50}}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "creds"}, Data: map[string][]byte{"token": []byte("s3cr3t")}},
	).Build()
	rm := &resourceManager{}
	ctx := context.Background()
	obj := func(ref string, src *svcapitypes.ObjectContentSource) *resource {
		return &resource{&svcapitypes.Object{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "o"},
			Spec: svcapitypes.ObjectSpec{Key: aws.String("k"), ContentFrom: src,
				BucketRef: &ackv1alpha1.AWSResourceReferenceWrapper{From: &ackv1alpha1.AWSResourceReference{Name: aws.String(ref)}}},
		}}
	}
	decoded := func(r *resource) string {
		b, _ := base64.StdEncoding.DecodeString(aws.ToString(r.ko.Spec.ContentBase64))
		return string(b)
	}

	for _, c := range []struct {
		src  *svcapitypes.ObjectContentSource
		want string
	}{
		{&svcapitypes.ObjectContentSource{ConfigMapKeyRef: &svcapitypes.ObjectKeyRef{Name: "site", Key: "index.html"}}, "<h1>hi</h1>"},
		{&svcapitypes.ObjectContentSource{ConfigMapKeyRef: &svcapitypes.ObjectKeyRef{Name: "site", Key: "logo"}}, "\x89P"},
		{&svcapitypes.ObjectContentSource{SecretKeyRef: &svcapitypes.ObjectKeyRef{Name: "creds", Key: "token"}}, "s3cr3t"},
	} {
		res, has, err := rm.ResolveReferences(ctx, reader, obj("logs", c.src))
		if err != nil || !has {
			t.Fatalf("resolve %+v: %v", c.src, err)
		}
		r := res.(*resource)
		if aws.ToString(r.ko.Spec.BucketName) != "logs-bucket-123" || decoded(r) != c.want {
			t.Fatalf("bucket %q content %q", aws.ToString(r.ko.Spec.BucketName), decoded(r))
		}
		cleared := rm.ClearResolvedReferences(r).(*resource)
		if cleared.ko.Spec.BucketName != nil || cleared.ko.Spec.ContentBase64 != nil || cleared.ko.Spec.ContentFrom == nil {
			t.Fatalf("cleared %+v", cleared.ko.Spec)
		}
	}
	if _, _, err := rm.ResolveReferences(ctx, reader, obj("new", nil)); err == nil || !isNotSynced(err) {
		t.Fatalf("an unsynced Bucket: %v", err)
	}
	if _, _, err := rm.ResolveReferences(ctx, reader, obj("logs", &svcapitypes.ObjectContentSource{ConfigMapKeyRef: &svcapitypes.ObjectKeyRef{Name: "site", Key: "missing"}})); err == nil {
		t.Fatal("a missing key resolved")
	}
}

func isNotSynced(err error) bool {
	return err.Error() == ackerr.ResourceReferenceNotSyncedFor("Bucket", "default", "new").Error()
}
