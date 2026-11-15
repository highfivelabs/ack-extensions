// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package default_security_group

import (
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	"k8s.io/apimachinery/pkg/api/equality"

	"github.com/highfivelabs/ack-extensions/services/ec2/pkg/tags"
)

// newResourceDelta compares the VPC, the tags, and the rules after ACK's
// SecurityGroup canonicalisation (customPostCompare, rules.go), so EC2's
// own regrouping of rules on read is no drift.
func newResourceDelta(a *resource, b *resource) *ackcompare.Delta {
	delta := ackcompare.NewDelta()
	if (a == nil && b != nil) || (a != nil && b == nil) {
		delta.Add("", a, b)
		return delta
	}
	if ackcompare.HasNilDifference(a.ko.Spec.VPCID, b.ko.Spec.VPCID) ||
		(a.ko.Spec.VPCID != nil && b.ko.Spec.VPCID != nil && *a.ko.Spec.VPCID != *b.ko.Spec.VPCID) {
		delta.Add("Spec.VPCID", a.ko.Spec.VPCID, b.ko.Spec.VPCID)
	}
	if !equality.Semantic.DeepEqual(a.ko.Spec.VPCRef, b.ko.Spec.VPCRef) {
		delta.Add("Spec.VPCRef", a.ko.Spec.VPCRef, b.ko.Spec.VPCRef)
	}
	if !tags.Equal(a.ko.Spec.Tags, b.ko.Spec.Tags) {
		delta.Add("Spec.Tags", a.ko.Spec.Tags, b.ko.Spec.Tags)
	}
	customPostCompare(delta, a, b)
	return delta
}
