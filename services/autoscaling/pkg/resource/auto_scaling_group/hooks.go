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
	"context"
	"sort"

	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	ackmetrics "github.com/aws-controllers-k8s/runtime/pkg/metrics"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/autoscaling"

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

	normalizeEnabledMetrics(a.Spec.EnabledMetrics, b.Spec.EnabledMetrics)
	normalizeSuspendedProcesses(a.Spec.SuspendedProcesses, b.Spec.SuspendedProcesses)
}

const defaultMetricGranularity = "1Minute"

func normalizeEnabledMetrics(a, b []*svcapitypes.EnabledMetric) {
	omittedGranularity := make(map[string]bool, len(a))
	for _, m := range a {
		if m != nil && m.Metric != nil && aws.ToString(m.Granularity) == "" {
			omittedGranularity[*m.Metric] = true
		}
	}
	for _, m := range b {
		if m != nil && m.Metric != nil && omittedGranularity[*m.Metric] && aws.ToString(m.Granularity) == defaultMetricGranularity {
			m.Granularity = nil
		}
	}
	sortEnabledMetrics(a)
	sortEnabledMetrics(b)
}

func sortEnabledMetrics(items []*svcapitypes.EnabledMetric) {
	sort.Slice(items, func(i, j int) bool {
		if items[i] == nil || items[j] == nil {
			return items[i] == nil && items[j] != nil
		}
		mi, mj := aws.ToString(items[i].Metric), aws.ToString(items[j].Metric)
		if mi != mj {
			return mi < mj
		}
		return aws.ToString(items[i].Granularity) < aws.ToString(items[j].Granularity)
	})
}

func normalizeSuspendedProcesses(a, b []*svcapitypes.SuspendedProcess) {
	omittedReason := make(map[string]bool, len(a))
	for _, p := range a {
		if p != nil && p.ProcessName != nil && p.SuspensionReason == nil {
			omittedReason[*p.ProcessName] = true
		}
	}
	for _, p := range b {
		if p != nil && p.ProcessName != nil && omittedReason[*p.ProcessName] {
			p.SuspensionReason = nil
		}
	}
	sortSuspendedProcesses(a)
	sortSuspendedProcesses(b)
}

func sortSuspendedProcesses(items []*svcapitypes.SuspendedProcess) {
	sort.Slice(items, func(i, j int) bool {
		if items[i] == nil || items[j] == nil {
			return items[i] == nil && items[j] != nil
		}
		return aws.ToString(items[i].ProcessName) < aws.ToString(items[j].ProcessName)
	})
}

type metricsAndProcessesClient interface {
	EnableMetricsCollection(context.Context, *svcsdk.EnableMetricsCollectionInput, ...func(*svcsdk.Options)) (*svcsdk.EnableMetricsCollectionOutput, error)
	DisableMetricsCollection(context.Context, *svcsdk.DisableMetricsCollectionInput, ...func(*svcsdk.Options)) (*svcsdk.DisableMetricsCollectionOutput, error)
	SuspendProcesses(context.Context, *svcsdk.SuspendProcessesInput, ...func(*svcsdk.Options)) (*svcsdk.SuspendProcessesOutput, error)
	ResumeProcesses(context.Context, *svcsdk.ResumeProcessesInput, ...func(*svcsdk.Options)) (*svcsdk.ResumeProcessesOutput, error)
}

func syncEnabledMetrics(
	ctx context.Context,
	desired []*svcapitypes.EnabledMetric,
	latest []*svcapitypes.EnabledMetric,
	asgName string,
	client metricsAndProcessesClient,
	metrics *ackmetrics.Metrics,
) error {
	desiredMap := make(map[string]string, len(desired))
	for _, m := range desired {
		if m == nil || aws.ToString(m.Metric) == "" {
			continue
		}
		gran := aws.ToString(m.Granularity)
		if gran == "" {
			gran = defaultMetricGranularity
		}
		desiredMap[*m.Metric] = gran
	}

	latestMap := make(map[string]string, len(latest))
	for _, m := range latest {
		if m == nil || aws.ToString(m.Metric) == "" {
			continue
		}
		gran := aws.ToString(m.Granularity)
		if gran == "" {
			gran = defaultMetricGranularity
		}
		latestMap[*m.Metric] = gran
	}

	var toDisable []string
	for metric := range latestMap {
		if _, ok := desiredMap[metric]; !ok {
			toDisable = append(toDisable, metric)
		}
	}
	sort.Strings(toDisable)
	if len(toDisable) > 0 {
		_, err := client.DisableMetricsCollection(ctx, &svcsdk.DisableMetricsCollectionInput{
			AutoScalingGroupName: &asgName,
			Metrics:              toDisable,
		})
		if metrics != nil {
			metrics.RecordAPICall("UPDATE", "DisableMetricsCollection", err)
		}
		if err != nil {
			return err
		}
	}

	byGranularity := make(map[string][]string)
	for metric, gran := range desiredMap {
		if prevGran, ok := latestMap[metric]; !ok || prevGran != gran {
			byGranularity[gran] = append(byGranularity[gran], metric)
		}
	}
	granularities := make([]string, 0, len(byGranularity))
	for gran := range byGranularity {
		granularities = append(granularities, gran)
	}
	sort.Strings(granularities)
	for _, gran := range granularities {
		list := byGranularity[gran]
		sort.Strings(list)
		g := gran
		_, err := client.EnableMetricsCollection(ctx, &svcsdk.EnableMetricsCollectionInput{
			AutoScalingGroupName: &asgName,
			Granularity:          &g,
			Metrics:              list,
		})
		if metrics != nil {
			metrics.RecordAPICall("UPDATE", "EnableMetricsCollection", err)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func syncSuspendedProcesses(
	ctx context.Context,
	desired []*svcapitypes.SuspendedProcess,
	latest []*svcapitypes.SuspendedProcess,
	asgName string,
	client metricsAndProcessesClient,
	metrics *ackmetrics.Metrics,
) error {
	desiredSet := make(map[string]struct{}, len(desired))
	for _, p := range desired {
		if p != nil && aws.ToString(p.ProcessName) != "" {
			desiredSet[*p.ProcessName] = struct{}{}
		}
	}
	latestSet := make(map[string]struct{}, len(latest))
	for _, p := range latest {
		if p != nil && aws.ToString(p.ProcessName) != "" {
			latestSet[*p.ProcessName] = struct{}{}
		}
	}

	var toResume []string
	for proc := range latestSet {
		if _, ok := desiredSet[proc]; !ok {
			toResume = append(toResume, proc)
		}
	}
	sort.Strings(toResume)
	if len(toResume) > 0 {
		_, err := client.ResumeProcesses(ctx, &svcsdk.ResumeProcessesInput{
			AutoScalingGroupName: &asgName,
			ScalingProcesses:     toResume,
		})
		if metrics != nil {
			metrics.RecordAPICall("UPDATE", "ResumeProcesses", err)
		}
		if err != nil {
			return err
		}
	}

	var toSuspend []string
	for proc := range desiredSet {
		if _, ok := latestSet[proc]; !ok {
			toSuspend = append(toSuspend, proc)
		}
	}
	sort.Strings(toSuspend)
	if len(toSuspend) > 0 {
		_, err := client.SuspendProcesses(ctx, &svcsdk.SuspendProcessesInput{
			AutoScalingGroupName: &asgName,
			ScalingProcesses:     toSuspend,
		})
		if metrics != nil {
			metrics.RecordAPICall("UPDATE", "SuspendProcesses", err)
		}
		if err != nil {
			return err
		}
	}
	return nil
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
