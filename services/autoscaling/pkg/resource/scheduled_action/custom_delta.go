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
// package; the fields are EC2 Auto Scaling ScheduledAction's server
// defaults instead of ScalableTarget's SuspendedState.

package scheduled_action

import (
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
)

// customSetDefaults treats a server default as no drift: a field the
// desired spec leaves unset takes the value DescribeScheduledActions
// reports. For a recurring action EC2 Auto Scaling reports the next start
// time it computed, and it may report the time zone it applies.
func customSetDefaults(
	a *resource,
	b *resource,
) {
	if ackcompare.IsNil(a.ko.Spec.StartTime) && ackcompare.IsNotNil(b.ko.Spec.StartTime) {
		a.ko.Spec.StartTime = b.ko.Spec.StartTime
	}
	if ackcompare.IsNil(a.ko.Spec.TimeZone) && ackcompare.IsNotNil(b.ko.Spec.TimeZone) {
		a.ko.Spec.TimeZone = b.ko.Spec.TimeZone
	}
}
