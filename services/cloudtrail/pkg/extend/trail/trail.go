// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package trail extends ACK's own CloudTrail Trail
// (cloudtrail.services.k8s.aws) with what cloudtrail-controller lacks
// (docs/extensions.md; docs/plan.md open question 26, answered 2026-11-23:
// annotations on ACK's Trail). ACK's controller makes a trail but never
// starts its logging, and its spec has no selectors; these annotations on
// ACK's Trail fill both, acted on by this controller once ACK has made the
// trail:
//
//   - cloudtrail.aws.hfl.dev/is-logging: "true" or "false" (StartLogging,
//     StopLogging; read with GetTrailStatus). CloudFormation's IsLogging,
//     Terraform's enable_logging. Without it the trail's logging is left as
//     it is, which for a new trail is off.
//   - cloudtrail.aws.hfl.dev/event-selectors: a JSON array of CloudTrail's
//     EventSelector shape (ReadWriteType, IncludeManagementEvents,
//     DataResources, ExcludeManagementEventSources), put with
//     PutEventSelectors.
//   - cloudtrail.aws.hfl.dev/advanced-event-selectors: a JSON array of
//     AdvancedEventSelector (Name, FieldSelectors), put with
//     PutEventSelectors' AdvancedEventSelectors. A trail has one kind or the
//     other, so the two annotations together are refused.
//   - cloudtrail.aws.hfl.dev/insight-selectors: a JSON array of
//     InsightSelector (InsightType), put with PutInsightSelectors; "[]"
//     turns Insights off.
//
// The JSON is CloudTrail's own API shape, checked here (unknown fields and
// bad values are an InvalidAnnotation Warning Event), since ACK's CRD
// cannot validate an annotation. Removing an annotation leaves the trail
// as it is.
//
// ACK's controller is left alone: this one waits until ACK has made the
// trail (status.ackResourceMetadata.arn), never writes ACK's spec or
// status, and records what it did, or why it did nothing, as Events on the
// Trail. It acts only in the manager's own account (not through CARM), and
// not on a read-only object. ACK's Trail is read as unstructured:
// cloudtrail-controller is not a Go module here (UPSTREAM.md).
package trail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/cloudtrail/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	hflextend "github.com/highfivelabs/ack-extensions/pkg/extend"
)

// +kubebuilder:rbac:groups=cloudtrail.services.k8s.aws,resources=trails,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

const (
	// AnnotationPrefix is our group's, on ACK's object.
	AnnotationPrefix = "cloudtrail.aws.hfl.dev/"
	// AnnotationIsLogging is "true" or "false".
	AnnotationIsLogging = AnnotationPrefix + "is-logging"
	// AnnotationEventSelectors is a JSON array of EventSelector.
	AnnotationEventSelectors = AnnotationPrefix + "event-selectors"
	// AnnotationAdvancedEventSelectors is a JSON array of
	// AdvancedEventSelector.
	AnnotationAdvancedEventSelectors = AnnotationPrefix + "advanced-event-selectors"
	// AnnotationInsightSelectors is a JSON array of InsightSelector.
	AnnotationInsightSelectors = AnnotationPrefix + "insight-selectors"

	// Name is the controller's name, and the Events' source.
	Name = "ack-extensions-cloudtrail-trail"
)

// Event reasons.
const (
	ReasonLoggingStarted    = "TrailLoggingStarted"
	ReasonLoggingStopped    = "TrailLoggingStopped"
	ReasonSelectorsSet      = "TrailEventSelectorsSet"
	ReasonInsightsSet       = "TrailInsightSelectorsSet"
	ReasonInvalidAnnotation = "InvalidAnnotation"
	ReasonNotActed          = "ExtensionNotApplied"
	ReasonAWSError          = "ExtensionAWSError"
)

// GVK is ACK's Trail.
var GVK = schema.GroupVersionKind{Group: "cloudtrail.services.k8s.aws", Version: "v1alpha1", Kind: "Trail"}

func init() { hflextend.Register("cloudtrail-trail", Setup) }

// Wanted is what a Trail's annotations ask for; nil asks nothing.
type Wanted struct {
	IsLogging *bool
	// EventSelectors and AdvancedEventSelectors are nil when not asked; at
	// most one is set.
	EventSelectors         []svcsdktypes.EventSelector
	AdvancedEventSelectors []svcsdktypes.AdvancedEventSelector
	// InsightSelectors is nil when not asked, empty for Insights off.
	InsightSelectors []svcsdktypes.InsightSelector
}

// Any reports whether anything is asked.
func (w Wanted) Any() bool {
	return w.IsLogging != nil || w.EventSelectors != nil || w.AdvancedEventSelectors != nil || w.InsightSelectors != nil
}

// strictJSON decodes s into v, refusing unknown fields and trailing data.
func strictJSON(s string, v any) error {
	d := json.NewDecoder(strings.NewReader(s))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.More() {
		return errors.New("data after the JSON array")
	}
	return nil
}

func oneOf[T ~string](v T, values []T) bool {
	for _, x := range values {
		if v == x {
			return true
		}
	}
	return false
}

// Parse reads a Trail's annotations. An annotation that is set but not
// valid is an error naming it; the others are still read.
func Parse(annotations map[string]string) (Wanted, error) {
	var w Wanted
	var errs []error
	if v, ok := annotations[AnnotationIsLogging]; ok {
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %q is neither true nor false", AnnotationIsLogging, v))
		} else {
			w.IsLogging = &b
		}
	}
	if v, ok := annotations[AnnotationEventSelectors]; ok {
		sel := []svcsdktypes.EventSelector{}
		err := strictJSON(v, &sel)
		for i := range sel {
			if err == nil && sel[i].ReadWriteType != "" && !oneOf(sel[i].ReadWriteType, svcsdktypes.ReadWriteType("").Values()) {
				err = fmt.Errorf("selector %d: ReadWriteType %q is not one of %v", i, sel[i].ReadWriteType, svcsdktypes.ReadWriteType("").Values())
			}
			for j, d := range sel[i].DataResources {
				if err == nil && aws.ToString(d.Type) == "" {
					err = fmt.Errorf("selector %d: data resource %d has no Type", i, j)
				}
			}
		}
		if err == nil && len(sel) > 5 {
			err = fmt.Errorf("%d selectors; a trail takes at most 5", len(sel))
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %v", AnnotationEventSelectors, err))
		} else {
			w.EventSelectors = sel
		}
	}
	if v, ok := annotations[AnnotationAdvancedEventSelectors]; ok {
		sel := []svcsdktypes.AdvancedEventSelector{}
		err := strictJSON(v, &sel)
		for i := range sel {
			if err == nil && len(sel[i].FieldSelectors) == 0 {
				err = fmt.Errorf("selector %d has no FieldSelectors", i)
			}
			for j, f := range sel[i].FieldSelectors {
				if err == nil && aws.ToString(f.Field) == "" {
					err = fmt.Errorf("selector %d: field selector %d has no Field", i, j)
				}
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %v", AnnotationAdvancedEventSelectors, err))
		} else {
			w.AdvancedEventSelectors = sel
		}
	}
	if w.EventSelectors != nil && w.AdvancedEventSelectors != nil {
		errs = append(errs, fmt.Errorf("%s and %s: a trail has event selectors or advanced event selectors, not both; neither is applied", AnnotationEventSelectors, AnnotationAdvancedEventSelectors))
		w.EventSelectors, w.AdvancedEventSelectors = nil, nil
	}
	if v, ok := annotations[AnnotationInsightSelectors]; ok {
		sel := []svcsdktypes.InsightSelector{}
		err := strictJSON(v, &sel)
		for i := range sel {
			if err == nil && !oneOf(sel[i].InsightType, svcsdktypes.InsightType("").Values()) {
				err = fmt.Errorf("selector %d: InsightType %q is not one of %v", i, sel[i].InsightType, svcsdktypes.InsightType("").Values())
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %v", AnnotationInsightSelectors, err))
		} else {
			w.InsightSelectors = sel
		}
	}
	return w, errors.Join(errs...)
}

// API is the part of CloudTrail's client the controller calls.
type API interface {
	GetTrailStatus(context.Context, *svcsdk.GetTrailStatusInput, ...func(*svcsdk.Options)) (*svcsdk.GetTrailStatusOutput, error)
	StartLogging(context.Context, *svcsdk.StartLoggingInput, ...func(*svcsdk.Options)) (*svcsdk.StartLoggingOutput, error)
	StopLogging(context.Context, *svcsdk.StopLoggingInput, ...func(*svcsdk.Options)) (*svcsdk.StopLoggingOutput, error)
	GetEventSelectors(context.Context, *svcsdk.GetEventSelectorsInput, ...func(*svcsdk.Options)) (*svcsdk.GetEventSelectorsOutput, error)
	PutEventSelectors(context.Context, *svcsdk.PutEventSelectorsInput, ...func(*svcsdk.Options)) (*svcsdk.PutEventSelectorsOutput, error)
	GetInsightSelectors(context.Context, *svcsdk.GetInsightSelectorsInput, ...func(*svcsdk.Options)) (*svcsdk.GetInsightSelectorsOutput, error)
	PutInsightSelectors(context.Context, *svcsdk.PutInsightSelectorsInput, ...func(*svcsdk.Options)) (*svcsdk.PutInsightSelectorsOutput, error)
}

// Done is what Apply did, for Events.
type Done struct {
	Reason, Message string
}

// canonicalEvent is an event selector with CloudTrail's defaults filled
// (ReadWriteType All, management events included) and empty lists as none,
// as JSON, so a selector written without the defaults equals what
// GetEventSelectors returns for it.
func canonicalEvent(sel []svcsdktypes.EventSelector) string {
	out := make([]svcsdktypes.EventSelector, len(sel))
	for i, s := range sel {
		if s.ReadWriteType == "" {
			s.ReadWriteType = svcsdktypes.ReadWriteTypeAll
		}
		if s.IncludeManagementEvents == nil {
			s.IncludeManagementEvents = aws.Bool(true)
		}
		if len(s.DataResources) == 0 {
			s.DataResources = nil
		}
		if len(s.ExcludeManagementEventSources) == 0 {
			s.ExcludeManagementEventSources = nil
		}
		for j := range s.DataResources {
			if len(s.DataResources[j].Values) == 0 {
				s.DataResources[j].Values = nil
			}
		}
		out[i] = s
	}
	return marshal(out)
}

// canonicalAdvanced is advanced selectors with empty lists as none, as
// JSON.
func canonicalAdvanced(sel []svcsdktypes.AdvancedEventSelector) string {
	out := make([]svcsdktypes.AdvancedEventSelector, len(sel))
	for i, s := range sel {
		fs := make([]svcsdktypes.AdvancedFieldSelector, len(s.FieldSelectors))
		for j, f := range s.FieldSelectors {
			for _, l := range []*[]string{&f.Equals, &f.NotEquals, &f.StartsWith, &f.NotStartsWith, &f.EndsWith, &f.NotEndsWith} {
				if len(*l) == 0 {
					*l = nil
				}
			}
			fs[j] = f
		}
		s.FieldSelectors = fs
		out[i] = s
	}
	return marshal(out)
}

// insightTypes is the set of insight types, sorted.
func insightTypes(sel []svcsdktypes.InsightSelector) []string {
	seen := map[string]bool{}
	for _, s := range sel {
		seen[string(s.InsightType)] = true
	}
	out := []string{}
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func marshal(v any) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	return strings.TrimSpace(b.String())
}

// isCode reports whether err is an AWS error with the code.
func isCode(err error, code string) bool {
	var ae interface{ ErrorCode() string }
	return errors.As(err, &ae) && ae.ErrorCode() == code
}

// Apply makes the trail what w asks. It reads before it writes, so a trail
// already as asked is not called again. arn names the trail.
func Apply(ctx context.Context, api API, arn string, w Wanted) (done []Done, err error) {
	if w.EventSelectors != nil || w.AdvancedEventSelectors != nil {
		cur, err := api.GetEventSelectors(ctx, &svcsdk.GetEventSelectorsInput{TrailName: aws.String(arn)})
		if err != nil {
			return done, fmt.Errorf("GetEventSelectors: %w", err)
		}
		switch {
		case w.EventSelectors != nil && (len(cur.AdvancedEventSelectors) > 0 || canonicalEvent(cur.EventSelectors) != canonicalEvent(w.EventSelectors)):
			if _, err := api.PutEventSelectors(ctx, &svcsdk.PutEventSelectorsInput{TrailName: aws.String(arn), EventSelectors: w.EventSelectors}); err != nil {
				return done, fmt.Errorf("PutEventSelectors: %w", err)
			}
			done = append(done, Done{ReasonSelectorsSet, fmt.Sprintf("put %d event selector(s) (%s)", len(w.EventSelectors), AnnotationEventSelectors)})
		case w.AdvancedEventSelectors != nil && (len(cur.EventSelectors) > 0 || canonicalAdvanced(cur.AdvancedEventSelectors) != canonicalAdvanced(w.AdvancedEventSelectors)):
			if _, err := api.PutEventSelectors(ctx, &svcsdk.PutEventSelectorsInput{TrailName: aws.String(arn), AdvancedEventSelectors: w.AdvancedEventSelectors}); err != nil {
				return done, fmt.Errorf("PutEventSelectors (advanced): %w", err)
			}
			done = append(done, Done{ReasonSelectorsSet, fmt.Sprintf("put %d advanced event selector(s) (%s)", len(w.AdvancedEventSelectors), AnnotationAdvancedEventSelectors)})
		}
	}
	if w.InsightSelectors != nil {
		var have []svcsdktypes.InsightSelector
		cur, err := api.GetInsightSelectors(ctx, &svcsdk.GetInsightSelectorsInput{TrailName: aws.String(arn)})
		switch {
		case err != nil && isCode(err, "InsightNotEnabledException"):
			// No Insights on the trail: none selected.
		case err != nil:
			return done, fmt.Errorf("GetInsightSelectors: %w", err)
		default:
			have = cur.InsightSelectors
		}
		if !reflect.DeepEqual(insightTypes(have), insightTypes(w.InsightSelectors)) {
			if _, err := api.PutInsightSelectors(ctx, &svcsdk.PutInsightSelectorsInput{TrailName: aws.String(arn), InsightSelectors: w.InsightSelectors}); err != nil {
				return done, fmt.Errorf("PutInsightSelectors: %w", err)
			}
			done = append(done, Done{ReasonInsightsSet, fmt.Sprintf("put insight selectors %v (%s)", insightTypes(w.InsightSelectors), AnnotationInsightSelectors)})
		}
	}
	if w.IsLogging != nil {
		st, err := api.GetTrailStatus(ctx, &svcsdk.GetTrailStatusInput{Name: aws.String(arn)})
		if err != nil {
			return done, fmt.Errorf("GetTrailStatus: %w", err)
		}
		on := aws.ToBool(st.IsLogging)
		switch {
		case *w.IsLogging && !on:
			if _, err := api.StartLogging(ctx, &svcsdk.StartLoggingInput{Name: aws.String(arn)}); err != nil {
				return done, fmt.Errorf("StartLogging: %w", err)
			}
			done = append(done, Done{ReasonLoggingStarted, "started logging (" + AnnotationIsLogging + ": true)"})
		case !*w.IsLogging && on:
			if _, err := api.StopLogging(ctx, &svcsdk.StopLoggingInput{Name: aws.String(arn)}); err != nil {
				return done, fmt.Errorf("StopLogging: %w", err)
			}
			done = append(done, Done{ReasonLoggingStopped, "stopped logging (" + AnnotationIsLogging + ": false)"})
		}
	}
	return done, nil
}

// Reconciler acts on ACK's Trails.
type Reconciler struct {
	Env      hflextend.Env
	Client   client.Client
	Recorder record.EventRecorder
	// NewAPI makes a CloudTrail client for a Region.
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
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(GVK)
	return ctrl.NewControllerManagedBy(env.Manager).Named(Name).For(u).Complete(r)
}

func (r *Reconciler) note(u *unstructured.Unstructured, kind, reason, msg string) {
	id := u.GetNamespace() + "/" + u.GetName()
	if r.noted[id] == reason+msg {
		return
	}
	r.noted[id] = reason + msg
	r.Recorder.Event(u, kind, reason, msg)
}

// Reconcile applies one Trail's annotations.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(GVK)
	if err := r.Client.Get(ctx, req.NamespacedName, u); err != nil {
		delete(r.noted, req.String())
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !u.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, nil
	}
	w, perr := Parse(u.GetAnnotations())
	if perr != nil {
		r.note(u, corev1.EventTypeWarning, ReasonInvalidAnnotation, perr.Error())
	}
	if !w.Any() {
		return ctrl.Result{}, nil
	}
	if hflextend.ReadOnly(u) {
		r.note(u, corev1.EventTypeNormal, ReasonNotActed, "the Trail is read-only (services.k8s.aws/read-only): nothing is changed in AWS")
		return ctrl.Result{}, nil
	}
	region, _, _ := unstructured.NestedString(u.Object, "status", "ackResourceMetadata", "region")
	owner, _, _ := unstructured.NestedString(u.Object, "status", "ackResourceMetadata", "ownerAccountID")
	arn, _, _ := unstructured.NestedString(u.Object, "status", "ackResourceMetadata", "arn")
	md := hflextend.ACKMetadata{Region: region, OwnerAccountID: owner}
	if arn == "" || !md.Made() {
		// ACK has not made the trail yet; its status update wakes us.
		return ctrl.Result{}, nil
	}
	if why := r.Env.OtherAccount(md); why != "" {
		r.note(u, corev1.EventTypeWarning, ReasonNotActed, why)
		return ctrl.Result{}, nil
	}
	api, err := r.NewAPI(ctx, md.Region)
	if err != nil {
		return ctrl.Result{}, err
	}
	done, err := Apply(ctx, api, arn, w)
	for _, d := range done {
		r.note(u, corev1.EventTypeNormal, d.Reason, d.Message)
	}
	if err != nil {
		r.note(u, corev1.EventTypeWarning, ReasonAWSError, err.Error())
		if isCode(err, "TrailNotFoundException") {
			// ACK's own read reports a trail gone; try again later.
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}
	// Reread on ACK's resync period, to put back a change made outside
	// Kubernetes.
	return ctrl.Result{RequeueAfter: r.Env.Resync()}, nil
}
