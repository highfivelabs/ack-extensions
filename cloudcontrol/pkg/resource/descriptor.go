// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package resource

import (
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	rtclient "sigs.k8s.io/controller-runtime/pkg/client"
	k8sctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/cfn"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
)

type descriptor struct{ t *registry.Type }

var _ acktypes.AWSResourceDescriptor = &descriptor{}

func (d *descriptor) finalizer() string { return "finalizers." + d.t.Group + "/" + d.t.Kind }

func (d *descriptor) GroupVersionKind() schema.GroupVersionKind { return d.t.GVK() }

func (d *descriptor) EmptyRuntimeObject() rtclient.Object {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(d.t.GVK())
	return u
}

func (d *descriptor) ResourceFromRuntimeObject(o rtclient.Object) acktypes.AWSResource {
	u := o.(*unstructured.Unstructured)
	if u.GetObjectKind().GroupVersionKind().Empty() {
		u.SetGroupVersionKind(d.t.GVK())
	}
	return &resource{t: d.t, u: u}
}

// Delta is where desired and latest differ, on the fields desired sets
// (cfn.Schema.Satisfies has the rules). Paths are ACK's: Spec.<field>.
func (d *descriptor) Delta(a, b acktypes.AWSResource) *ackcompare.Delta {
	delta := ackcompare.NewDelta()
	ra, rb := a.(*resource), b.(*resource)
	s := d.t.Schema()
	names, _ := cfn.Names(s.Properties)
	da := s.ToCFN(s.Root(), cfn.Normalise(ra.spec())).(map[string]any)
	db := s.ToCFN(s.Root(), cfn.Normalise(rb.spec())).(map[string]any)
	for _, k := range sortedMapKeys(da) {
		if da[k] == nil {
			continue
		}
		ov, ok := db[k]
		if !ok || !s.Satisfies(s.ComparedProperty(k), da[k], ov) {
			f := names.ToField[k]
			if f == "" {
				f = k
			}
			delta.Add("Spec."+f, da[k], ov)
		}
	}
	return delta
}

func (d *descriptor) IsManaged(res acktypes.AWSResource) bool {
	for _, f := range res.RuntimeObject().GetFinalizers() {
		if f == d.finalizer() {
			return true
		}
	}
	return false
}

func (d *descriptor) MarkManaged(res acktypes.AWSResource) {
	k8sctrlutil.AddFinalizer(res.RuntimeObject(), d.finalizer())
}

func (d *descriptor) MarkUnmanaged(res acktypes.AWSResource) {
	k8sctrlutil.RemoveFinalizer(res.RuntimeObject(), d.finalizer())
}

func (d *descriptor) MarkAdopted(res acktypes.AWSResource) {
	obj := res.RuntimeObject()
	a := obj.GetAnnotations()
	if a == nil {
		a = map[string]string{}
	}
	a[ackv1alpha1.AnnotationAdopted] = "true"
	obj.SetAnnotations(a)
}
