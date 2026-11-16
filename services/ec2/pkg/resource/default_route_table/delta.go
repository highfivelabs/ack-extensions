// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package default_route_table

import (
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	"github.com/aws/aws-sdk-go-v2/aws"
	"k8s.io/apimachinery/pkg/api/equality"

	"github.com/highfivelabs/ack-extensions/services/ec2/pkg/tags"
)

// newResourceDelta compares the VPC, the routes (as ACK's RouteTable
// does: by their targets, as a set), the propagating gateways (as a set)
// and the tags.
func newResourceDelta(a *resource, b *resource) *ackcompare.Delta {
	delta := ackcompare.NewDelta()
	if (a == nil && b != nil) || (a != nil && b == nil) {
		delta.Add("", a, b)
		return delta
	}
	if aws.ToString(a.ko.Spec.VPCID) != aws.ToString(b.ko.Spec.VPCID) {
		delta.Add("Spec.VPCID", a.ko.Spec.VPCID, b.ko.Spec.VPCID)
	}
	if !equality.Semantic.DeepEqual(a.ko.Spec.VPCRef, b.ko.Spec.VPCRef) {
		delta.Add("Spec.VPCRef", a.ko.Spec.VPCRef, b.ko.Spec.VPCRef)
	}
	if routesDiffer(a.ko.Spec.Routes, b.ko.Spec.Routes) {
		delta.Add("Spec.Routes", a.ko.Spec.Routes, b.ko.Spec.Routes)
	}
	if !sameSet(a.ko.Spec.PropagatingVGWs, b.ko.Spec.PropagatingVGWs) {
		delta.Add("Spec.PropagatingVGWs", a.ko.Spec.PropagatingVGWs, b.ko.Spec.PropagatingVGWs)
	}
	if !tags.Equal(a.ko.Spec.Tags, b.ko.Spec.Tags) {
		delta.Add("Spec.Tags", a.ko.Spec.Tags, b.ko.Spec.Tags)
	}
	return delta
}

func sameSet(a, b []*string) bool {
	m := map[string]bool{}
	for _, s := range a {
		m[aws.ToString(s)] = true
	}
	n := map[string]bool{}
	for _, s := range b {
		if !m[aws.ToString(s)] {
			return false
		}
		n[aws.ToString(s)] = true
	}
	return len(m) == len(n)
}
