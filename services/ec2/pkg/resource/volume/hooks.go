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
// aws-controllers-k8s/ec2-controller v1.23.1 (9303654c),
// pkg/resource/internet_gateway/hooks.go (customUpdateInternetGateway,
// updateTagSpecificationsInCreateRequest). Changes: the resource is an EBS
// volume; its update calls ModifyVolume for the size, type, IOPS,
// throughput and multi-attach, then syncs the tags; no VPC attachment or
// route tables.

package volume

import (
	"context"

	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/highfivelabs/ack-extensions/services/ec2/pkg/tags"
)

// modifiable are the fields ModifyVolume changes.
var modifiable = []string{"Spec.Size", "Spec.VolumeType", "Spec.IOPS", "Spec.Throughput", "Spec.MultiAttachEnabled"}

func (rm *resourceManager) customUpdateVolume(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta *ackcompare.Delta,
) (updated *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.customUpdateVolume")
	defer func(err error) {
		exit(err)
	}(err)

	// Default `updated` to `desired` because it is likely
	// EC2 `modify` APIs do NOT return output, only errors.
	updated = rm.concreteResource(desired.DeepCopy())
	updated.SetStatus(latest)

	modify := false
	for _, f := range modifiable {
		if delta.DifferentAt(f) {
			modify = true
		}
	}
	if modify {
		input := &svcsdk.ModifyVolumeInput{VolumeId: latest.ko.Status.VolumeID}
		if delta.DifferentAt("Spec.Size") && desired.ko.Spec.Size != nil {
			input.Size = int32Ptr(*desired.ko.Spec.Size)
		}
		if delta.DifferentAt("Spec.VolumeType") && desired.ko.Spec.VolumeType != nil {
			input.VolumeType = svcsdktypes.VolumeType(*desired.ko.Spec.VolumeType)
		}
		if delta.DifferentAt("Spec.IOPS") && desired.ko.Spec.IOPS != nil {
			input.Iops = int32Ptr(*desired.ko.Spec.IOPS)
		}
		if delta.DifferentAt("Spec.Throughput") && desired.ko.Spec.Throughput != nil {
			input.Throughput = int32Ptr(*desired.ko.Spec.Throughput)
		}
		if delta.DifferentAt("Spec.MultiAttachEnabled") {
			input.MultiAttachEnabled = desired.ko.Spec.MultiAttachEnabled
		}
		_, err = rm.sdkapi.ModifyVolume(ctx, input)
		rm.metrics.RecordAPICall("UPDATE", "ModifyVolume", err)
		if err != nil {
			return nil, err
		}
	}

	if delta.DifferentAt("Spec.Tags") {
		if err := tags.Sync(
			ctx, rm.sdkapi, rm.metrics, *latest.ko.Status.VolumeID,
			desired.ko.Spec.Tags, latest.ko.Spec.Tags,
		); err != nil {
			return nil, err
		}
	}

	return updated, nil
}

func int32Ptr(v int64) *int32 {
	i := int32(v)
	return &i
}

// updateTagSpecificationsInCreateRequest adds
// Tags defined in the Spec to CreateVolumeInput.TagSpecification
// and ensures the ResourceType is always set to 'volume'
func updateTagSpecificationsInCreateRequest(r *resource,
	input *svcsdk.CreateVolumeInput) {
	input.TagSpecifications = nil
	desiredTagSpecs := svcsdktypes.TagSpecification{}
	if r.ko.Spec.Tags != nil {
		requestedTags := []svcsdktypes.Tag{}
		for _, desiredTag := range r.ko.Spec.Tags {
			// Add in tags defined in the Spec
			tag := svcsdktypes.Tag{}
			if desiredTag.Key != nil && desiredTag.Value != nil {
				tag.Key = desiredTag.Key
				tag.Value = desiredTag.Value
			}
			requestedTags = append(requestedTags, tag)
		}
		desiredTagSpecs.ResourceType = "volume"
		desiredTagSpecs.Tags = requestedTags
		input.TagSpecifications = []svcsdktypes.TagSpecification{desiredTagSpecs}
	}
}
