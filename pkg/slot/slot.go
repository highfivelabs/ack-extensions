// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package slot is the version-slot guard (docs/slot-moves.md): an opt-in
// soft lease that lets two installs of the same managers (two "slots",
// each running one version) share an AWS account without both writing
// one resource.
//
// With a slot set (--slot, or HFL_SLOT; the flag wins) a manager
//   - tags every resource it creates or adopts hfl.dev/slot=<slot>, through
//     ACK's own --resource-tags, so the tag is written wherever ACK writes
//     its default tags;
//   - labels the object hfl.dev/slot=<slot>;
//   - refuses every write and delete to a resource whose hfl.dev/slot tag
//     names another slot: no AWS call, ACK.Terminal with reason
//     SlotMismatch (so Ready is False), and a Warning event SlotMismatch.
//     Reads go on, every RetryAfter.
//
// A move hands a resource over explicitly: the adopting object in the new
// slot carries hfl.dev/slot-takeover=<old slot>, and the new slot may then
// write a resource tagged with that slot (and re-tags it as its own).
//
// Without a slot nothing changes: Wrap returns the factories as given.
//
// The guard reads the tag from the observed resource, so it covers only
// kinds whose read returns their tags (TaggedKinds for the ACK-generated
// kinds; the Cloud Control kinds with a top-level Key/Value tag list).
// Every other kind is unguarded; docs/slot-moves.md lists them.
package slot

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	ackcondition "github.com/aws-controllers-k8s/runtime/pkg/condition"
	ackcfg "github.com/aws-controllers-k8s/runtime/pkg/config"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackmetrics "github.com/aws-controllers-k8s/runtime/pkg/metrics"
	ackrequeue "github.com/aws-controllers-k8s/runtime/pkg/requeue"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/go-logr/logr"
	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// TagKey is the AWS tag naming the slot that holds a resource.
	TagKey = "hfl.dev/slot"
	// LabelKey is the label naming the slot on the Kubernetes object.
	LabelKey = "hfl.dev/slot"
	// TakeoverAnnotation, on an object, names the slot it may take the
	// resource over from.
	TakeoverAnnotation = "hfl.dev/slot-takeover"
	// EnvVar sets the slot when --slot is not given.
	EnvVar = "HFL_SLOT"
	// FlagName is the managers' flag.
	FlagName = "slot"
	// Reason is the condition's and the event's reason on a refusal.
	Reason = "SlotMismatch"
)

// BindFlags adds --slot to fs; Resolve turns its value into the slot.
func BindFlags(fs *pflag.FlagSet) *string {
	return fs.String(FlagName, "", "the version slot this manager runs as (docs/slot-moves.md); default $"+EnvVar+"; empty: no slot guard")
}

// Resolve is the slot: the flag's value, else $HFL_SLOT, else "".
func Resolve(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return os.Getenv(EnvVar)
}

var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidateName accepts a slot name that is a DNS label: valid as an AWS
// tag value, a Kubernetes label value, and on the other clouds.
func ValidateName(s string) error {
	if !nameRe.MatchString(s) {
		return fmt.Errorf("slot %q: lower-case letters, digits and '-', at most 63, starting and ending with a letter or digit", s)
	}
	return nil
}

// AddResourceTag makes hfl.dev/slot=<slot> one of ACK's default tags
// (--resource-tags), replacing any hfl.dev/slot already there. Call it
// before cfg.Validate, which reads the keys out of ResourceTags.
func AddResourceTag(cfg *ackcfg.Config, slot string) {
	if slot == "" {
		return
	}
	var tags []string
	for _, t := range cfg.ResourceTags {
		if strings.SplitN(t, "=", 2)[0] != TagKey {
			tags = append(tags, t)
		}
	}
	cfg.ResourceTags = append(tags, TagKey+"="+slot)
}

// Setup is what a manager's main calls after flag.Parse and before
// cfg.Validate: the slot from the flag or $HFL_SLOT, validated, added to
// the default tags. It returns the slot ("" for none).
func Setup(cfg *ackcfg.Config, flagValue string) (string, error) {
	s := Resolve(flagValue)
	if s == "" {
		return "", nil
	}
	if err := ValidateName(s); err != nil {
		return "", err
	}
	AddResourceTag(cfg, s)
	return s, nil
}

// TagsFunc returns the AWS tags of an observed resource (what ReadOne
// returned), and false when the kind's read does not return its tags.
type TagsFunc func(gk schema.GroupKind, observed acktypes.AWSResource) (map[string]string, bool)

// TaggedKinds are the ACK-generated kinds (Kind.group) whose read fills
// spec.tags from AWS. TestTaggedKinds keeps it equal to the generated code.
var TaggedKinds = map[string]bool{
	"CustomerGateway.ec2.aws.hfl.dev":     true,
	"Volume.ec2.aws.hfl.dev":              true,
	"VPNConnection.ec2.aws.hfl.dev":       true,
	"VPNGateway.ec2.aws.hfl.dev":          true,
	"ReplicaKey.kms.aws.hfl.dev":          true,
	"Policy.organizations.aws.hfl.dev":    true,
	"OptionGroup.rds.aws.hfl.dev":         true,
	"ScheduleGroup.scheduler.aws.hfl.dev": true,
}

// GeneratedTags is the TagsFunc of the ACK-generated managers: spec.tags,
// a list of {key, value}, for the kinds in TaggedKinds.
func GeneratedTags(gk schema.GroupKind, observed acktypes.AWSResource) (map[string]string, bool) {
	if !TaggedKinds[gk.Kind+"."+gk.Group] {
		return nil, false
	}
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(observed.RuntimeObject())
	if err != nil {
		return nil, false
	}
	spec, _ := u["spec"].(map[string]any)
	return KeyValueTags(spec["tags"]), true
}

// KeyValueTags reads a list of {key, value} objects into a map.
func KeyValueTags(v any) map[string]string {
	out := map[string]string{}
	l, _ := v.([]any)
	for _, x := range l {
		m, _ := x.(map[string]any)
		k, _ := m["key"].(string)
		val, _ := m["value"].(string)
		if k != "" {
			out[k] = val
		}
	}
	return out
}

// Manager is what the guard takes from the controller manager: the
// client it labels objects with and the recorder for its events.
type Manager interface {
	GetClient() client.Client
	GetEventRecorderFor(name string) record.EventRecorder
}

// RecorderName is the guard's event source.
const RecorderName = "hfl-slot-guard"

// Wrap guards every factory's managers with the slot. With no slot it
// returns factories unchanged.
func Wrap(factories []acktypes.AWSResourceManagerFactory, slot string, mgr Manager, tags TagsFunc) []acktypes.AWSResourceManagerFactory {
	if slot == "" {
		return factories
	}
	rec, kc := mgr.GetEventRecorderFor(RecorderName), mgr.GetClient()
	out := make([]acktypes.AWSResourceManagerFactory, len(factories))
	for i, f := range factories {
		gk := f.ResourceDescriptor().GroupVersionKind().GroupKind()
		out[i] = &factory{AWSResourceManagerFactory: f, g: &guard{slot: slot, rec: rec, kc: kc, tags: tags, gk: gk}}
	}
	return out
}

type guard struct {
	slot string
	rec  record.EventRecorder
	kc   client.Client
	tags TagsFunc
	gk   schema.GroupKind
}

type factory struct {
	acktypes.AWSResourceManagerFactory
	g *guard
}

func (f *factory) ManagerFor(
	cfg ackcfg.Config, clientcfg aws.Config, log logr.Logger, m *ackmetrics.Metrics, rr acktypes.Reconciler,
	id ackv1alpha1.AWSAccountID, region ackv1alpha1.AWSRegion, roleARN ackv1alpha1.AWSResourceName,
) (acktypes.AWSResourceManager, error) {
	rm, err := f.AWSResourceManagerFactory.ManagerFor(cfg, clientcfg, log, m, rr, id, region, roleARN)
	if err != nil {
		return rm, err
	}
	w := &manager{AWSResourceManager: rm, g: f.g}
	// The runtime looks for ReferenceEnsurer on the manager; keep it.
	if e, ok := rm.(acktypes.ReferenceEnsurer); ok {
		return &ensurer{manager: w, e: e}, nil
	}
	return w, nil
}

type manager struct {
	acktypes.AWSResourceManager
	g *guard
}

type ensurer struct {
	*manager
	e acktypes.ReferenceEnsurer
}

func (e *ensurer) EnsureReferences(desired, latest acktypes.AWSResource) acktypes.AWSResource {
	return e.e.EnsureReferences(desired, latest)
}

// holder is the slot holding observed, when that is another slot the
// declared object may not take over from; "" when this slot may write.
func (g *guard) holder(declared, observed acktypes.AWSResource) string {
	tags, ok := g.tags(g.gk, observed)
	if !ok {
		return ""
	}
	h := tags[TagKey]
	if h == "" || h == g.slot {
		return ""
	}
	if declared.MetaObject().GetAnnotations()[TakeoverAnnotation] == h {
		return ""
	}
	return h
}

// RetryAfter is how long a refused object waits before it is read again.
var RetryAfter = 30 * time.Second

// refuse marks each object ACK.Terminal (reason SlotMismatch), emits the
// event on the first, and returns ackerr.Terminal wrapped in a requeue.
// The runtime makes Ready False from the terminal condition; the requeue
// is there because ACK neither resyncs an object that ended in
// ackerr.Terminal nor reconciles on an annotation change, and the ways
// out of a refusal (deletion-policy retain on the old object, the
// takeover annotation) are annotation changes.
func (g *guard) refuse(holder string, objs ...acktypes.AWSResource) error {
	msg := fmt.Sprintf("cloud resource is held by slot %s; this controller is slot %s", holder, g.slot)
	reason := Reason
	for _, o := range objs {
		if cm, ok := o.(acktypes.ConditionManager); ok {
			ackcondition.SetTerminal(cm, corev1.ConditionTrue, &msg, &reason)
		}
	}
	if g.rec != nil && len(objs) > 0 {
		g.rec.Event(objs[0].RuntimeObject(), corev1.EventTypeWarning, Reason, msg)
	}
	return ackrequeue.NeededAfter(ackerr.Terminal, RetryAfter)
}

func (g *guard) label(res acktypes.AWSResource) {
	if res == nil {
		return
	}
	mo := res.MetaObject()
	l := mo.GetLabels()
	if l == nil {
		l = map[string]string{}
	}
	if l[LabelKey] != g.slot {
		l[LabelKey] = g.slot
		mo.SetLabels(l)
	}
}

// ensureLabel patches the slot label onto the stored object when it lacks
// it. Through the client, because the runtime persists metadata only from
// what a synchronous create or update returns: never from an adoption,
// nor from Cloud Control's asynchronous requests.
func (g *guard) ensureLabel(ctx context.Context, declared acktypes.AWSResource) {
	mo := declared.MetaObject()
	if g.kc == nil || mo.GetLabels()[LabelKey] == g.slot || mo.GetDeletionTimestamp() != nil {
		return
	}
	obj, ok := declared.RuntimeObject().DeepCopyObject().(client.Object)
	if !ok {
		return
	}
	base := obj.DeepCopyObject().(client.Object)
	l := obj.GetLabels()
	if l == nil {
		l = map[string]string{}
	}
	l[LabelKey] = g.slot
	obj.SetLabels(l)
	if err := g.kc.Patch(ctx, obj, client.MergeFrom(base)); err != nil {
		// The label is a marker for people and Bundt; the lease is the
		// tag. Say so and go on; the next read tries again.
		ackrtlog.FromContext(ctx).Info("could not label the object with its slot", "slot", g.slot, "error", err.Error())
	}
}

// fresh is the resource as AWS has it, for the check: observed itself,
// except for an object being deleted, which is read again as if it were
// not. A manager may leave AWS's values out of the read of an object
// being deleted (Cloud Control's keeps the declared spec), and the
// declared tags are not the lease.
func (m *manager) fresh(ctx context.Context, observed acktypes.AWSResource) (acktypes.AWSResource, error) {
	if observed.MetaObject().GetDeletionTimestamp() == nil {
		return observed, nil
	}
	probe := observed.DeepCopy()
	probe.MetaObject().SetDeletionTimestamp(nil)
	return m.AWSResourceManager.ReadOne(ctx, probe)
}

// ReadOne reads, then checks the holder. On a refusal the condition is
// set on the object given as well as the one returned: when the runtime
// deletes, it keeps the object it gave ReadOne and drops the result.
func (m *manager) ReadOne(ctx context.Context, res acktypes.AWSResource) (acktypes.AWSResource, error) {
	latest, err := m.AWSResourceManager.ReadOne(ctx, res)
	if err != nil || latest == nil {
		return latest, err
	}
	check, err := m.fresh(ctx, latest)
	if err != nil {
		return latest, err
	}
	if h := m.g.holder(res, check); h != "" {
		return latest, m.g.refuse(h, latest, res)
	}
	m.g.ensureLabel(ctx, res)
	return latest, nil
}

// Create is never refused (the resource does not exist yet); the tag
// comes with ACK's default tags. The label goes on what Create and Update
// return: the runtime persists metadata by diffing that against the
// declared object (a label set in ReadOne would be in both, and lost).
func (m *manager) Create(ctx context.Context, res acktypes.AWSResource) (acktypes.AWSResource, error) {
	out, err := m.AWSResourceManager.Create(ctx, res)
	m.g.label(out)
	return out, err
}

// Update and Delete check again, on the observed state the runtime hands
// them (ReadOne's result), so a path that skipped ReadOne is still guarded.
func (m *manager) Update(ctx context.Context, desired, latest acktypes.AWSResource, delta *ackcompare.Delta) (acktypes.AWSResource, error) {
	if h := m.g.holder(desired, latest); h != "" {
		out := latest.DeepCopy()
		return out, m.g.refuse(h, out)
	}
	out, err := m.AWSResourceManager.Update(ctx, desired, latest, delta)
	if out != nil {
		if out == desired {
			out = out.DeepCopy()
		}
		m.g.label(out)
	}
	return out, err
}

func (m *manager) Delete(ctx context.Context, observed acktypes.AWSResource) (acktypes.AWSResource, error) {
	check, err := m.fresh(ctx, observed)
	if err == ackerr.NotFound {
		return m.AWSResourceManager.Delete(ctx, observed)
	}
	if err != nil {
		return observed, err
	}
	if h := m.g.holder(observed, check); h != "" {
		out := observed.DeepCopy()
		return out, m.g.refuse(h, out)
	}
	return m.AWSResourceManager.Delete(ctx, observed)
}
