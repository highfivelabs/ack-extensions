// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package traffic_source_attachment

import (
	"sort"

	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	"github.com/aws/aws-sdk-go-v2/aws"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/autoscaling/apis/v1alpha1"
)

func newResourceDelta(a, b *resource) *ackcompare.Delta {
	delta := ackcompare.NewDelta()
	if ackcompare.HasNilDifference(a.ko.Spec.AutoScalingGroupName, b.ko.Spec.AutoScalingGroupName) {
		delta.Add("Spec.AutoScalingGroupName", a.ko.Spec.AutoScalingGroupName, b.ko.Spec.AutoScalingGroupName)
	} else if a.ko.Spec.AutoScalingGroupName != nil && b.ko.Spec.AutoScalingGroupName != nil &&
		*a.ko.Spec.AutoScalingGroupName != *b.ko.Spec.AutoScalingGroupName {
		delta.Add("Spec.AutoScalingGroupName", a.ko.Spec.AutoScalingGroupName, b.ko.Spec.AutoScalingGroupName)
	}

	aKeys := canonicalTrafficSourceKeys(a.ko.Spec.TrafficSources)
	bKeys := canonicalTrafficSourceKeys(b.ko.Spec.TrafficSources)
	if len(aKeys) != len(bKeys) {
		delta.Add("Spec.TrafficSources", a.ko.Spec.TrafficSources, b.ko.Spec.TrafficSources)
	} else {
		for i := range aKeys {
			if aKeys[i] != bKeys[i] {
				delta.Add("Spec.TrafficSources", a.ko.Spec.TrafficSources, b.ko.Spec.TrafficSources)
				break
			}
		}
	}
	return delta
}

type trafficSourceKey struct {
	identifier string
	sourceType string
}

func keyForTrafficSource(ts *svcapitypes.TrafficSourceIdentifier) trafficSourceKey {
	if ts == nil {
		return trafficSourceKey{}
	}
	return trafficSourceKey{
		identifier: aws.ToString(ts.Identifier),
		sourceType: aws.ToString(ts.Type),
	}
}

func canonicalTrafficSourceKeys(items []*svcapitypes.TrafficSourceIdentifier) []trafficSourceKey {
	keys := make([]trafficSourceKey, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		keys = append(keys, keyForTrafficSource(item))
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].identifier != keys[j].identifier {
			return keys[i].identifier < keys[j].identifier
		}
		return keys[i].sourceType < keys[j].sourceType
	})
	return keys
}
