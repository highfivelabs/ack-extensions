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
// package; the fields are a schedule's server defaults instead of
// ScalableTarget's SuspendedState.

package schedule

import (
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
)

// customSetDefaults treats a server default as no drift: a field the
// desired spec leaves unset takes the value GetSchedule reports.
// Scheduler defaults the group ("default"), the time zone (UTC), the
// state (ENABLED), the action after completion (NONE) and the target's
// retry policy (185 attempts over a day).
func customSetDefaults(
	a *resource,
	b *resource,
) {
	if ackcompare.IsNil(a.ko.Spec.GroupName) && ackcompare.IsNil(a.ko.Spec.GroupRef) && ackcompare.IsNotNil(b.ko.Spec.GroupName) {
		a.ko.Spec.GroupName = b.ko.Spec.GroupName
	}
	if ackcompare.IsNil(a.ko.Spec.ScheduleExpressionTimezone) && ackcompare.IsNotNil(b.ko.Spec.ScheduleExpressionTimezone) {
		a.ko.Spec.ScheduleExpressionTimezone = b.ko.Spec.ScheduleExpressionTimezone
	}
	if ackcompare.IsNil(a.ko.Spec.State) && ackcompare.IsNotNil(b.ko.Spec.State) {
		a.ko.Spec.State = b.ko.Spec.State
	}
	if ackcompare.IsNil(a.ko.Spec.ActionAfterCompletion) && ackcompare.IsNotNil(b.ko.Spec.ActionAfterCompletion) {
		a.ko.Spec.ActionAfterCompletion = b.ko.Spec.ActionAfterCompletion
	}
	if a.ko.Spec.Target != nil && b.ko.Spec.Target != nil &&
		ackcompare.IsNil(a.ko.Spec.Target.RetryPolicy) && ackcompare.IsNotNil(b.ko.Spec.Target.RetryPolicy) {
		a.ko.Spec.Target.RetryPolicy = b.ko.Spec.Target.RetryPolicy
	}
}
