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
// Modified by High Five Labs for ack-extensions: the tag calls (getTags,
// syncTags and the conversions) are copied from
// aws-controllers-k8s/eventbridge-controller v1.9.2 (fd084a0b),
// pkg/resource/event_bus/hooks.go. Changes: package; Organizations' client
// and its ResourceId field names, and ListTagsForResource's paging; the
// policy summary hooks (setFromPolicy) are written here.

package policy

import (
	"context"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/organizations"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/organizations/apis/v1alpha1"
)

// setIdentifiers sets what the generator cannot read from the response,
// because Organizations wraps a policy's id, ARN, name, description and
// type in a PolicySummary: the identifier (status.id), the ARN, and the
// spec's name, description and type.
func (rm *resourceManager) setIdentifiers(
	ko *svcapitypes.Policy,
	p *svcsdktypes.Policy,
) {
	if p == nil || p.PolicySummary == nil {
		return
	}
	s := p.PolicySummary
	ko.Status.ID = s.Id
	if s.Arn != nil {
		if ko.Status.ACKResourceMetadata == nil {
			ko.Status.ACKResourceMetadata = &ackv1alpha1.ResourceMetadata{}
		}
		arn := ackv1alpha1.AWSResourceName(*s.Arn)
		ko.Status.ACKResourceMetadata.ARN = &arn
	}
	ko.Spec.Name = s.Name
	ko.Spec.Description = s.Description
	if s.Type != "" {
		ko.Spec.Type = aws.String(string(s.Type))
	}
}

// setResourceAdditionalFields sets the fields that DescribePolicy does not
// return: the policy's tags.
func (rm *resourceManager) setResourceAdditionalFields(
	ctx context.Context,
	ko *svcapitypes.Policy,
) (err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.setResourceAdditionalFields")
	defer func() { exit(err) }()

	if ko.Status.ID != nil && *ko.Status.ID != "" {
		ko.Spec.Tags, err = rm.getTags(ctx, *ko.Status.ID)
		if err != nil {
			return err
		}
	}
	return nil
}

// getTags retrieves a policy's tags.
func (rm *resourceManager) getTags(
	ctx context.Context,
	policyID string,
) (tags []*svcapitypes.Tag, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.getTags")
	defer func() { exit(err) }()

	var next *string
	for {
		var resp *svcsdk.ListTagsForResourceOutput
		resp, err = rm.sdkapi.ListTagsForResource(
			ctx,
			&svcsdk.ListTagsForResourceInput{
				ResourceId: &policyID,
				NextToken:  next,
			},
		)
		rm.metrics.RecordAPICall("GET", "ListTagsForResource", err)
		if err != nil {
			return nil, err
		}
		for _, tag := range resp.Tags {
			tags = append(tags, &svcapitypes.Tag{
				Key:   tag.Key,
				Value: tag.Value,
			})
		}
		if resp.NextToken == nil || *resp.NextToken == "" {
			return tags, nil
		}
		next = resp.NextToken
	}
}

// syncTags updates a policy's tags.
func (rm *resourceManager) syncTags(
	ctx context.Context,
	desired *resource,
	latest *resource,
) (err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.syncTags")
	defer func() { exit(err) }()

	from, fromOrder := convertToOrderedACKTags(latest.ko.Spec.Tags)
	to, toOrder := convertToOrderedACKTags(desired.ko.Spec.Tags)
	added, _, removed := ackcompare.GetTagsDifference(from, to)
	toAdd := fromACKTags(added, toOrder)
	toDelete := fromACKTags(removed, fromOrder)

	if len(toAdd) == 0 && len(toDelete) == 0 {
		return nil
	}

	if len(toDelete) > 0 {
		_, err = rm.sdkapi.UntagResource(
			ctx,
			&svcsdk.UntagResourceInput{
				ResourceId: latest.ko.Status.ID,
				TagKeys:    sdkTagStringsFromResourceTags(toDelete),
			})
		rm.metrics.RecordAPICall("UPDATE", "UntagResource", err)
		if err != nil {
			return err
		}
	}

	if len(toAdd) > 0 {
		_, err = rm.sdkapi.TagResource(
			ctx,
			&svcsdk.TagResourceInput{
				ResourceId: latest.ko.Status.ID,
				Tags:       sdkTagsFromResourceTags(toAdd),
			})
		rm.metrics.RecordAPICall("UPDATE", "TagResource", err)
		if err != nil {
			return err
		}
	}
	return nil
}

// sdkTagsFromResourceTags transforms a *svcapitypes.Tag array to a *svcsdk.Tag array.
func sdkTagsFromResourceTags(rTags []*svcapitypes.Tag) []svcsdktypes.Tag {
	tags := make([]svcsdktypes.Tag, len(rTags))
	for i := range rTags {
		tags[i] = svcsdktypes.Tag{
			Key:   rTags[i].Key,
			Value: rTags[i].Value,
		}
	}
	return tags
}

// sdkTagStringsFromResourceTags transforms a *svcapitypes.Tag array to a string array.
func sdkTagStringsFromResourceTags(rTags []*svcapitypes.Tag) []string {
	tags := make([]string, len(rTags))
	for i := range rTags {
		tags[i] = *rTags[i].Key
	}
	return tags
}
