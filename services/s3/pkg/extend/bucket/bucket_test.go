// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package bucket

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/s3"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// fakeS3 is one bucket's versions and uploads, listed a page at a time.
type fakeS3 struct {
	versions []string // "key@version"
	uploads  []string
	locked   map[string]bool // versions DeleteObjects refuses
	lock     bool            // Object Lock enabled on the bucket
	page     int
	gone     bool
	batches  []int
	bypass   []bool
}

var errNoSuchBucket = &smithy.GenericAPIError{Code: "NoSuchBucket"}

func (f *fakeS3) ListObjectVersions(_ context.Context, in *svcsdk.ListObjectVersionsInput, _ ...func(*svcsdk.Options)) (*svcsdk.ListObjectVersionsOutput, error) {
	if f.gone {
		return nil, errNoSuchBucket
	}
	start := 0
	if in.KeyMarker != nil {
		fmt.Sscan(aws.ToString(in.KeyMarker), &start)
	}
	end := min(start+f.page, len(f.versions))
	out := &svcsdk.ListObjectVersionsOutput{}
	for _, v := range f.versions[start:end] {
		k, id, _ := strings.Cut(v, "@")
		if strings.HasPrefix(id, "marker") {
			out.DeleteMarkers = append(out.DeleteMarkers, svcsdktypes.DeleteMarkerEntry{Key: aws.String(k), VersionId: aws.String(id)})
		} else {
			out.Versions = append(out.Versions, svcsdktypes.ObjectVersion{Key: aws.String(k), VersionId: aws.String(id)})
		}
	}
	if end < len(f.versions) {
		// The fake's marker is the index of the next entry; the deletes
		// of this page have not shifted it yet, as S3's would not.
		out.IsTruncated = aws.Bool(true)
		out.NextKeyMarker, out.NextVersionIdMarker = aws.String(fmt.Sprint(end)), aws.String("x")
	}
	return out, nil
}

func (f *fakeS3) GetObjectLockConfiguration(context.Context, *svcsdk.GetObjectLockConfigurationInput, ...func(*svcsdk.Options)) (*svcsdk.GetObjectLockConfigurationOutput, error) {
	if f.gone {
		return nil, errNoSuchBucket
	}
	if !f.lock {
		return nil, &smithy.GenericAPIError{Code: "ObjectLockConfigurationNotFoundError"}
	}
	return &svcsdk.GetObjectLockConfigurationOutput{ObjectLockConfiguration: &svcsdktypes.ObjectLockConfiguration{ObjectLockEnabled: svcsdktypes.ObjectLockEnabledEnabled}}, nil
}

func (f *fakeS3) DeleteObjects(_ context.Context, in *svcsdk.DeleteObjectsInput, _ ...func(*svcsdk.Options)) (*svcsdk.DeleteObjectsOutput, error) {
	f.batches = append(f.batches, len(in.Delete.Objects))
	f.bypass = append(f.bypass, aws.ToBool(in.BypassGovernanceRetention))
	// As real S3 (live run ackx-11160411-5872): the header is refused on a
	// bucket without Object Lock.
	if in.BypassGovernanceRetention != nil && !f.lock {
		return nil, &smithy.GenericAPIError{Code: "InvalidArgument", Message: "x-amz-bypass-governance-retention is only applicable to Object Lock enabled buckets."}
	}
	out := &svcsdk.DeleteObjectsOutput{}
	for _, o := range in.Delete.Objects {
		id := aws.ToString(o.Key) + "@" + aws.ToString(o.VersionId)
		if f.locked[id] {
			out.Errors = append(out.Errors, svcsdktypes.Error{Key: o.Key, VersionId: o.VersionId, Code: aws.String("AccessDenied"), Message: aws.String("Access Denied because object protected by object lock.")})
		}
	}
	return out, nil
}

func (f *fakeS3) ListMultipartUploads(context.Context, *svcsdk.ListMultipartUploadsInput, ...func(*svcsdk.Options)) (*svcsdk.ListMultipartUploadsOutput, error) {
	out := &svcsdk.ListMultipartUploadsOutput{}
	for _, u := range f.uploads {
		out.Uploads = append(out.Uploads, svcsdktypes.MultipartUpload{Key: aws.String(u), UploadId: aws.String("id-" + u)})
	}
	return out, nil
}

func (f *fakeS3) AbortMultipartUpload(context.Context, *svcsdk.AbortMultipartUploadInput, ...func(*svcsdk.Options)) (*svcsdk.AbortMultipartUploadOutput, error) {
	return &svcsdk.AbortMultipartUploadOutput{}, nil
}

func TestEmpty(t *testing.T) {
	ctx := context.Background()
	t.Run("every page, in batches of at most 1,000, bypassing governance retention under Object Lock", func(t *testing.T) {
		f := &fakeS3{page: 1500, uploads: []string{"a", "b"}, lock: true}
		for i := 0; i < 2600; i++ {
			id := fmt.Sprint("v", i)
			if i%10 == 0 {
				id = fmt.Sprint("marker", i)
			}
			f.versions = append(f.versions, fmt.Sprintf("k%d@%s", i/3, id))
		}
		n, err := Empty(ctx, f, "b")
		if err != nil {
			t.Fatal(err)
		}
		if n.Versions != 2600 || n.Uploads != 2 {
			t.Fatalf("emptied %+v", n)
		}
		if want := []int{1000, 500, 1000, 100}; fmt.Sprint(f.batches) != fmt.Sprint(want) {
			t.Fatalf("batches %v, want %v", f.batches, want)
		}
		for _, b := range f.bypass {
			if !b {
				t.Fatal("a DeleteObjects call without BypassGovernanceRetention")
			}
		}
	})
	t.Run("without Object Lock the deletes do not ask to bypass governance retention", func(t *testing.T) {
		f := &fakeS3{page: 10, versions: []string{"data/report.csv@1", "data/report.csv@2", "logs/a@marker3"}, uploads: []string{"big/upload"}}
		n, err := Empty(ctx, f, "b")
		if err != nil {
			t.Fatal(err)
		}
		if n.Versions != 3 || n.Uploads != 1 {
			t.Fatalf("emptied %+v", n)
		}
		for _, b := range f.bypass {
			if b {
				t.Fatal("a DeleteObjects call with BypassGovernanceRetention on a bucket without Object Lock")
			}
		}
	})
	t.Run("a bucket already gone is empty", func(t *testing.T) {
		n, err := Empty(ctx, &fakeS3{gone: true, page: 10}, "b")
		if err != nil || n != (Emptied{}) {
			t.Fatalf("%+v %v", n, err)
		}
	})
	t.Run("a locked version is an error after the rest are deleted", func(t *testing.T) {
		f := &fakeS3{page: 10, versions: []string{"a@1", "b@2", "c@3"}, locked: map[string]bool{"b@2": true}, lock: true}
		n, err := Empty(ctx, f, "b")
		if err == nil || !strings.Contains(err.Error(), "b (version 2)") {
			t.Fatalf("error %v", err)
		}
		if n.Versions != 2 {
			t.Fatalf("emptied %+v", n)
		}
	})
}

func TestWanted(t *testing.T) {
	for v, want := range map[string]bool{"true": true, "TRUE": true, " true ": true, "false": false} {
		u := &unstructured.Unstructured{}
		u.SetAnnotations(map[string]string{AnnotationForceDestroy: v})
		got, err := Wanted(u)
		if err != nil || got != want {
			t.Errorf("Wanted(%q) = %v, %v", v, got, err)
		}
	}
	u := &unstructured.Unstructured{}
	if got, err := Wanted(u); got || err != nil {
		t.Errorf("no annotation: %v, %v", got, err)
	}
	u.SetAnnotations(map[string]string{AnnotationForceDestroy: "yes"})
	if got, err := Wanted(u); got || err == nil {
		t.Errorf("yes: %v, %v", got, err)
	}
}
