// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package object

import (
	"reflect"

	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
)

// newResourceDelta compares the spec field by field, as ACK's generated
// delta.go does. The content is compared as sdkFind leaves it: equal to
// desired's while what was put is still there, cleared when the content
// changed in Kubernetes or in S3 (sdk.go). The bucket and the key are
// immutable.
func newResourceDelta(a *resource, b *resource) *ackcompare.Delta {
	delta := ackcompare.NewDelta()
	if (a == nil && b != nil) || (a != nil && b == nil) {
		delta.Add("", a, b)
		return delta
	}
	as, bs := a.ko.Spec, b.ko.Spec
	for _, f := range []struct {
		path string
		a, b *string
	}{
		{"Spec.BucketName", as.BucketName, bs.BucketName},
		{"Spec.Key", as.Key, bs.Key},
		{"Spec.Content", as.Content, bs.Content},
		{"Spec.ContentBase64", as.ContentBase64, bs.ContentBase64},
		{"Spec.ContentType", as.ContentType, bs.ContentType},
		{"Spec.CacheControl", as.CacheControl, bs.CacheControl},
		{"Spec.ContentDisposition", as.ContentDisposition, bs.ContentDisposition},
		{"Spec.ContentEncoding", as.ContentEncoding, bs.ContentEncoding},
		{"Spec.ContentLanguage", as.ContentLanguage, bs.ContentLanguage},
		{"Spec.ServerSideEncryption", as.ServerSideEncryption, bs.ServerSideEncryption},
		{"Spec.KMSKeyID", as.KMSKeyID, bs.KMSKeyID},
		{"Spec.StorageClass", as.StorageClass, bs.StorageClass},
	} {
		if ackcompare.HasNilDifference(f.a, f.b) {
			delta.Add(f.path, f.a, f.b)
		} else if f.a != nil && f.b != nil && *f.a != *f.b {
			delta.Add(f.path, f.a, f.b)
		}
	}
	if !reflect.DeepEqual(as.ContentFrom, bs.ContentFrom) {
		delta.Add("Spec.ContentFrom", as.ContentFrom, bs.ContentFrom)
	}
	if !equalMetadata(as.Metadata, bs.Metadata) {
		delta.Add("Spec.Metadata", as.Metadata, bs.Metadata)
	}
	if !equalTags(as.Tags, bs.Tags) {
		delta.Add("Spec.Tags", as.Tags, bs.Tags)
	}
	return delta
}
