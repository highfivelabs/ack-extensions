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
// package; the fields are an EBS volume's server defaults instead of
// ScalableTarget's SuspendedState.

package volume

import (
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
)

// customSetDefaults treats a server default as no drift: a field the
// desired spec leaves unset takes the value DescribeVolumes reports. EC2
// defaults the volume type, the IOPS and throughput of the types that have
// them, multi-attach (off) and encryption (the account's default), each
// reported for some volume types only.
func customSetDefaults(
	a *resource,
	b *resource,
) {
	if ackcompare.IsNil(a.ko.Spec.VolumeType) && ackcompare.IsNotNil(b.ko.Spec.VolumeType) {
		a.ko.Spec.VolumeType = b.ko.Spec.VolumeType
	}
	if ackcompare.IsNil(a.ko.Spec.IOPS) && ackcompare.IsNotNil(b.ko.Spec.IOPS) {
		a.ko.Spec.IOPS = b.ko.Spec.IOPS
	}
	if ackcompare.IsNil(a.ko.Spec.Throughput) && ackcompare.IsNotNil(b.ko.Spec.Throughput) {
		a.ko.Spec.Throughput = b.ko.Spec.Throughput
	}
	if ackcompare.IsNil(a.ko.Spec.MultiAttachEnabled) && ackcompare.IsNotNil(b.ko.Spec.MultiAttachEnabled) {
		a.ko.Spec.MultiAttachEnabled = b.ko.Spec.MultiAttachEnabled
	}
	if ackcompare.IsNil(a.ko.Spec.Encrypted) && ackcompare.IsNotNil(b.ko.Spec.Encrypted) {
		a.ko.Spec.Encrypted = b.ko.Spec.Encrypted
	}
	if ackcompare.IsNil(a.ko.Spec.KMSKeyID) && ackcompare.IsNotNil(b.ko.Spec.KMSKeyID) {
		a.ko.Spec.KMSKeyID = b.ko.Spec.KMSKeyID
	}
}
