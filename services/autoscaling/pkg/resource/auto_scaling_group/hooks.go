// Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//     http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0
//
// Modified by High Five Labs for ack-extensions: copied from
// aws-controllers-k8s/autoscaling-controller v1.5.0 (56316c75),
// pkg/resource/auto_scaling_group/hooks.go (UPSTREAM.md). Changes: the
// import path of the service's own packages (autoscaling.aws.hfl.dev),
// preserveLaunchTemplateReferenceFields to keep launchTemplateRef across
// reads, and normalizeAutoScalingGroupForCompare for overrides *Ref and
// tag PropagateAtLaunch defaults.

package auto_scaling_group

import (
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/autoscaling/apis/v1alpha1"
	"github.com/highfivelabs/ack-extensions/services/autoscaling/pkg/tags"
)

var syncTags = tags.Tags

// preserveLaunchTemplateReferenceFields copies the LaunchTemplateRef companion
// fields from the desired AutoScalingGroup onto the observed AutoScalingGroup
// reconstructed from DescribeAutoScalingGroups. AWS returns only concrete
// launch template names, so without this the runtime's spec patch would drop
// launchTemplateRef from the user's CR after the first read.
func preserveLaunchTemplateReferenceFields(
	desired *svcapitypes.AutoScalingGroup,
	observed *svcapitypes.AutoScalingGroup,
) {
	if desired == nil || observed == nil {
		return
	}

	if desired.Spec.LaunchTemplate != nil && observed.Spec.LaunchTemplate != nil {
		observed.Spec.LaunchTemplate.LaunchTemplateRef = desired.Spec.LaunchTemplate.LaunchTemplateRef
	}

	if desired.Spec.MixedInstancesPolicy != nil &&
		desired.Spec.MixedInstancesPolicy.LaunchTemplate != nil &&
		observed.Spec.MixedInstancesPolicy != nil &&
		observed.Spec.MixedInstancesPolicy.LaunchTemplate != nil {
		dLT := desired.Spec.MixedInstancesPolicy.LaunchTemplate
		oLT := observed.Spec.MixedInstancesPolicy.LaunchTemplate

		if dLT.LaunchTemplateSpecification != nil && oLT.LaunchTemplateSpecification != nil {
			oLT.LaunchTemplateSpecification.LaunchTemplateRef = dLT.LaunchTemplateSpecification.LaunchTemplateRef
		}

		for idx, dOv := range dLT.Overrides {
			if idx >= len(oLT.Overrides) {
				break
			}
			oOv := oLT.Overrides[idx]
			if dOv == nil || oOv == nil ||
				dOv.LaunchTemplateSpecification == nil || oOv.LaunchTemplateSpecification == nil {
				continue
			}
			oOv.LaunchTemplateSpecification.LaunchTemplateRef = dOv.LaunchTemplateSpecification.LaunchTemplateRef
		}
	}
}

// normalizeAutoScalingGroupForCompare normalizes observed fields on b so that:
//  1. MixedInstancesPolicy.LaunchTemplate.Overrides[*].LaunchTemplateSpecification.LaunchTemplateRef
//     does not cause a whole-slice DeepEqual diff when b lacks the *Ref companion.
//  2. Tags with omitted PropagateAtLaunch (nil) on a match AWS's default (false) on b,
//     and any PropagateAtLaunch difference on a matching tag key records a Spec.Tags diff
//     (since generated delta.go only compares tag keys and values).
func normalizeAutoScalingGroupForCompare(
	delta *ackcompare.Delta,
	a *svcapitypes.AutoScalingGroup,
	b *svcapitypes.AutoScalingGroup,
) {
	if a == nil || b == nil {
		return
	}

	if a.Spec.MixedInstancesPolicy != nil &&
		a.Spec.MixedInstancesPolicy.LaunchTemplate != nil &&
		b.Spec.MixedInstancesPolicy != nil &&
		b.Spec.MixedInstancesPolicy.LaunchTemplate != nil {
		aOv := a.Spec.MixedInstancesPolicy.LaunchTemplate.Overrides
		bOv := b.Spec.MixedInstancesPolicy.LaunchTemplate.Overrides
		if len(aOv) == len(bOv) {
			for idx := range aOv {
				if aOv[idx] == nil || bOv[idx] == nil ||
					aOv[idx].LaunchTemplateSpecification == nil ||
					bOv[idx].LaunchTemplateSpecification == nil {
					continue
				}
				aSpec := aOv[idx].LaunchTemplateSpecification
				bSpec := bOv[idx].LaunchTemplateSpecification
				if bSpec.LaunchTemplateRef == nil && aSpec.LaunchTemplateRef != nil &&
					equalStringPtr(aSpec.LaunchTemplateName, bSpec.LaunchTemplateName) {
					bSpec.LaunchTemplateRef = aSpec.LaunchTemplateRef
				}
			}
		}
	}

	if len(a.Spec.Tags) > 0 && len(b.Spec.Tags) > 0 {
		omittedPropagate := make(map[string]bool, len(a.Spec.Tags))
		desiredPropagate := make(map[string]bool, len(a.Spec.Tags))
		for _, t := range a.Spec.Tags {
			if t == nil || t.Key == nil {
				continue
			}
			if t.PropagateAtLaunch == nil {
				omittedPropagate[*t.Key] = true
			} else {
				desiredPropagate[*t.Key] = *t.PropagateAtLaunch
			}
		}
		propagateDiff := false
		for _, t := range b.Spec.Tags {
			if t == nil || t.Key == nil {
				continue
			}
			if omittedPropagate[*t.Key] && t.PropagateAtLaunch != nil && !*t.PropagateAtLaunch {
				t.PropagateAtLaunch = nil
				continue
			}
			bProp := false
			if t.PropagateAtLaunch != nil {
				bProp = *t.PropagateAtLaunch
			}
			if aProp, ok := desiredPropagate[*t.Key]; ok && aProp != bProp {
				propagateDiff = true
			} else if omittedPropagate[*t.Key] && bProp {
				propagateDiff = true
			}
		}
		if propagateDiff && delta != nil {
			delta.Add("Spec.Tags", a.Spec.Tags, b.Spec.Tags)
		}
	}
}

func equalStringPtr(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// preserveTagPropagateAtLaunch restores per-tag PropagateAtLaunch from existing
// onto merged after EnsureTags/FilterSystemTags rebuild Spec.Tags via acktags.Tags
// (which only carries Key and Value).
func preserveTagPropagateAtLaunch(existing, merged []*svcapitypes.Tag) {
	propagate := make(map[string]*bool, len(existing))
	for _, t := range existing {
		if t != nil && t.Key != nil && t.PropagateAtLaunch != nil {
			propagate[*t.Key] = t.PropagateAtLaunch
		}
	}
	for _, t := range merged {
		if t != nil && t.Key != nil {
			if p, ok := propagate[*t.Key]; ok {
				t.PropagateAtLaunch = p
			}
		}
	}
}
