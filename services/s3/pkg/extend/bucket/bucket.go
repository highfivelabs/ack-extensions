// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package bucket extends ACK's own S3 Bucket (s3.services.k8s.aws) with
// what s3-controller lacks (docs/extensions.md; docs/plan.md section 5,
// "Gaps on ACK's own kinds"; open question 1, answered 2026-11-13):
//
//   - s3.aws.hfl.dev/force-destroy: "true" empties the bucket when the
//     Bucket is deleted, so ACK's DeleteBucket can succeed (Terraform's
//     force_destroy). ACK's controller calls DeleteBucket alone, which
//     fails with BucketNotEmpty and is retried for ever.
//
// How: while the annotation is "true", this controller holds a finalizer
// of its own (s3.aws.hfl.dev/force-destroy) on ACK's Bucket. When the
// Bucket is deleted, and ACK will delete the bucket (its deletion policy
// is delete, the Bucket is not read-only, ACK made or adopted it), it
// deletes every object version and delete marker (DeleteObjects, 1,000 a
// call, with BypassGovernanceRetention as Terraform's force_destroy does)
// and aborts every multipart upload, and repeats that until ACK's own
// finalizer is gone (ACK's DeleteBucket has succeeded), then removes its
// own. An object under compliance-mode Object Lock or a legal hold cannot
// be deleted by anyone: the error is recorded as an Event and retried.
//
// ACK's Bucket is read as unstructured: s3-controller is not a Go module
// of this repository (a new upstream dependency is the builder's call;
// see docs/plan.md open question 16 for ec2-controller), and only
// spec.name, the status's ackResourceMetadata and the finalizers are read.
package bucket

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/s3"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	hflextend "github.com/highfivelabs/ack-extensions/pkg/extend"
)

// +kubebuilder:rbac:groups=s3.services.k8s.aws,resources=buckets,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

const (
	// AnnotationForceDestroy is "true" to empty the bucket on delete.
	AnnotationForceDestroy = "s3.aws.hfl.dev/force-destroy"
	// Finalizer is held while AnnotationForceDestroy is "true".
	Finalizer = "s3.aws.hfl.dev/force-destroy"
	// ACKFinalizer is ACK's own on its Bucket, removed once ACK's
	// DeleteBucket has succeeded (ACK's runtime: finalizers.<group>/<Kind>).
	ACKFinalizer = "finalizers.s3.services.k8s.aws/Bucket"

	// Name is the controller's name, and the Events' source.
	Name = "ack-extensions-s3-bucket"
)

// Event reasons.
const (
	ReasonEmptied       = "BucketEmptied"
	ReasonNotEmptied    = "BucketNotEmptied"
	ReasonEmptyFailed   = "BucketEmptyFailed"
	ReasonNotActed      = "ExtensionNotApplied"
	ReasonInvalidAnnot  = "InvalidAnnotation"
	deleteBatch         = 1000
	retryWhileACKDelete = 5 * time.Second
)

// GVK is ACK's Bucket.
var GVK = schema.GroupVersionKind{Group: "s3.services.k8s.aws", Version: "v1alpha1", Kind: "Bucket"}

func init() { hflextend.Register("s3-bucket", Setup) }

// API is the part of S3's client the controller calls.
type API interface {
	ListObjectVersions(context.Context, *svcsdk.ListObjectVersionsInput, ...func(*svcsdk.Options)) (*svcsdk.ListObjectVersionsOutput, error)
	DeleteObjects(context.Context, *svcsdk.DeleteObjectsInput, ...func(*svcsdk.Options)) (*svcsdk.DeleteObjectsOutput, error)
	ListMultipartUploads(context.Context, *svcsdk.ListMultipartUploadsInput, ...func(*svcsdk.Options)) (*svcsdk.ListMultipartUploadsOutput, error)
	AbortMultipartUpload(context.Context, *svcsdk.AbortMultipartUploadInput, ...func(*svcsdk.Options)) (*svcsdk.AbortMultipartUploadOutput, error)
}

// Emptied counts what Empty removed.
type Emptied struct {
	Versions, Uploads int
}

func notFound(err error) bool {
	var ae smithy.APIError
	return errors.As(err, &ae) && (ae.ErrorCode() == "NoSuchBucket" || ae.ErrorCode() == "NotFound")
}

// Empty deletes every object version and delete marker in the bucket and
// aborts every multipart upload. A bucket that does not exist is empty.
// Objects that cannot be deleted (Object Lock) make an error naming the
// first of them, after everything else is deleted.
func Empty(ctx context.Context, api API, bucket string) (Emptied, error) {
	var n Emptied
	var keyMarker, versionMarker *string
	var undeletable []string
	for {
		out, err := api.ListObjectVersions(ctx, &svcsdk.ListObjectVersionsInput{
			Bucket: aws.String(bucket), KeyMarker: keyMarker, VersionIdMarker: versionMarker,
		})
		if notFound(err) {
			return n, nil
		}
		if err != nil {
			return n, fmt.Errorf("ListObjectVersions: %w", err)
		}
		var ids []svcsdktypes.ObjectIdentifier
		for _, v := range out.Versions {
			ids = append(ids, svcsdktypes.ObjectIdentifier{Key: v.Key, VersionId: v.VersionId})
		}
		for _, m := range out.DeleteMarkers {
			ids = append(ids, svcsdktypes.ObjectIdentifier{Key: m.Key, VersionId: m.VersionId})
		}
		for len(ids) > 0 {
			batch := ids
			if len(batch) > deleteBatch {
				batch = ids[:deleteBatch]
			}
			ids = ids[len(batch):]
			del, err := api.DeleteObjects(ctx, &svcsdk.DeleteObjectsInput{
				Bucket:                    aws.String(bucket),
				Delete:                    &svcsdktypes.Delete{Objects: batch, Quiet: aws.Bool(true)},
				BypassGovernanceRetention: aws.Bool(true),
			})
			if notFound(err) {
				return n, nil
			}
			if err != nil {
				return n, fmt.Errorf("DeleteObjects: %w", err)
			}
			n.Versions += len(batch) - len(del.Errors)
			for _, e := range del.Errors {
				undeletable = append(undeletable, fmt.Sprintf("%s (version %s): %s %s", aws.ToString(e.Key), aws.ToString(e.VersionId), aws.ToString(e.Code), aws.ToString(e.Message)))
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		keyMarker, versionMarker = out.NextKeyMarker, out.NextVersionIdMarker
	}

	var uploadKey, uploadID *string
	for {
		out, err := api.ListMultipartUploads(ctx, &svcsdk.ListMultipartUploadsInput{
			Bucket: aws.String(bucket), KeyMarker: uploadKey, UploadIdMarker: uploadID,
		})
		if notFound(err) {
			return n, nil
		}
		if err != nil {
			return n, fmt.Errorf("ListMultipartUploads: %w", err)
		}
		for _, u := range out.Uploads {
			_, err := api.AbortMultipartUpload(ctx, &svcsdk.AbortMultipartUploadInput{Bucket: aws.String(bucket), Key: u.Key, UploadId: u.UploadId})
			var nsu *svcsdktypes.NoSuchUpload
			if err != nil && !errors.As(err, &nsu) && !notFound(err) {
				return n, fmt.Errorf("AbortMultipartUpload: %w", err)
			}
			n.Uploads++
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		uploadKey, uploadID = out.NextKeyMarker, out.NextUploadIdMarker
	}
	if len(undeletable) > 0 {
		return n, fmt.Errorf("%d object version(s) could not be deleted, first %s", len(undeletable), undeletable[0])
	}
	return n, nil
}

// Reconciler acts on ACK's Buckets.
type Reconciler struct {
	Env      hflextend.Env
	Client   client.Client
	Recorder record.EventRecorder
	// NewAPI makes an S3 client for a Region.
	NewAPI func(ctx context.Context, region string) (API, error)
}

// Setup adds the controller to the manager.
func Setup(env hflextend.Env) error {
	r := &Reconciler{
		Env:      env,
		Client:   env.Manager.GetClient(),
		Recorder: env.Manager.GetEventRecorderFor(Name),
		NewAPI: func(ctx context.Context, region string) (API, error) {
			cfg, err := env.AWSConfig(ctx, region, GVK)
			if err != nil {
				return nil, err
			}
			pathStyle := env.Config.UsePathStyle
			return svcsdk.NewFromConfig(cfg, func(o *svcsdk.Options) { o.UsePathStyle = pathStyle }), nil
		},
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(GVK)
	return ctrl.NewControllerManagedBy(env.Manager).Named(Name).For(u).Complete(r)
}

// Wanted reports whether the Bucket asks to be emptied on delete.
func Wanted(obj client.Object) (bool, error) {
	v, ok := obj.GetAnnotations()[AnnotationForceDestroy]
	if !ok {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, fmt.Errorf("%s: %q is neither true nor false; the bucket will not be emptied", AnnotationForceDestroy, v)
}

func metadataOf(u *unstructured.Unstructured) hflextend.ACKMetadata {
	region, _, _ := unstructured.NestedString(u.Object, "status", "ackResourceMetadata", "region")
	owner, _, _ := unstructured.NestedString(u.Object, "status", "ackResourceMetadata", "ownerAccountID")
	return hflextend.ACKMetadata{Region: region, OwnerAccountID: owner}
}

func (r *Reconciler) setFinalizer(ctx context.Context, u *unstructured.Unstructured, want bool) error {
	has := controllerutil.ContainsFinalizer(u, Finalizer)
	if has == want {
		return nil
	}
	base := u.DeepCopy()
	if want {
		controllerutil.AddFinalizer(u, Finalizer)
	} else {
		controllerutil.RemoveFinalizer(u, Finalizer)
	}
	// An optimistic-lock merge patch: ACK's controller writes the same
	// list, and a conflict is retried with a fresh read.
	return r.Client.Patch(ctx, u, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

// Reconcile holds or releases the finalizer, and empties a deleted bucket.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(GVK)
	if err := r.Client.Get(ctx, req.NamespacedName, u); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	want, perr := Wanted(u)
	if perr != nil {
		r.Recorder.Event(u, corev1.EventTypeWarning, ReasonInvalidAnnot, perr.Error())
	}

	if u.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, r.setFinalizer(ctx, u, want)
	}
	if !controllerutil.ContainsFinalizer(u, Finalizer) {
		return ctrl.Result{}, nil
	}
	release := func(kind, reason, msg string) (ctrl.Result, error) {
		if msg != "" {
			r.Recorder.Event(u, kind, reason, msg)
		}
		return ctrl.Result{}, r.setFinalizer(ctx, u, false)
	}
	if !want {
		return release("", "", "")
	}
	if hflextend.ReadOnly(u) {
		return release(corev1.EventTypeNormal, ReasonNotEmptied, "the Bucket is read-only (services.k8s.aws/read-only): the bucket is not emptied")
	}
	policy, err := r.Env.DeletionPolicy(ctx, u, "s3")
	if err != nil {
		return ctrl.Result{}, err
	}
	if policy == ackv1alpha1.DeletionPolicyRetain {
		return release(corev1.EventTypeNormal, ReasonNotEmptied, "the deletion policy is retain: ACK keeps the bucket, so it is not emptied")
	}
	md := metadataOf(u)
	if !md.Made() {
		// ACK never made the bucket: there is nothing to empty.
		return release("", "", "")
	}
	if why := r.Env.OtherAccount(md); why != "" {
		return release(corev1.EventTypeWarning, ReasonNotActed, why+"; the bucket is not emptied")
	}
	if !controllerutil.ContainsFinalizer(u, ACKFinalizer) {
		// ACK's DeleteBucket has succeeded.
		return release("", "", "")
	}
	name, _, _ := unstructured.NestedString(u.Object, "spec", "name")
	if name == "" {
		return release(corev1.EventTypeWarning, ReasonNotEmptied, "spec.name is empty")
	}
	api, err := r.NewAPI(ctx, md.Region)
	if err != nil {
		return ctrl.Result{}, err
	}
	n, err := Empty(ctx, api, name)
	if n.Versions > 0 || n.Uploads > 0 {
		r.Recorder.Event(u, corev1.EventTypeNormal, ReasonEmptied, fmt.Sprintf("deleted %d object version(s) and delete marker(s) and aborted %d multipart upload(s) in %s, for ACK's DeleteBucket", n.Versions, n.Uploads, name))
	}
	if err != nil {
		r.Recorder.Event(u, corev1.EventTypeWarning, ReasonEmptyFailed, err.Error())
		return ctrl.Result{}, err
	}
	// Again until ACK's DeleteBucket succeeds: an object written meanwhile
	// is deleted on the next pass.
	return ctrl.Result{RequeueAfter: retryWhileACKDelete}, nil
}
