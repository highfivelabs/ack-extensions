// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package default_network_acl

import (
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	"github.com/aws/aws-sdk-go-v2/aws"
	"k8s.io/apimachinery/pkg/api/equality"

	"github.com/highfivelabs/ack-extensions/services/ec2/pkg/tags"
)

// newResourceDelta compares the VPC, the entries (canonically, without
// EC2's 32767 entries), the subnets (as sets) and the tags.
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
	if !entriesEqual(a.ko.Spec.Entries, b.ko.Spec.Entries) {
		delta.Add("Spec.Entries", a.ko.Spec.Entries, b.ko.Spec.Entries)
	}
	if !sameSet(a.ko.Spec.SubnetIDs, b.ko.Spec.SubnetIDs) {
		delta.Add("Spec.SubnetIDs", a.ko.Spec.SubnetIDs, b.ko.Spec.SubnetIDs)
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
