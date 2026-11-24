// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package transitgateway extends ACK's own EC2 TransitGateway
// (ec2.services.k8s.aws) with the options ec2-controller lacks
// (docs/extensions.md; docs/plan.md open question 1, answered
// 2026-11-13), as annotations on ACK's TransitGateway that this controller
// acts on with ModifyTransitGateway once ACK has made the gateway:
//
//   - ec2.aws.hfl.dev/security-group-referencing-support: "enable" or
//     "disable" (Terraform's security_group_referencing_support;
//     CloudFormation's SecurityGroupReferencingSupport). ec2-controller
//     ignores the field on create (its generator.yaml, at v1.23.1 and on
//     main).
//   - ec2.aws.hfl.dev/encryption-support: "enable" or "disable"
//     (CloudFormation's EncryptionSupport): encryption in transit between
//     the gateway's VPC attachments. AWS takes some time to apply it; the
//     state "enabling" or "disabling" is waited out.
//   - ec2.aws.hfl.dev/association-default-route-table-id and
//     ec2.aws.hfl.dev/propagation-default-route-table-id: the transit
//     gateway route table (tgw-rtb-...) new attachments are associated
//     with and propagate to, in place of the one AWS makes with the
//     gateway (CloudFormation's AssociationDefaultRouteTableId and
//     PropagationDefaultRouteTableId). They apply while ACK's
//     spec.options.defaultRouteTableAssociation (or Propagation) is
//     "enable", AWS's default.
//
// ec2-controller's TransitGateway has none of the four in its spec
// (TransitGatewayRequestOptions) and its update syncs tags only, so its
// reads never compare them and nothing fights this controller.
//
// ACK's controller is left alone: this one waits until ACK has made the
// gateway (status.transitGatewayID), never writes ACK's spec or status,
// and records what it did, or why it did nothing, as Events on the
// TransitGateway. It acts only in the manager's own account (not through
// CARM), and not on a read-only object. Removing an annotation leaves the
// option as it is.
package transitgateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	ackec2 "github.com/aws-controllers-k8s/ec2-controller/apis/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/ec2/types"
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

// +kubebuilder:rbac:groups=ec2.services.k8s.aws,resources=transitgateways,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

const (
	// AnnotationPrefix is our group's, on ACK's object.
	AnnotationPrefix = "ec2.aws.hfl.dev/"
	// AnnotationSecurityGroupReferencing is "enable" or "disable".
	AnnotationSecurityGroupReferencing = AnnotationPrefix + "security-group-referencing-support"
	// AnnotationEncryption is "enable" or "disable".
	AnnotationEncryption = AnnotationPrefix + "encryption-support"
	// AnnotationAssociationDefaultRouteTable is a transit gateway route
	// table ID.
	AnnotationAssociationDefaultRouteTable = AnnotationPrefix + "association-default-route-table-id"
	// AnnotationPropagationDefaultRouteTable is a transit gateway route
	// table ID.
	AnnotationPropagationDefaultRouteTable = AnnotationPrefix + "propagation-default-route-table-id"

	// Name is the controller's name, and the Events' source.
	Name = "ack-extensions-ec2-transit-gateway"

	enable  = "enable"
	disable = "disable"
)

// Event reasons.
const (
	ReasonOptionsSet        = "TransitGatewayOptionsSet"
	ReasonOptionIgnored     = "TransitGatewayOptionIgnored"
	ReasonInvalidAnnotation = "InvalidAnnotation"
	ReasonNotActed          = "ExtensionNotApplied"
	ReasonAWSError          = "ExtensionAWSError"
)

// GVK is ACK's TransitGateway.
var GVK = schema.GroupVersionKind{Group: "ec2.services.k8s.aws", Version: "v1alpha1", Kind: "TransitGateway"}

func init() { hflextend.Register("ec2-transit-gateway", Setup) }

// Wanted is what a TransitGateway's annotations ask for; an empty field
// asks nothing.
type Wanted struct {
	// SecurityGroupReferencing and Encryption are "enable", "disable" or
	// empty.
	SecurityGroupReferencing string
	Encryption               string
	// AssociationDefaultRouteTable and PropagationDefaultRouteTable are
	// route table IDs or empty.
	AssociationDefaultRouteTable string
	PropagationDefaultRouteTable string
}

// Any reports whether anything is asked.
func (w Wanted) Any() bool { return w != Wanted{} }

// Parse reads a TransitGateway's annotations. An annotation that is set
// but not valid is an error naming it; the others are still read.
func Parse(annotations map[string]string) (Wanted, error) {
	var w Wanted
	var errs []error
	onOff := func(name string, into *string) {
		v, ok := annotations[name]
		if !ok {
			return
		}
		switch s := strings.ToLower(strings.TrimSpace(v)); s {
		case enable, disable:
			*into = s
		default:
			errs = append(errs, fmt.Errorf("%s: %q is neither enable nor disable", name, v))
		}
	}
	routeTable := func(name string, into *string) {
		v, ok := annotations[name]
		if !ok {
			return
		}
		s := strings.TrimSpace(v)
		if !strings.HasPrefix(s, "tgw-rtb-") || len(s) == len("tgw-rtb-") {
			errs = append(errs, fmt.Errorf("%s: %q is not a transit gateway route table ID (tgw-rtb-...)", name, v))
			return
		}
		*into = s
	}
	onOff(AnnotationSecurityGroupReferencing, &w.SecurityGroupReferencing)
	onOff(AnnotationEncryption, &w.Encryption)
	routeTable(AnnotationAssociationDefaultRouteTable, &w.AssociationDefaultRouteTable)
	routeTable(AnnotationPropagationDefaultRouteTable, &w.PropagationDefaultRouteTable)
	return w, errors.Join(errs...)
}

// API is the part of EC2's client the controller calls.
type API interface {
	DescribeTransitGateways(context.Context, *svcsdk.DescribeTransitGatewaysInput, ...func(*svcsdk.Options)) (*svcsdk.DescribeTransitGatewaysOutput, error)
	ModifyTransitGateway(context.Context, *svcsdk.ModifyTransitGatewayInput, ...func(*svcsdk.Options)) (*svcsdk.ModifyTransitGatewayOutput, error)
}

// Done is what Apply did, or why it left an option alone, for Events.
type Done struct {
	Warning         bool
	Reason, Message string
}

// encryptionOf is the gateway's encryption in transit as "enable" or
// "disable", and whether AWS is still changing it. A gateway that reports
// none has it disabled.
func encryptionOf(o *svcsdktypes.TransitGatewayOptions) (string, bool) {
	if o == nil || o.EncryptionSupport == nil {
		return disable, false
	}
	switch o.EncryptionSupport.EncryptionState {
	case svcsdktypes.EncryptionStateValueEnabled:
		return enable, false
	case svcsdktypes.EncryptionStateValueEnabling:
		return enable, true
	case svcsdktypes.EncryptionStateValueDisabling:
		return disable, true
	}
	return disable, false
}

// Apply makes the gateway's options what w asks, in one
// ModifyTransitGateway call with only the options that differ. It reads
// before it writes, so a gateway already as asked is not called again.
// retry is true when the gateway cannot be changed yet (being made or
// modified) or AWS is still applying encryption.
func Apply(ctx context.Context, api API, id string, w Wanted) (done []Done, retry bool, err error) {
	out, err := api.DescribeTransitGateways(ctx, &svcsdk.DescribeTransitGatewaysInput{TransitGatewayIds: []string{id}})
	if err != nil {
		return nil, false, fmt.Errorf("DescribeTransitGateways: %w", err)
	}
	if len(out.TransitGateways) == 0 {
		// Gone, or not visible yet: ACK's own read reports it.
		return nil, true, nil
	}
	tgw := out.TransitGateways[0]
	switch tgw.State {
	case svcsdktypes.TransitGatewayStateDeleting, svcsdktypes.TransitGatewayStateDeleted:
		return nil, false, nil
	case svcsdktypes.TransitGatewayStateAvailable:
	default:
		// pending or modifying: ModifyTransitGateway refuses it
		// (IncorrectState).
		return nil, true, nil
	}
	cur := tgw.Options
	if cur == nil {
		cur = &svcsdktypes.TransitGatewayOptions{}
	}

	opts := &svcsdktypes.ModifyTransitGatewayOptions{}
	var changed []string
	if w.SecurityGroupReferencing != "" {
		have := string(cur.SecurityGroupReferencingSupport)
		if have == "" {
			have = disable
		}
		if have != w.SecurityGroupReferencing {
			opts.SecurityGroupReferencingSupport = svcsdktypes.SecurityGroupReferencingSupportValue(w.SecurityGroupReferencing)
			changed = append(changed, "securityGroupReferencingSupport "+w.SecurityGroupReferencing)
		}
	}
	if w.Encryption != "" {
		have, changing := encryptionOf(cur)
		switch {
		case have == w.Encryption && changing:
			// Asked and on its way.
			retry = true
		case have != w.Encryption && changing:
			// The other way round is under way; asked again when it is done.
			retry = true
		case have != w.Encryption:
			opts.EncryptionSupport = svcsdktypes.EncryptionSupportOptionValue(w.Encryption)
			changed = append(changed, "encryptionSupport "+w.Encryption)
			retry = true
		}
	}
	defaultTable := func(want, have string, mode svcsdktypes.DefaultRouteTableAssociationValue, what, annotation string, set **string) {
		if want == "" || want == have {
			return
		}
		if mode != svcsdktypes.DefaultRouteTableAssociationValueEnable {
			done = append(done, Done{true, ReasonOptionIgnored, fmt.Sprintf("%s applies only while the gateway's default route table %s is enable; it is %q", annotation, what, string(mode))})
			return
		}
		*set = aws.String(want)
		changed = append(changed, what+" default route table "+want)
	}
	defaultTable(w.AssociationDefaultRouteTable, aws.ToString(cur.AssociationDefaultRouteTableId),
		svcsdktypes.DefaultRouteTableAssociationValue(cur.DefaultRouteTableAssociation), "association",
		AnnotationAssociationDefaultRouteTable, &opts.AssociationDefaultRouteTableId)
	defaultTable(w.PropagationDefaultRouteTable, aws.ToString(cur.PropagationDefaultRouteTableId),
		svcsdktypes.DefaultRouteTableAssociationValue(cur.DefaultRouteTablePropagation), "propagation",
		AnnotationPropagationDefaultRouteTable, &opts.PropagationDefaultRouteTableId)

	if len(changed) == 0 {
		return done, retry, nil
	}
	if _, err := api.ModifyTransitGateway(ctx, &svcsdk.ModifyTransitGatewayInput{TransitGatewayId: aws.String(id), Options: opts}); err != nil {
		return done, false, fmt.Errorf("ModifyTransitGateway: %w", err)
	}
	done = append(done, Done{false, ReasonOptionsSet, "set " + strings.Join(changed, ", ")})
	return done, retry, nil
}

// Reconciler acts on ACK's TransitGateways.
type Reconciler struct {
	Env      hflextend.Env
	Client   client.Client
	Recorder record.EventRecorder
	// NewAPI makes an EC2 client for a Region.
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
		For(&ackec2.TransitGateway{}, builder.WithPredicates(predicate.Or(
			predicate.GenerationChangedPredicate{},
			predicate.AnnotationChangedPredicate{},
			// status.transitGatewayID appears when ACK has made the
			// gateway.
			predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool { return idOf(e.ObjectOld) != idOf(e.ObjectNew) }},
		))).
		Complete(r)
}

func idOf(o client.Object) string {
	if g, ok := o.(*ackec2.TransitGateway); ok && g.Status.TransitGatewayID != nil {
		return *g.Status.TransitGatewayID
	}
	return ""
}

func (r *Reconciler) note(g *ackec2.TransitGateway, kind, reason, msg string) {
	id := g.Namespace + "/" + g.Name
	if r.noted[id] == reason+msg {
		return
	}
	r.noted[id] = reason + msg
	r.Recorder.Event(g, kind, reason, msg)
}

// Reconcile applies one TransitGateway's annotations.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	g := &ackec2.TransitGateway{}
	if err := r.Client.Get(ctx, req.NamespacedName, g); err != nil {
		delete(r.noted, req.String())
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !g.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	w, perr := Parse(g.Annotations)
	if perr != nil {
		r.note(g, corev1.EventTypeWarning, ReasonInvalidAnnotation, perr.Error())
	}
	if !w.Any() {
		return ctrl.Result{}, nil
	}
	if hflextend.ReadOnly(g) {
		r.note(g, corev1.EventTypeNormal, ReasonNotActed, "the TransitGateway is read-only (services.k8s.aws/read-only): nothing is changed in AWS")
		return ctrl.Result{}, nil
	}
	md := hflextend.ACKMetadataOf(g.Status.ACKResourceMetadata)
	if g.Status.TransitGatewayID == nil || !md.Made() {
		// ACK has not made the gateway yet; its status update wakes us.
		return ctrl.Result{}, nil
	}
	if why := r.Env.OtherAccount(md); why != "" {
		r.note(g, corev1.EventTypeWarning, ReasonNotActed, why)
		return ctrl.Result{}, nil
	}
	api, err := r.NewAPI(ctx, md.Region)
	if err != nil {
		return ctrl.Result{}, err
	}
	done, retry, err := Apply(ctx, api, *g.Status.TransitGatewayID, w)
	for _, d := range done {
		kind := corev1.EventTypeNormal
		if d.Warning {
			kind = corev1.EventTypeWarning
		}
		r.note(g, kind, d.Reason, d.Message)
	}
	if err != nil {
		r.note(g, corev1.EventTypeWarning, ReasonAWSError, err.Error())
		return ctrl.Result{}, err
	}
	if retry {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	// Reread on ACK's resync period, to put back a change made outside
	// Kubernetes.
	return ctrl.Result{RequeueAfter: r.Env.Resync()}, nil
}
