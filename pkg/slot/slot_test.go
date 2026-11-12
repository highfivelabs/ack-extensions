// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package slot_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	ackcondition "github.com/aws-controllers-k8s/runtime/pkg/condition"
	ackcfg "github.com/aws-controllers-k8s/runtime/pkg/config"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackmetrics "github.com/aws-controllers-k8s/runtime/pkg/metrics"
	ackrequeue "github.com/aws-controllers-k8s/runtime/pkg/requeue"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/go-logr/logr"
	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/highfivelabs/ack-extensions/pkg/fakeaws"
	"github.com/highfivelabs/ack-extensions/pkg/slot"
	ec2v1 "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
	ec2resource "github.com/highfivelabs/ack-extensions/services/ec2/pkg/resource"
	_ "github.com/highfivelabs/ack-extensions/services/ec2/pkg/resource/network_interface_permission"
	_ "github.com/highfivelabs/ack-extensions/services/ec2/pkg/resource/volume"
)

func TestResolve(t *testing.T) {
	for _, c := range []struct{ flag, env, want string }{
		{"", "", ""},
		{"", "blue", "blue"},
		{"green", "blue", "green"},
		{"green", "", "green"},
	} {
		t.Setenv(slot.EnvVar, c.env)
		fs := pflag.NewFlagSet("x", pflag.ContinueOnError)
		v := slot.BindFlags(fs)
		var args []string
		if c.flag != "" {
			args = []string{"--slot=" + c.flag}
		}
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		if got := slot.Resolve(*v); got != c.want {
			t.Errorf("flag %q env %q: slot %q, want %q", c.flag, c.env, got, c.want)
		}
	}
}

func TestAddResourceTag(t *testing.T) {
	cfg := ackcfg.Config{ResourceTags: []string{"services.k8s.aws/namespace=%K8S_NAMESPACE%", "hfl.dev/slot=old"}}
	slot.AddResourceTag(&cfg, "blue")
	if got := strings.Join(cfg.ResourceTags, ","); got != "services.k8s.aws/namespace=%K8S_NAMESPACE%,hfl.dev/slot=blue" {
		t.Errorf("resource tags %s", got)
	}
	cfg = ackcfg.Config{ResourceTags: []string{"a=b"}}
	slot.AddResourceTag(&cfg, "")
	if got := strings.Join(cfg.ResourceTags, ","); got != "a=b" {
		t.Errorf("no slot changed the resource tags: %s", got)
	}
	if err := slot.ValidateName("blue-1"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"has space", "has=equals", strings.Repeat("x", 257)} {
		if slot.ValidateName(bad) == nil {
			t.Errorf("slot name %q accepted", bad)
		}
	}
}

// inner is the resource manager under the guard: it records the writes
// it was asked for and answers ReadOne with the cloud's tags.
type inner struct {
	cloudTags map[string]string
	notFound  bool
	writes    []string
	// deletingKeepsSpec reads as the Cloud Control manager does: an object
	// being deleted keeps its declared spec, AWS's tags left out.
	deletingKeepsSpec bool
}

func (m *inner) observed(res acktypes.AWSResource) acktypes.AWSResource {
	out := res.DeepCopy()
	ko := out.RuntimeObject().(*ec2v1.Volume)
	ko.Status.VolumeID = aws.String("vol-1")
	if m.deletingKeepsSpec && ko.DeletionTimestamp != nil {
		return out
	}
	ko.Spec.Tags = nil
	for k, v := range m.cloudTags {
		ko.Spec.Tags = append(ko.Spec.Tags, &ec2v1.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	return out
}

func (m *inner) ResolveReferences(_ context.Context, _ client.Reader, r acktypes.AWSResource) (acktypes.AWSResource, bool, error) {
	return r, false, nil
}
func (m *inner) ClearResolvedReferences(r acktypes.AWSResource) acktypes.AWSResource { return r }
func (m *inner) EnsureReferences(_, latest acktypes.AWSResource) acktypes.AWSResource {
	return latest
}
func (m *inner) ReadOne(_ context.Context, r acktypes.AWSResource) (acktypes.AWSResource, error) {
	if m.notFound {
		return r, ackerr.NotFound
	}
	return m.observed(r), nil
}
func (m *inner) Create(_ context.Context, r acktypes.AWSResource) (acktypes.AWSResource, error) {
	m.writes = append(m.writes, "create")
	return m.observed(r), nil
}
func (m *inner) Update(_ context.Context, d, _ acktypes.AWSResource, _ *ackcompare.Delta) (acktypes.AWSResource, error) {
	m.writes = append(m.writes, "update")
	return d, nil
}
func (m *inner) Delete(_ context.Context, _ acktypes.AWSResource) (acktypes.AWSResource, error) {
	m.writes = append(m.writes, "delete")
	return nil, nil
}
func (m *inner) ARNFromName(string) string { return "" }
func (m *inner) LateInitialize(_ context.Context, r acktypes.AWSResource) (acktypes.AWSResource, error) {
	return r, nil
}
func (m *inner) IsSynced(context.Context, acktypes.AWSResource) (bool, error) { return true, nil }
func (m *inner) EnsureTags(context.Context, acktypes.AWSResource, acktypes.ServiceControllerMetadata) error {
	return nil
}
func (m *inner) FilterSystemTags(acktypes.AWSResource, []string) {}

// factory hands out the one inner manager, behind the volume's descriptor.
type factory struct {
	acktypes.AWSResourceManagerFactory
	m *inner
}

func (f *factory) ManagerFor(ackcfg.Config, aws.Config, logr.Logger, *ackmetrics.Metrics, acktypes.Reconciler, ackv1alpha1.AWSAccountID, ackv1alpha1.AWSRegion, ackv1alpha1.AWSResourceName) (acktypes.AWSResourceManager, error) {
	return f.m, nil
}

func volumeFactory(t *testing.T) acktypes.AWSResourceManagerFactory {
	t.Helper()
	for _, f := range ec2resource.GetManagerFactories() {
		if f.ResourceDescriptor().GroupVersionKind().Kind == "Volume" {
			return f
		}
	}
	t.Fatal("no Volume factory")
	return nil
}

// fakeManager is the controller manager the guard gets its client and
// event recorder from.
type fakeManager struct {
	client.Client
	rec *record.FakeRecorder
}

func (f *fakeManager) GetClient() client.Client { return f.Client }
func (f *fakeManager) GetEventRecorderFor(string) record.EventRecorder {
	return f.rec
}

func guarded(t *testing.T, s string, m *inner) (acktypes.AWSResourceManager, acktypes.AWSResourceDescriptor, *record.FakeRecorder) {
	rm, rd, mgr := guardedWith(t, s, m)
	return rm, rd, mgr.rec
}

// guardedWith also returns the fake manager, whose client holds the
// object "team/data".
func guardedWith(t *testing.T, s string, m *inner) (acktypes.AWSResourceManager, acktypes.AWSResourceDescriptor, *fakeManager) {
	t.Helper()
	sch := runtime.NewScheme()
	_ = ec2v1.AddToScheme(sch)
	kc := fake.NewClientBuilder().WithScheme(sch).WithObjects(&ec2v1.Volume{
		ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "team"},
	}).Build()
	mgr := &fakeManager{Client: kc, rec: record.NewFakeRecorder(10)}
	f := &factory{AWSResourceManagerFactory: volumeFactory(t), m: m}
	w := slot.Wrap([]acktypes.AWSResourceManagerFactory{f}, s, mgr, slot.GeneratedTags)
	// The inner manager makes no AWS client; the configuration is the
	// fake's all the same, as every test path's is (task noaws).
	awsCfg := fakeaws.MustParse("http://127.0.0.1:1", fakeaws.Loopback).Config("us-west-2")
	rm, err := w[0].ManagerFor(ackcfg.Config{}, awsCfg, logr.Discard(), nil, nil, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return rm, w[0].ResourceDescriptor(), mgr
}

func volume(rd acktypes.AWSResourceDescriptor, annotations map[string]string) acktypes.AWSResource {
	return rd.ResourceFromRuntimeObject(&ec2v1.Volume{
		ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "team", Annotations: annotations},
		Spec:       ec2v1.VolumeSpec{Size: aws.Int64(10)},
	})
}

func terminal(res acktypes.AWSResource) *ackv1alpha1.Condition {
	return ackcondition.Terminal(res.(acktypes.ConditionManager))
}

func TestGuard(t *testing.T) {
	ctx := context.Background()

	t.Run("no slot: the factories are returned as they are", func(t *testing.T) {
		fs := ec2resource.GetManagerFactories()
		w := slot.Wrap(fs, "", &fakeManager{rec: record.NewFakeRecorder(1)}, slot.GeneratedTags)
		for i := range fs {
			if w[i] != fs[i] {
				t.Fatalf("factory %d wrapped with no slot", i)
			}
		}
	})

	t.Run("a resource with no slot tag is claimed; the object gets the label", func(t *testing.T) {
		m := &inner{cloudTags: map[string]string{"team": "data"}}
		rm, rd, mgr := guardedWith(t, "blue", m)
		desired := volume(rd, nil)
		got, err := rm.ReadOne(ctx, desired)
		if err != nil {
			t.Fatal(err)
		}
		// Patched onto the object through the client: the runtime does
		// not persist metadata from an asynchronous create or update
		// (Cloud Control's), nor from an adoption.
		stored := &ec2v1.Volume{}
		if err := mgr.Get(ctx, client.ObjectKey{Namespace: "team", Name: "data"}, stored); err != nil {
			t.Fatal(err)
		}
		if l := stored.Labels[slot.LabelKey]; l != "blue" {
			t.Errorf("stored object's label %q, want blue", l)
		}
		// The runtime persists metadata by diffing what Update returns
		// against the declared object, so the label goes on the result,
		// never on the declared object.
		out, err := rm.Update(ctx, desired, got, nil)
		if err != nil {
			t.Fatal(err)
		}
		if l := out.MetaObject().GetLabels()[slot.LabelKey]; l != "blue" {
			t.Errorf("label %q, want blue", l)
		}
		if l := desired.MetaObject().GetLabels()[slot.LabelKey]; l != "" {
			t.Errorf("the declared object was labelled %q", l)
		}
		if strings.Join(m.writes, ",") != "update" {
			t.Errorf("writes %v", m.writes)
		}
	})

	t.Run("a resource held by this slot is written", func(t *testing.T) {
		m := &inner{cloudTags: map[string]string{slot.TagKey: "blue"}}
		rm, rd, _ := guarded(t, "blue", m)
		got, err := rm.ReadOne(ctx, volume(rd, nil))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rm.Delete(ctx, got); err != nil {
			t.Fatal(err)
		}
		if strings.Join(m.writes, ",") != "delete" {
			t.Errorf("writes %v", m.writes)
		}
	})

	t.Run("a resource held by another slot: SlotMismatch, an event, no write", func(t *testing.T) {
		m := &inner{cloudTags: map[string]string{slot.TagKey: "green"}}
		rm, rd, rec := guarded(t, "blue", m)
		desired := volume(rd, nil)
		got, err := rm.ReadOne(ctx, desired)
		// Terminal in the condition, but retried: ACK does not resync a
		// terminal object, nor reconcile on an annotation change, and the
		// way out (deletion-policy retain, the takeover annotation) is an
		// annotation change.
		var rq *ackrequeue.RequeueNeededAfter
		if !errors.As(err, &rq) || rq.Duration() != slot.RetryAfter || !errors.Is(err, ackerr.Terminal) {
			t.Fatalf("ReadOne: %v, want ackerr.Terminal requeued after %s", err, slot.RetryAfter)
		}
		want := "cloud resource is held by slot green; this controller is slot blue"
		for name, r := range map[string]acktypes.AWSResource{"returned": got, "given": desired} {
			c := terminal(r)
			if c == nil || c.Status != corev1.ConditionTrue || c.Reason == nil || *c.Reason != slot.Reason || c.Message == nil || *c.Message != want {
				t.Errorf("%s object's terminal condition %+v", name, c)
			}
		}
		select {
		case ev := <-rec.Events:
			if ev != "Warning SlotMismatch "+want {
				t.Errorf("event %q", ev)
			}
		default:
			t.Error("no event")
		}
		// Update and Delete given the observed state refuse it too.
		observed := m.observed(desired)
		if _, err := rm.Update(ctx, desired, observed, nil); !errors.Is(err, ackerr.Terminal) {
			t.Errorf("Update: %v", err)
		}
		if _, err := rm.Delete(ctx, observed); !errors.Is(err, ackerr.Terminal) {
			t.Errorf("Delete: %v", err)
		}
		if len(m.writes) != 0 {
			t.Errorf("writes made: %v", m.writes)
		}
	})

	t.Run("the takeover annotation naming the holder lets the new slot claim it", func(t *testing.T) {
		m := &inner{cloudTags: map[string]string{slot.TagKey: "green"}}
		rm, rd, _ := guarded(t, "blue", m)
		desired := volume(rd, map[string]string{slot.TakeoverAnnotation: "green"})
		got, err := rm.ReadOne(ctx, desired)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rm.Update(ctx, desired, got, nil); err != nil {
			t.Fatal(err)
		}
		// Naming another slot is no takeover.
		desired = volume(rd, map[string]string{slot.TakeoverAnnotation: "red"})
		if _, err := rm.ReadOne(ctx, desired); !errors.Is(err, ackerr.Terminal) {
			t.Errorf("takeover from red of green's resource: %v", err)
		}
	})

	t.Run("an object being deleted is judged by a fresh read of AWS's tags", func(t *testing.T) {
		m := &inner{cloudTags: map[string]string{slot.TagKey: "green"}, deletingKeepsSpec: true}
		rm, rd, _ := guarded(t, "blue", m)
		desired := volume(rd, nil)
		now := metav1.Now()
		desired.MetaObject().SetDeletionTimestamp(&now)
		observed, _ := m.ReadOne(ctx, desired) // the declared tags, no slot
		if _, err := rm.ReadOne(ctx, desired); !errors.Is(err, ackerr.Terminal) {
			t.Errorf("ReadOne: %v", err)
		}
		if _, err := rm.Delete(ctx, observed); !errors.Is(err, ackerr.Terminal) {
			t.Errorf("Delete: %v", err)
		}
		if len(m.writes) != 0 {
			t.Errorf("writes made: %v", m.writes)
		}
	})

	t.Run("not found: create, labelled", func(t *testing.T) {
		m := &inner{notFound: true}
		rm, rd, _ := guarded(t, "blue", m)
		if _, err := rm.ReadOne(ctx, volume(rd, nil)); err != ackerr.NotFound {
			t.Fatalf("ReadOne: %v", err)
		}
		got, err := rm.Create(ctx, volume(rd, nil))
		if err != nil {
			t.Fatal(err)
		}
		if l := got.MetaObject().GetLabels()[slot.LabelKey]; l != "blue" {
			t.Errorf("label %q", l)
		}
	})

	t.Run("the reference ensurer is kept", func(t *testing.T) {
		rm, _, _ := guarded(t, "blue", &inner{})
		if _, ok := rm.(acktypes.ReferenceEnsurer); !ok {
			t.Error("the wrapped manager lost EnsureReferences")
		}
	})
}

// GeneratedTags reads spec.tags only for the kinds whose read fills it
// from AWS; for any other kind the spec's tags are the object's own, not
// the cloud's, and the guard must not trust them.
func TestGeneratedTags(t *testing.T) {
	vol := &ec2v1.Volume{Spec: ec2v1.VolumeSpec{Tags: []*ec2v1.Tag{{Key: aws.String(slot.TagKey), Value: aws.String("blue")}}}}
	rd := volumeFactory(t).ResourceDescriptor()
	tags, ok := slot.GeneratedTags(schema.GroupKind{Group: "ec2.aws.hfl.dev", Kind: "Volume"}, rd.ResourceFromRuntimeObject(vol))
	if !ok || tags[slot.TagKey] != "blue" {
		t.Errorf("Volume: %v %v", tags, ok)
	}
	if _, ok := slot.GeneratedTags(schema.GroupKind{Group: "ec2.aws.hfl.dev", Kind: "NetworkInterfacePermission"}, rd.ResourceFromRuntimeObject(vol)); ok {
		t.Error("NetworkInterfacePermission is not a tagged kind")
	}
}
