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
// package; the fields are the four settings of an account's public access
// block instead of ScalableTarget's SuspendedState, and only S3's default
// (false) taken from the latest state.

package public_access_block

import (
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
)

// customSetDefaults treats a server default as no drift: a setting the
// desired spec leaves unset is false, as PutPublicAccessBlock takes it, so
// it matches the false GetPublicAccessBlock reports (S3 reports all four).
// One set to true outside Kubernetes is still drift, and put back.
func customSetDefaults(
	a *resource,
	b *resource,
) {
	if ackcompare.IsNil(a.ko.Spec.PublicAccessBlockConfiguration) || ackcompare.IsNil(b.ko.Spec.PublicAccessBlockConfiguration) {
		return
	}
	ac, bc := a.ko.Spec.PublicAccessBlockConfiguration, b.ko.Spec.PublicAccessBlockConfiguration
	if ackcompare.IsNil(ac.BlockPublicACLs) && ackcompare.IsNotNil(bc.BlockPublicACLs) && !*bc.BlockPublicACLs {
		ac.BlockPublicACLs = bc.BlockPublicACLs
	}
	if ackcompare.IsNil(ac.IgnorePublicACLs) && ackcompare.IsNotNil(bc.IgnorePublicACLs) && !*bc.IgnorePublicACLs {
		ac.IgnorePublicACLs = bc.IgnorePublicACLs
	}
	if ackcompare.IsNil(ac.BlockPublicPolicy) && ackcompare.IsNotNil(bc.BlockPublicPolicy) && !*bc.BlockPublicPolicy {
		ac.BlockPublicPolicy = bc.BlockPublicPolicy
	}
	if ackcompare.IsNil(ac.RestrictPublicBuckets) && ackcompare.IsNotNil(bc.RestrictPublicBuckets) && !*bc.RestrictPublicBuckets {
		ac.RestrictPublicBuckets = bc.RestrictPublicBuckets
	}
}
