// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package default_vpc

import (
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"

	"github.com/highfivelabs/ack-extensions/services/ec2/pkg/tags"
)

// newResourceDelta compares the settings and the tags.
func newResourceDelta(a *resource, b *resource) *ackcompare.Delta {
	delta := ackcompare.NewDelta()
	if (a == nil && b != nil) || (a != nil && b == nil) {
		delta.Add("", a, b)
		return delta
	}
	for _, f := range []struct {
		name string
		a, b *bool
	}{
		{"Spec.EnableDNSSupport", a.ko.Spec.EnableDNSSupport, b.ko.Spec.EnableDNSSupport},
		{"Spec.EnableDNSHostnames", a.ko.Spec.EnableDNSHostnames, b.ko.Spec.EnableDNSHostnames},
		{"Spec.EnableNetworkAddressUsageMetrics", a.ko.Spec.EnableNetworkAddressUsageMetrics, b.ko.Spec.EnableNetworkAddressUsageMetrics},
	} {
		if ackcompare.HasNilDifference(f.a, f.b) || (f.a != nil && f.b != nil && *f.a != *f.b) {
			delta.Add(f.name, f.a, f.b)
		}
	}
	if !tags.Equal(a.ko.Spec.Tags, b.ko.Spec.Tags) {
		delta.Add("Spec.Tags", a.ko.Spec.Tags, b.ko.Spec.Tags)
	}
	return delta
}
