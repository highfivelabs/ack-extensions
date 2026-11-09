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
// aws-controllers-k8s/applicationautoscaling-controller v1.9.0
// (009baef3), pkg/resource/scalable_target/custom_delta.go. Changes:
// package; the fields are EC2 Auto Scaling ScalingPolicy's
// server defaults instead of ScalableTarget's SuspendedState.

package scaling_policy

import (
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
)

// customSetDefaults treats a server default as no drift: a field the
// desired spec leaves unset takes the value DescribePolicies reports.
// EC2 Auto Scaling defaults the policy type (SimpleScaling), enabled
// (true), the metric aggregation type of a step policy (Average), a
// target-tracking policy's disableScaleIn (false), and may report a
// cooldown and an instance warmup it applies. Late initialization cannot
// do this: the server reports each of these for some policy types only,
// so ACK's late initialization would wait for the rest forever.
func customSetDefaults(
	a *resource,
	b *resource,
) {
	if ackcompare.IsNil(a.ko.Spec.PolicyType) && ackcompare.IsNotNil(b.ko.Spec.PolicyType) {
		a.ko.Spec.PolicyType = b.ko.Spec.PolicyType
	}
	if ackcompare.IsNil(a.ko.Spec.Enabled) && ackcompare.IsNotNil(b.ko.Spec.Enabled) {
		a.ko.Spec.Enabled = b.ko.Spec.Enabled
	}
	if ackcompare.IsNil(a.ko.Spec.MetricAggregationType) && ackcompare.IsNotNil(b.ko.Spec.MetricAggregationType) {
		a.ko.Spec.MetricAggregationType = b.ko.Spec.MetricAggregationType
	}
	if ackcompare.IsNil(a.ko.Spec.Cooldown) && ackcompare.IsNotNil(b.ko.Spec.Cooldown) {
		a.ko.Spec.Cooldown = b.ko.Spec.Cooldown
	}
	if ackcompare.IsNil(a.ko.Spec.EstimatedInstanceWarmup) && ackcompare.IsNotNil(b.ko.Spec.EstimatedInstanceWarmup) {
		a.ko.Spec.EstimatedInstanceWarmup = b.ko.Spec.EstimatedInstanceWarmup
	}
	if ackcompare.IsNotNil(a.ko.Spec.TargetTrackingConfiguration) && ackcompare.IsNotNil(b.ko.Spec.TargetTrackingConfiguration) {
		if ackcompare.IsNil(a.ko.Spec.TargetTrackingConfiguration.DisableScaleIn) && ackcompare.IsNotNil(b.ko.Spec.TargetTrackingConfiguration.DisableScaleIn) {
			a.ko.Spec.TargetTrackingConfiguration.DisableScaleIn = b.ko.Spec.TargetTrackingConfiguration.DisableScaleIn
		}
	}
}
