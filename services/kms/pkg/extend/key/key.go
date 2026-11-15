// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package key extends ACK's own KMS Key (kms.services.k8s.aws) with what
// kms-controller lacks (docs/extensions.md; docs/plan.md section 5, "Gaps
// on ACK's own kinds"; open question 1, answered 2026-11-13), as
// annotations on ACK's Key that this controller acts on:
//
//   - kms.aws.hfl.dev/rotation-period-in-days: the automatic rotation
//     period, 90 to 2560 days (EnableKeyRotation's RotationPeriodInDays;
//     Terraform's rotation_period_in_days). It applies while ACK's
//     spec.enableKeyRotation is true; ACK turns rotation on and off, this
//     sets its period. Without it AWS's default, 365 days, holds.
//   - kms.aws.hfl.dev/enabled: "true" or "false", the key enabled or
//     disabled (EnableKey, DisableKey; Terraform's is_enabled). Without it
//     the key's state is left as it is. ACK's Key reports status.enabled
//     only.
//
// The deletion window needs nothing here: ACK's Key already takes it as
// the annotation kms.services.k8s.aws/pending-window-in-days (7 to 30;
// default 7), which ScheduleKeyDeletion is called with.
//
// ACK's controller is left alone: this one waits until ACK has made the
// key (status.keyID), never writes ACK's spec or status, and records what
// it did, or why it did nothing, as Events on the Key. It acts only in the
// manager's own account (not through CARM), and not on a read-only Key.
package key

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	ackkms "github.com/aws-controllers-k8s/kms-controller/apis/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/kms"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	hflextend "github.com/highfivelabs/ack-extensions/pkg/extend"
)

// +kubebuilder:rbac:groups=kms.services.k8s.aws,resources=keys,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

const (
	// AnnotationPrefix is our group's, on ACK's object.
	AnnotationPrefix = "kms.aws.hfl.dev/"
	// AnnotationRotationPeriod is the rotation period in days.
	AnnotationRotationPeriod = AnnotationPrefix + "rotation-period-in-days"
	// AnnotationEnabled is "true" or "false".
	AnnotationEnabled = AnnotationPrefix + "enabled"

	// MinRotationPeriod and MaxRotationPeriod bound RotationPeriodInDays
	// (the KMS API reference, EnableKeyRotation).
	MinRotationPeriod = 90
	MaxRotationPeriod = 2560

	// Name is the controller's name, and the Events' source.
	Name = "ack-extensions-kms-key"
)

// Event reasons.
const (
	ReasonRotationPeriodSet     = "RotationPeriodSet"
	ReasonRotationPeriodIgnored = "RotationPeriodIgnored"
	ReasonKeyEnabled            = "KeyEnabled"
	ReasonKeyDisabled           = "KeyDisabled"
	ReasonInvalidAnnotation     = "InvalidAnnotation"
	ReasonNotActed              = "ExtensionNotApplied"
	ReasonAWSError              = "ExtensionAWSError"
)

// GVK is ACK's Key.
var GVK = schema.GroupVersionKind{Group: "kms.services.k8s.aws", Version: "v1alpha1", Kind: "Key"}

func init() { hflextend.Register("kms-key", Setup) }

// Wanted is what a Key's annotations ask for.
type Wanted struct {
	// RotationPeriod is the period in days, or 0 for none asked.
	RotationPeriod int32
	// Enabled is nil when the annotation is absent.
	Enabled *bool
}

// Any reports whether anything is asked.
func (w Wanted) Any() bool { return w.RotationPeriod != 0 || w.Enabled != nil }

// Parse reads a Key's annotations. An annotation that is set but not
// valid is an error naming it.
func Parse(annotations map[string]string) (Wanted, error) {
	var w Wanted
	var errs []error
	if v, ok := annotations[AnnotationRotationPeriod]; ok {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %q is not a whole number of days", AnnotationRotationPeriod, v))
		case n < MinRotationPeriod || n > MaxRotationPeriod:
			errs = append(errs, fmt.Errorf("%s: %d is outside %d to %d days", AnnotationRotationPeriod, n, MinRotationPeriod, MaxRotationPeriod))
		default:
			w.RotationPeriod = int32(n)
		}
	}
	if v, ok := annotations[AnnotationEnabled]; ok {
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %q is neither true nor false", AnnotationEnabled, v))
		} else {
			w.Enabled = &b
		}
	}
	return w, errors.Join(errs...)
}

// API is the part of KMS's client the controller calls.
type API interface {
	DescribeKey(context.Context, *svcsdk.DescribeKeyInput, ...func(*svcsdk.Options)) (*svcsdk.DescribeKeyOutput, error)
	GetKeyRotationStatus(context.Context, *svcsdk.GetKeyRotationStatusInput, ...func(*svcsdk.Options)) (*svcsdk.GetKeyRotationStatusOutput, error)
	EnableKeyRotation(context.Context, *svcsdk.EnableKeyRotationInput, ...func(*svcsdk.Options)) (*svcsdk.EnableKeyRotationOutput, error)
	EnableKey(context.Context, *svcsdk.EnableKeyInput, ...func(*svcsdk.Options)) (*svcsdk.EnableKeyOutput, error)
	DisableKey(context.Context, *svcsdk.DisableKeyInput, ...func(*svcsdk.Options)) (*svcsdk.DisableKeyOutput, error)
}

// Done is what Apply did, for Events.
type Done struct {
	Reason, Message string
}

// Apply makes the key what w asks: the rotation period while rotation is
// on (rotationOn, ACK's spec.enableKeyRotation), and the key enabled or
// disabled. It reads before it writes, so a key already as asked is not
// called again. retry is true when the key is in a state that cannot
// change yet (being made, pending import).
func Apply(ctx context.Context, api API, keyID string, rotationOn bool, w Wanted) (done []Done, retry bool, err error) {
	desc, err := api.DescribeKey(ctx, &svcsdk.DescribeKeyInput{KeyId: aws.String(keyID)})
	if err != nil {
		return nil, false, fmt.Errorf("DescribeKey: %w", err)
	}
	state := desc.KeyMetadata.KeyState
	switch state {
	case svcsdktypes.KeyStatePendingDeletion, svcsdktypes.KeyStatePendingReplicaDeletion:
		// Being deleted: nothing to set.
		return nil, false, nil
	case svcsdktypes.KeyStateCreating, svcsdktypes.KeyStatePendingImport, svcsdktypes.KeyStateUpdating, svcsdktypes.KeyStateUnavailable:
		return nil, true, nil
	}

	if w.Enabled != nil {
		switch {
		case *w.Enabled && state == svcsdktypes.KeyStateDisabled:
			if _, err := api.EnableKey(ctx, &svcsdk.EnableKeyInput{KeyId: aws.String(keyID)}); err != nil {
				return done, false, fmt.Errorf("EnableKey: %w", err)
			}
			done = append(done, Done{ReasonKeyEnabled, "enabled the key (" + AnnotationEnabled + ": true)"})
			state = svcsdktypes.KeyStateEnabled
		case !*w.Enabled && state == svcsdktypes.KeyStateEnabled:
			if _, err := api.DisableKey(ctx, &svcsdk.DisableKeyInput{KeyId: aws.String(keyID)}); err != nil {
				return done, false, fmt.Errorf("DisableKey: %w", err)
			}
			done = append(done, Done{ReasonKeyDisabled, "disabled the key (" + AnnotationEnabled + ": false)"})
			state = svcsdktypes.KeyStateDisabled
		}
	}

	if w.RotationPeriod != 0 && rotationOn {
		if state != svcsdktypes.KeyStateEnabled {
			// KMS refuses EnableKeyRotation on a disabled key
			// (KMSInvalidStateException); it is set when the key is enabled.
			done = append(done, Done{ReasonRotationPeriodIgnored, "the rotation period is set only while the key is enabled; it is " + string(state)})
			return done, false, nil
		}
		st, err := api.GetKeyRotationStatus(ctx, &svcsdk.GetKeyRotationStatusInput{KeyId: aws.String(keyID)})
		if err != nil {
			return done, false, fmt.Errorf("GetKeyRotationStatus: %w", err)
		}
		// A period AWS does not report (nil) is set again: it cannot be
		// compared, and the call is idempotent.
		if !st.KeyRotationEnabled || st.RotationPeriodInDays == nil || *st.RotationPeriodInDays != w.RotationPeriod {
			if _, err := api.EnableKeyRotation(ctx, &svcsdk.EnableKeyRotationInput{KeyId: aws.String(keyID), RotationPeriodInDays: aws.Int32(w.RotationPeriod)}); err != nil {
				return done, false, fmt.Errorf("EnableKeyRotation: %w", err)
			}
			if st.RotationPeriodInDays == nil || *st.RotationPeriodInDays != w.RotationPeriod || !st.KeyRotationEnabled {
				done = append(done, Done{ReasonRotationPeriodSet, fmt.Sprintf("set the rotation period to %d days", w.RotationPeriod)})
			}
		}
	}
	return done, false, nil
}

// Reconciler acts on ACK's Keys.
type Reconciler struct {
	Env      hflextend.Env
	Client   client.Client
	Recorder record.EventRecorder
	// NewAPI makes a KMS client for a Region.
	NewAPI func(ctx context.Context, region string) (API, error)

	// noted remembers the last message recorded per object, so a resync
	// that finds the same thing does not record it again.
	noted map[string]string
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
			return svcsdk.NewFromConfig(cfg), nil
		},
		noted: map[string]string{},
	}
	return ctrl.NewControllerManagedBy(env.Manager).
		Named(Name).
		For(&ackkms.Key{}, builder.WithPredicates(predicate.Or(
			predicate.GenerationChangedPredicate{},
			predicate.AnnotationChangedPredicate{},
			// status.keyID appears when ACK has made the key.
			predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool { return keyIDOf(e.ObjectOld) != keyIDOf(e.ObjectNew) }},
		))).
		Complete(r)
}

func keyIDOf(o client.Object) string {
	if k, ok := o.(*ackkms.Key); ok && k.Status.KeyID != nil {
		return *k.Status.KeyID
	}
	return ""
}

func (r *Reconciler) note(k *ackkms.Key, kind, reason, msg string) {
	id := k.Namespace + "/" + k.Name
	if r.noted[id] == reason+msg {
		return
	}
	r.noted[id] = reason + msg
	r.Recorder.Event(k, kind, reason, msg)
}

// Reconcile applies one Key's annotations.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	k := &ackkms.Key{}
	if err := r.Client.Get(ctx, req.NamespacedName, k); err != nil {
		delete(r.noted, req.String())
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !k.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	w, perr := Parse(k.Annotations)
	if perr != nil {
		r.note(k, corev1.EventTypeWarning, ReasonInvalidAnnotation, perr.Error())
	}
	if !w.Any() {
		return ctrl.Result{}, nil
	}
	if hflextend.ReadOnly(k) {
		r.note(k, corev1.EventTypeNormal, ReasonNotActed, "the Key is read-only (services.k8s.aws/read-only): nothing is changed in AWS")
		return ctrl.Result{}, nil
	}
	md := hflextend.ACKMetadataOf(k.Status.ACKResourceMetadata)
	if k.Status.KeyID == nil || !md.Made() {
		// ACK has not made the key yet; its status update wakes us.
		return ctrl.Result{}, nil
	}
	if why := r.Env.OtherAccount(md); why != "" {
		r.note(k, corev1.EventTypeWarning, ReasonNotActed, why)
		return ctrl.Result{}, nil
	}
	rotationOn := k.Spec.EnableKeyRotation != nil && *k.Spec.EnableKeyRotation
	if w.RotationPeriod != 0 && !rotationOn {
		r.note(k, corev1.EventTypeWarning, ReasonRotationPeriodIgnored, AnnotationRotationPeriod+" applies only with spec.enableKeyRotation: true")
	}
	api, err := r.NewAPI(ctx, md.Region)
	if err != nil {
		return ctrl.Result{}, err
	}
	done, retry, err := Apply(ctx, api, *k.Status.KeyID, rotationOn, w)
	for _, d := range done {
		kind := corev1.EventTypeNormal
		if d.Reason == ReasonRotationPeriodIgnored {
			kind = corev1.EventTypeWarning
		}
		r.note(k, kind, d.Reason, d.Message)
	}
	if err != nil {
		r.note(k, corev1.EventTypeWarning, ReasonAWSError, err.Error())
		return ctrl.Result{}, err
	}
	if retry {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	// Reread on ACK's resync period, to put back a change made outside
	// Kubernetes.
	return ctrl.Result{RequeueAfter: r.Env.Resync()}, nil
}
