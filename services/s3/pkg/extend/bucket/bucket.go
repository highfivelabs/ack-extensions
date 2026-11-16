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
// call, each naming its version) and aborts every multipart upload, and repeats that until ACK's own
// finalizer is gone (ACK's DeleteBucket has succeeded), then removes its
// own. On a bucket with Object Lock enabled the deletes bypass governance
// retention, as Terraform's force_destroy does; on any other bucket they
// must not ask to, since real S3 refuses the header there ("InvalidArgument:
// x-amz-bypass-governance-retention is only applicable to Object Lock
// enabled buckets", live run ackx-11160411-5872; moto and the AWS
// Emulator accept it). An object under compliance-mode Object Lock or a
// legal hold cannot be deleted by anyone: the error is recorded as an
// Event and retried.
//
// The same annotation acts on our fixed copy of ACK's Bucket
// (s3.aws.hfl.dev, docs/plan.md section 6.38), whose controller is
// ACK's runtime too, with its own finalizer: one controller per kind.
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
// +kubebuilder:rbac:groups=s3.aws.hfl.dev,resources=buckets,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

const (
	// AnnotationForceDestroy is "true" to empty the bucket on delete.
	AnnotationForceDestroy = "s3.aws.hfl.dev/force-destroy"
	// Finalizer is held while AnnotationForceDestroy is "true".
	Finalizer = "s3.aws.hfl.dev/force-destroy"
	// ACKFinalizer is ACK's own on its Bucket, removed once ACK's
	// DeleteBucket has succeeded (ACK's runtime: finalizers.<group>/<Kind>).
	ACKFinalizer = "finalizers.s3.services.k8s.aws/Bucket"
	// HFLFinalizer is ACK's runtime's on our copy of the Bucket.
	HFLFinalizer = "finalizers.s3.aws.hfl.dev/Bucket"

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

// GVK is ACK's Bucket; HFLGVK our fixed copy of it.
var (
	GVK    = schema.GroupVersionKind{Group: "s3.services.k8s.aws", Version: "v1alpha1", Kind: "Bucket"}
	HFLGVK = schema.GroupVersionKind{Group: "s3.aws.hfl.dev", Version: "v1alpha1", Kind: "Bucket"}
)

// Kind is one Bucket kind the extension acts on: its GVK, the finalizer
// its controller (ACK's runtime) removes once DeleteBucket has succeeded,
// and the name of the extension's controller for it.
type Kind struct {
	GVK          schema.GroupVersionKind
	ACKFinalizer string
	Name         string
}

// Kinds are ACK's Bucket and ours.
var Kinds = []Kind{
	{GVK: GVK, ACKFinalizer: ACKFinalizer, Name: Name},
	{GVK: HFLGVK, ACKFinalizer: HFLFinalizer, Name: Name + "-hfl"},
}

func init() { hflextend.Register("s3-bucket", Setup) }

// API is the part of S3's client the controller calls.
type API interface {
	GetObjectLockConfiguration(context.Context, *svcsdk.GetObjectLockConfigurationInput, ...func(*svcsdk.Options)) (*svcsdk.GetObjectLockConfigurationOutput, error)
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

// objectLocked reports whether the bucket has Object Lock enabled. A
// bucket without an Object Lock configuration answers
// ObjectLockConfigurationNotFoundError.
func objectLocked(ctx context.Context, api API, bucket string) (locked, gone bool, err error) {
	out, err := api.GetObjectLockConfiguration(ctx, &svcsdk.GetObjectLockConfigurationInput{Bucket: aws.String(bucket)})
	var ae smithy.APIError
	switch {
	case notFound(err):
		return false, true, nil
	case errors.As(err, &ae) && ae.ErrorCode() == "ObjectLockConfigurationNotFoundError":
		return false, false, nil
	case err != nil:
		return false, false, fmt.Errorf("GetObjectLockConfiguration: %w", err)
	}
	return out.ObjectLockConfiguration != nil && out.ObjectLockConfiguration.ObjectLockEnabled == svcsdktypes.ObjectLockEnabledEnabled, false, nil
}

// Empty deletes every object version and delete marker in the bucket and
// aborts every multipart upload. A bucket that does not exist is empty.
// The deletes bypass governance retention only when the bucket has Object
// Lock enabled (S3 refuses the header on any other bucket). Objects that
// cannot be deleted (compliance mode, a legal hold) make an error naming
// the first of them, after everything else is deleted.
func Empty(ctx context.Context, api API, bucket string) (Emptied, error) {
	var n Emptied
	locked, gone, err := objectLocked(ctx, api, bucket)
	if gone {
		return n, nil
	}
	if err != nil {
		return n, err
	}
	var bypass *bool
	if locked {
		bypass = aws.Bool(true)
	}
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
				BypassGovernanceRetention: bypass,
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

// Reconciler acts on one kind of Bucket (ACK's, unless Kind says ours).
type Reconciler struct {
	// Kind is the Bucket kind; zero means ACK's (Kinds[0]).
	Kind     Kind
	Env      hflextend.Env
	Client   client.Client
	Recorder record.EventRecorder
	// NewAPI makes an S3 client for a Region.
	NewAPI func(ctx context.Context, region string) (API, error)
}

// Setup adds a controller per Bucket kind to the manager.
func Setup(env hflextend.Env) error {
	for _, k := range Kinds {
		if err := setup(env, k); err != nil {
			return err
		}
	}
	return nil
}

func setup(env hflextend.Env, k Kind) error {
	r := &Reconciler{
		Kind:     k,
		Env:      env,
		Client:   env.Manager.GetClient(),
		Recorder: env.Manager.GetEventRecorderFor(k.Name),
		NewAPI: func(ctx context.Context, region string) (API, error) {
			cfg, err := env.AWSConfig(ctx, region, k.GVK)
			if err != nil {
				return nil, err
			}
			pathStyle := env.Config.UsePathStyle
			return svcsdk.NewFromConfig(cfg, func(o *svcsdk.Options) { o.UsePathStyle = pathStyle }), nil
		},
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(k.GVK)
	return ctrl.NewControllerManagedBy(env.Manager).Named(k.Name).For(u).Complete(r)
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

func (r *Reconciler) kind() Kind {
	if r.Kind.GVK.Empty() {
		return Kinds[0]
	}
	return r.Kind
}

// Reconcile holds or releases the finalizer, and empties a deleted bucket.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	k := r.kind()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(k.GVK)
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
	if !controllerutil.ContainsFinalizer(u, k.ACKFinalizer) {
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
