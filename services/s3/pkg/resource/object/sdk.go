// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package object

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/s3"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/s3/apis/v1alpha1"
)

// AnnotationLastContentSHA256 is the SHA-256 (hex) of the content last
// put, kept on the object as ElastiCache's ReplicationGroup keeps its auth
// token's (docs/plan.md section 6.47): a ConfigMap's or Secret's value
// that changed, with contentFrom unchanged, differs from it, and the
// object is put again.
const AnnotationLastContentSHA256 = "s3.aws.hfl.dev/last-content-sha256"

func isAPIError(err error, codes ...string) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	for _, c := range codes {
		if apiErr.ErrorCode() == c {
			return true
		}
	}
	return false
}

// notFound is S3's answer for an object or bucket that is not there;
// HeadObject has no body, so its 404 comes as NotFound.
func notFound(err error) bool {
	return isAPIError(err, "NotFound", "NoSuchKey", "NoSuchBucket")
}

// body is the content the spec asks for: content as text, or
// contentBase64 decoded (contentFrom is resolved into contentBase64,
// references.go).
func body(ko *svcapitypes.Object) ([]byte, error) {
	switch {
	case ko.Spec.Content != nil:
		return []byte(*ko.Spec.Content), nil
	case ko.Spec.ContentBase64 != nil:
		b, err := base64.StdEncoding.DecodeString(*ko.Spec.ContentBase64)
		if err != nil {
			return nil, ackerr.NewTerminalError(fmt.Errorf("spec.contentBase64 is not base64: %w", err))
		}
		return b, nil
	}
	return nil, ackerr.NewTerminalError(fmt.Errorf("no content: one of spec.content, spec.contentBase64 and spec.contentFrom is required"))
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (rm *resourceManager) requiredFieldsMissingFromReadOneInput(r *resource) bool {
	return aws.ToString(r.ko.Spec.BucketName) == "" || aws.ToString(r.ko.Spec.Key) == ""
}

// optional is have where want is set, and nil where it is not: a field
// the spec leaves out takes S3's default, which is no drift.
func optional(want *string, have *string) *string {
	if want == nil {
		return nil
	}
	return aws.String(aws.ToString(have))
}

// sdkFind reads the object with HeadObject and its tags with
// GetObjectTagging. The content is not read back: while the ETag is the
// one last put and the desired content's SHA-256 is the one last put, the
// content is desired's; otherwise it is cleared, which is a delta, and the
// object is put again.
func (rm *resourceManager) sdkFind(ctx context.Context, r *resource) (latest *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkFind")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, ackerr.NotFound
	}
	bucket, key := r.ko.Spec.BucketName, r.ko.Spec.Key
	head, err := rm.sdkapi.HeadObject(ctx, &svcsdk.HeadObjectInput{Bucket: bucket, Key: key})
	rm.metrics.RecordAPICall("READ_ONE", "HeadObject", err)
	if err != nil {
		if notFound(err) {
			return nil, ackerr.NotFound
		}
		return nil, err
	}
	ko := r.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	rm.setARN(ko)

	want, err := body(r.ko)
	if err != nil {
		return nil, err
	}
	sameETag := r.ko.Status.ETag != nil && aws.ToString(head.ETag) == *r.ko.Status.ETag
	sameContent := r.ko.GetAnnotations()[AnnotationLastContentSHA256] == sha(want)
	if !sameETag || !sameContent {
		ko.Spec.Content, ko.Spec.ContentBase64 = nil, nil
	}

	s := r.ko.Spec
	ko.Spec.ContentType = optional(s.ContentType, head.ContentType)
	ko.Spec.CacheControl = optional(s.CacheControl, head.CacheControl)
	ko.Spec.ContentDisposition = optional(s.ContentDisposition, head.ContentDisposition)
	ko.Spec.ContentEncoding = optional(s.ContentEncoding, head.ContentEncoding)
	ko.Spec.ContentLanguage = optional(s.ContentLanguage, head.ContentLanguage)
	if s.ServerSideEncryption != nil {
		ko.Spec.ServerSideEncryption = aws.String(string(head.ServerSideEncryption))
	}
	// S3 returns the key's ARN whatever form was put: compared only when
	// the spec gives an ARN.
	if s.KMSKeyID != nil && strings.HasPrefix(*s.KMSKeyID, "arn:") {
		ko.Spec.KMSKeyID = aws.String(aws.ToString(head.SSEKMSKeyId))
	}
	if s.StorageClass != nil {
		sc := string(head.StorageClass)
		if sc == "" {
			// HeadObject names no class for STANDARD.
			sc = string(svcsdktypes.StorageClassStandard)
		}
		ko.Spec.StorageClass = aws.String(sc)
	}
	ko.Spec.Metadata = nil
	if len(head.Metadata) > 0 {
		ko.Spec.Metadata = map[string]*string{}
		for k, v := range head.Metadata {
			ko.Spec.Metadata[k] = aws.String(v)
		}
	}

	tags, err := rm.sdkapi.GetObjectTagging(ctx, &svcsdk.GetObjectTaggingInput{Bucket: bucket, Key: key})
	rm.metrics.RecordAPICall("READ_ONE", "GetObjectTagging", err)
	if err != nil {
		return nil, err
	}
	ko.Spec.Tags = nil
	for _, t := range tags.TagSet {
		ko.Spec.Tags = append(ko.Spec.Tags, &svcapitypes.Tag{Key: t.Key, Value: t.Value})
	}
	return &resource{ko}, nil
}

// setARN names the object as S3 does: arn:<partition>:s3:::bucket/key.
func (rm *resourceManager) setARN(ko *svcapitypes.Object) {
	arn := ackv1alpha1.AWSResourceName(rm.ARNFromName(aws.ToString(ko.Spec.BucketName) + "/" + aws.ToString(ko.Spec.Key)))
	ko.Status.ACKResourceMetadata.ARN = &arn
}

// tagging is the tags as PutObject's Tagging header takes them.
func tagging(tags []*svcapitypes.Tag) *string {
	if len(tags) == 0 {
		return nil
	}
	v := url.Values{}
	for _, t := range tags {
		v.Set(aws.ToString(t.Key), aws.ToString(t.Value))
	}
	return aws.String(v.Encode())
}

// put puts the whole object (content, headers, metadata, tags) and
// returns the object as it now stands.
func (rm *resourceManager) put(ctx context.Context, desired *resource, op string) (*resource, error) {
	b, err := body(desired.ko)
	if err != nil {
		return nil, err
	}
	s := desired.ko.Spec
	in := &svcsdk.PutObjectInput{
		Bucket:             s.BucketName,
		Key:                s.Key,
		Body:               bytes.NewReader(b),
		ContentLength:      aws.Int64(int64(len(b))),
		ContentType:        s.ContentType,
		CacheControl:       s.CacheControl,
		ContentDisposition: s.ContentDisposition,
		ContentEncoding:    s.ContentEncoding,
		ContentLanguage:    s.ContentLanguage,
		SSEKMSKeyId:        s.KMSKeyID,
		Tagging:            tagging(s.Tags),
	}
	if s.ServerSideEncryption != nil {
		in.ServerSideEncryption = svcsdktypes.ServerSideEncryption(*s.ServerSideEncryption)
	}
	if s.StorageClass != nil {
		in.StorageClass = svcsdktypes.StorageClass(*s.StorageClass)
	}
	if len(s.Metadata) > 0 {
		in.Metadata = map[string]string{}
		for k, v := range s.Metadata {
			in.Metadata[k] = aws.ToString(v)
		}
	}
	out, err := rm.sdkapi.PutObject(ctx, in)
	rm.metrics.RecordAPICall(op, "PutObject", err)
	if err != nil {
		return nil, err
	}
	ko := desired.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	rm.setARN(ko)
	ko.Status.ETag = out.ETag
	ko.Status.VersionID = out.VersionId
	ann := map[string]string{}
	for k, v := range ko.GetAnnotations() {
		ann[k] = v
	}
	ann[AnnotationLastContentSHA256] = sha(b)
	ko.SetAnnotations(ann)
	return &resource{ko}, nil
}

func (rm *resourceManager) sdkCreate(ctx context.Context, desired *resource) (created *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkCreate")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(desired) {
		return nil, ackerr.NewTerminalError(fmt.Errorf("spec.bucketName (or bucketRef) and spec.key are required"))
	}
	return rm.put(ctx, desired, "CREATE")
}

// sdkUpdate puts the object again for any change but its tags, which
// PutObjectTagging (or DeleteObjectTagging) changes alone: S3 changes an
// object's content, headers and metadata only by a new put. The bucket
// and the key are immutable (the CRD refuses a change).
func (rm *resourceManager) sdkUpdate(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta interface{},
) (updated *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkUpdate")
	defer func() { exit(err) }()
	if aws.ToString(desired.ko.Spec.BucketName) != aws.ToString(latest.ko.Spec.BucketName) ||
		aws.ToString(desired.ko.Spec.Key) != aws.ToString(latest.ko.Spec.Key) {
		return nil, ackerr.NewTerminalError(fmt.Errorf("an Object's bucket and key cannot change; delete it and create another"))
	}
	d := newResourceDelta(desired, latest)
	tagsOnly := true
	for _, diff := range d.Differences {
		if !diff.Path.Contains("Spec.Tags") {
			tagsOnly = false
		}
	}
	if !tagsOnly {
		return rm.put(ctx, desired, "UPDATE")
	}
	s := desired.ko.Spec
	if len(s.Tags) == 0 {
		_, err = rm.sdkapi.DeleteObjectTagging(ctx, &svcsdk.DeleteObjectTaggingInput{Bucket: s.BucketName, Key: s.Key})
		rm.metrics.RecordAPICall("UPDATE", "DeleteObjectTagging", err)
	} else {
		var set []svcsdktypes.Tag
		for _, t := range s.Tags {
			set = append(set, svcsdktypes.Tag{Key: t.Key, Value: aws.String(aws.ToString(t.Value))})
		}
		_, err = rm.sdkapi.PutObjectTagging(ctx, &svcsdk.PutObjectTaggingInput{Bucket: s.BucketName, Key: s.Key, Tagging: &svcsdktypes.Tagging{TagSet: set}})
		rm.metrics.RecordAPICall("UPDATE", "PutObjectTagging", err)
	}
	if err != nil {
		return nil, err
	}
	ko := desired.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	rm.setARN(ko)
	ko.Status.ETag, ko.Status.VersionID = latest.ko.Status.ETag, latest.ko.Status.VersionID
	return &resource{ko}, nil
}

// sdkDelete deletes the object: in a bucket with versioning, its current
// version (S3 then keeps a delete marker and the older versions, as
// Terraform's aws_s3_object does without force_destroy). An object or
// bucket already gone is deleted.
func (rm *resourceManager) sdkDelete(ctx context.Context, r *resource) (deleted *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkDelete")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, nil
	}
	_, err = rm.sdkapi.DeleteObject(ctx, &svcsdk.DeleteObjectInput{Bucket: r.ko.Spec.BucketName, Key: r.ko.Spec.Key})
	rm.metrics.RecordAPICall("DELETE", "DeleteObject", err)
	if err != nil && !notFound(err) {
		return nil, err
	}
	return nil, nil
}

// equalMetadata compares user metadata; S3 keeps the keys in lower case.
func equalMetadata(a, b map[string]*string) bool {
	norm := func(m map[string]*string) map[string]string {
		out := map[string]string{}
		for k, v := range m {
			out[strings.ToLower(k)] = aws.ToString(v)
		}
		return out
	}
	na, nb := norm(a), norm(b)
	if len(na) != len(nb) {
		return false
	}
	for k, v := range na {
		if w, ok := nb[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// equalTags compares tags as a set.
func equalTags(a, b []*svcapitypes.Tag) bool {
	flat := func(ts []*svcapitypes.Tag) []string {
		var out []string
		for _, t := range ts {
			out = append(out, aws.ToString(t.Key)+"="+aws.ToString(t.Value))
		}
		sort.Strings(out)
		return out
	}
	fa, fb := flat(a), flat(b)
	if len(fa) != len(fb) {
		return false
	}
	for i := range fa {
		if fa[i] != fb[i] {
			return false
		}
	}
	return true
}

func (rm *resourceManager) setStatusDefaults(ko *svcapitypes.Object) {
	if ko.Status.ACKResourceMetadata == nil {
		ko.Status.ACKResourceMetadata = &ackv1alpha1.ResourceMetadata{}
	}
	if ko.Status.ACKResourceMetadata.Region == nil {
		ko.Status.ACKResourceMetadata.Region = &rm.awsRegion
	}
	if ko.Status.ACKResourceMetadata.Partition == nil {
		ko.Status.ACKResourceMetadata.Partition = &rm.awsPartition
	}
	if ko.Status.ACKResourceMetadata.OwnerAccountID == nil {
		ko.Status.ACKResourceMetadata.OwnerAccountID = &rm.awsAccountID
	}
	if ko.Status.Conditions == nil {
		ko.Status.Conditions = []*ackv1alpha1.Condition{}
	}
}

// updateConditions sets the Terminal and Recoverable conditions as ACK's
// generated resources do (code-generator templates/pkg/resource/sdk.go.tpl)
// and leaves ACK.ResourceSynced to the runtime, which then asks IsSynced.
func (rm *resourceManager) updateConditions(r *resource, onSuccess bool, err error) (*resource, bool) {
	ko := r.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	var terminalCondition, recoverableCondition, syncCondition *ackv1alpha1.Condition
	for _, condition := range ko.Status.Conditions {
		switch condition.Type {
		case ackv1alpha1.ConditionTypeTerminal:
			terminalCondition = condition
		case ackv1alpha1.ConditionTypeRecoverable:
			recoverableCondition = condition
		case ackv1alpha1.ConditionTypeResourceSynced:
			syncCondition = condition
		}
	}
	var termError *ackerr.TerminalError
	if rm.terminalAWSError(err) || errors.As(err, &termError) {
		if terminalCondition == nil {
			terminalCondition = &ackv1alpha1.Condition{Type: ackv1alpha1.ConditionTypeTerminal}
			ko.Status.Conditions = append(ko.Status.Conditions, terminalCondition)
		}
		msg := err.Error()
		if awsErr, _ := ackerr.AWSError(err); awsErr != nil {
			msg = awsErr.Error()
		}
		terminalCondition.Status = corev1.ConditionTrue
		terminalCondition.Message = &msg
	} else {
		if terminalCondition != nil {
			terminalCondition.Status = corev1.ConditionFalse
			terminalCondition.Message = nil
		}
		if err != nil {
			if recoverableCondition == nil {
				recoverableCondition = &ackv1alpha1.Condition{Type: ackv1alpha1.ConditionTypeRecoverable}
				ko.Status.Conditions = append(ko.Status.Conditions, recoverableCondition)
			}
			msg := err.Error()
			if awsErr, _ := ackerr.AWSError(err); awsErr != nil {
				msg = awsErr.Error()
			}
			recoverableCondition.Status = corev1.ConditionTrue
			recoverableCondition.Message = &msg
		} else if recoverableCondition != nil {
			recoverableCondition.Status = corev1.ConditionFalse
			recoverableCondition.Message = nil
		}
	}
	if terminalCondition != nil || recoverableCondition != nil || syncCondition != nil {
		return &resource{ko}, true
	}
	return nil, false
}

// terminalAWSError: errors no retry fixes: a request S3 refuses as it
// stands (a bad header, storage class or KMS key, a tag S3 refuses). A
// bucket that does not exist yet is not terminal: it may be made after
// this object.
func (rm *resourceManager) terminalAWSError(err error) bool {
	return isAPIError(err, "InvalidArgument", "InvalidStorageClass", "InvalidRequest", "InvalidTag", "EntityTooLarge", "KMS.NotFoundException")
}
