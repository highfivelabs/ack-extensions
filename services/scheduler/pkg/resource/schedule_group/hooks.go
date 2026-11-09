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
// aws-controllers-k8s/eventbridge-controller v1.9.2 (fd084a0b),
// pkg/resource/event_bus/hooks.go. Changes: package; the Scheduler client
// and its ResourceArn field names; the resource policy (PutPermission,
// RemovePermission) dropped, since a schedule group has none.

package schedule_group

import (
	"context"

	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/scheduler"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/scheduler/apis/v1alpha1"
)

// setResourceAdditionalFields will set the fields that are not returned by
// GetScheduleGroup calls
func (rm *resourceManager) setResourceAdditionalFields(
	ctx context.Context,
	ko *svcapitypes.ScheduleGroup,
) (err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.setResourceAdditionalFields")
	defer func() { exit(err) }()

	if ko.Status.ACKResourceMetadata != nil && ko.Status.ACKResourceMetadata.ARN != nil &&
		*ko.Status.ACKResourceMetadata.ARN != "" {
		// Set the group's tags
		ko.Spec.Tags, err = rm.getTags(ctx, string(*ko.Status.ACKResourceMetadata.ARN))
		if err != nil {
			return err
		}
	}
	return nil
}

// getTags retrieves a resource list of tags.
func (rm *resourceManager) getTags(
	ctx context.Context,
	resourceARN string,
) (tags []*svcapitypes.Tag, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.getTags")
	defer func() { exit(err) }()

	var listTagsResponse *svcsdk.ListTagsForResourceOutput
	listTagsResponse, err = rm.sdkapi.ListTagsForResource(
		ctx,
		&svcsdk.ListTagsForResourceInput{
			ResourceArn: &resourceARN,
		},
	)
	rm.metrics.RecordAPICall("GET", "ListTagsForResource", err)
	if err != nil {
		return nil, err
	}
	for _, tag := range listTagsResponse.Tags {
		tags = append(tags, &svcapitypes.Tag{
			Key:   tag.Key,
			Value: tag.Value,
		})
	}
	return tags, nil
}

// customUpdate implements a custom logic for handling ScheduleGroup
// resource updates: a group has only its tags to change.
func (rm *resourceManager) customUpdate(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta *ackcompare.Delta,
) (*resource, error) {
	var err error
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.customUpdate")
	defer func() { exit(err) }()

	updated := desired.DeepCopy()
	updated.SetStatus(latest)

	if delta.DifferentAt("Spec.Tags") {
		err = rm.syncTags(ctx, desired, latest)
		if err != nil {
			return nil, err
		}
	}
	return rm.concreteResource(updated), nil
}

// syncTags updates schedule group tags
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

	arn := (*string)(latest.ko.Status.ACKResourceMetadata.ARN)
	if len(toDelete) > 0 {
		_, err = rm.sdkapi.UntagResource(
			ctx,
			&svcsdk.UntagResourceInput{
				ResourceArn: arn,
				TagKeys:     sdkTagStringsFromResourceTags(toDelete),
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
				ResourceArn: arn,
				Tags:        sdkTagsFromResourceTags(toAdd),
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

// compareTags is a custom comparison function for comparing lists of Tag
// structs where the order of the structs in the list is not important.
func compareTags(
	delta *ackcompare.Delta,
	desired *resource,
	latest *resource,
) {
	desiredTags, _ := convertToOrderedACKTags(desired.ko.Spec.Tags)
	latestTags, _ := convertToOrderedACKTags(latest.ko.Spec.Tags)
	added, _, removed := ackcompare.GetTagsDifference(latestTags, desiredTags)
	if len(added) > 0 || len(removed) > 0 {
		delta.Add("Spec.Tags", desired.ko.Spec.Tags, latest.ko.Spec.Tags)
	}
}
