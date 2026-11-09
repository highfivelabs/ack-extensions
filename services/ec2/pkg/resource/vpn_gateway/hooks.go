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
// getAttachedVPC, attachToVPC, detachFromVPC,
// updateTagSpecificationsInCreateRequest) and its hook templates.
// Changes: the resource is a VPN gateway (AttachVpnGateway,
// DetachVpnGateway, VpcAttachments); getAttachedVPC reads the status and
// returns no error; no route table associations; the delete detaches from
// the VPC the gateway is attached to.

package vpn_gateway

import (
	"context"

	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/highfivelabs/ack-extensions/services/ec2/pkg/tags"
)

func (rm *resourceManager) customUpdateVpnGateway(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta *ackcompare.Delta,
) (updated *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.customUpdateVpnGateway")
	defer func(err error) {
		exit(err)
	}(err)

	// Default `updated` to `desired` because it is likely
	// EC2 `modify` APIs do NOT return output, only errors.
	// If the `modify` calls (i.e. `sync`) do NOT return
	// an error, then the update was successful and desired.Spec
	// (now updated.Spec) reflects the latest resource state.
	updated = rm.concreteResource(desired.DeepCopy())

	if delta.DifferentAt("Spec.VPC") {
		if latest.ko.Spec.VPC != nil {
			if err = rm.detachFromVPC(ctx, *latest.ko.Spec.VPC, *latest.ko.Status.VPNGatewayID); err != nil {
				return nil, err
			}
		}
		if desired.ko.Spec.VPC != nil {
			desired.ko.Status.VPNGatewayID = latest.ko.Status.VPNGatewayID
			if err = rm.attachToVPC(ctx, desired); err != nil {
				return nil, err
			}
		}
	}

	if delta.DifferentAt("Spec.Tags") {
		if err := tags.Sync(
			ctx, rm.sdkapi, rm.metrics, *latest.ko.Status.VPNGatewayID,
			desired.ko.Spec.Tags, latest.ko.Spec.Tags,
		); err != nil {
			return nil, err
		}
	}

	return updated, nil
}

// getAttachedVPC returns the VPC the VPN gateway is attached to, or nil if
// it is not attached or is being detached. A VPN gateway is attached to
// at most one VPC at a time.
func (rm *resourceManager) getAttachedVPC(latest *resource) *string {
	for _, att := range latest.ko.Status.VPCAttachments {
		if att == nil || att.State == nil {
			continue
		}
		// There is no `AttachmentStatusAvailable` - so we can just check by
		// using negative logic with the constants we have, instead
		if *att.State != string(svcsdktypes.AttachmentStatusDetached) &&
			*att.State != string(svcsdktypes.AttachmentStatusDetaching) {
			return att.VPCID
		}
	}
	return nil
}

func (rm *resourceManager) attachToVPC(
	ctx context.Context,
	desired *resource,
) (err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.attachToVPC")
	defer func(err error) {
		exit(err)
	}(err)

	input := &svcsdk.AttachVpnGatewayInput{
		VpnGatewayId: desired.ko.Status.VPNGatewayID,
		VpcId:        desired.ko.Spec.VPC,
	}

	_, err = rm.sdkapi.AttachVpnGateway(ctx, input)
	rm.metrics.RecordAPICall("UPDATE", "AttachVpnGateway", err)
	return err
}

func (rm *resourceManager) detachFromVPC(
	ctx context.Context,
	vpcID string,
	vgwID string,
) (err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.detachFromVPC")
	defer func(err error) {
		exit(err)
	}(err)

	input := &svcsdk.DetachVpnGatewayInput{
		VpnGatewayId: &vgwID,
		VpcId:        &vpcID,
	}

	_, err = rm.sdkapi.DetachVpnGateway(ctx, input)
	rm.metrics.RecordAPICall("UPDATE", "DetachVpnGateway", err)
	return err
}

// updateTagSpecificationsInCreateRequest adds
// Tags defined in the Spec to CreateVpnGatewayInput.TagSpecification
// and ensures the ResourceType is always set to 'vpn-gateway'
func updateTagSpecificationsInCreateRequest(r *resource,
	input *svcsdk.CreateVpnGatewayInput) {
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
		desiredTagSpecs.ResourceType = "vpn-gateway"
		desiredTagSpecs.Tags = requestedTags
		input.TagSpecifications = []svcsdktypes.TagSpecification{desiredTagSpecs}
	}
}
