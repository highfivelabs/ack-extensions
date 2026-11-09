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
// package; the field is a login profile's passwordResetRequired instead
// of ScalableTarget's SuspendedState.

package login_profile

import (
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
)

// customSetDefaults treats a server default as no drift: when the desired
// spec leaves passwordResetRequired unset, it takes the value
// GetLoginProfile reports (false unless set).
func customSetDefaults(
	a *resource,
	b *resource,
) {
	if ackcompare.IsNil(a.ko.Spec.PasswordResetRequired) && ackcompare.IsNotNil(b.ko.Spec.PasswordResetRequired) {
		a.ko.Spec.PasswordResetRequired = b.ko.Spec.PasswordResetRequired
	}
}
