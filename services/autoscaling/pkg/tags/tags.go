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
// aws-controllers-k8s/autoscaling-controller v1.5.0 (56316c75),
// pkg/tags/tags.go (UPSTREAM.md). Changes: the import path of the
// service's own API types (autoscaling.aws.hfl.dev), and per-tag
// PropagateAtLaunch instead of PropagateAtLaunchDefault.

package tags

import (
	"context"

	"github.com/aws-controllers-k8s/runtime/pkg/metrics"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	acktags "github.com/aws-controllers-k8s/runtime/pkg/tags"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/autoscaling"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/autoscaling/apis/v1alpha1"
)

// The type of resource. The only supported value is auto-scaling-group.
const ResourceType = "auto-scaling-group"

// The default value for the PropagateAtLaunch parameter when omitted on a tag.
const PropagateAtLaunchDefault = false

type tagState struct {
	value             string
	propagateAtLaunch bool
}

func tagStateMap(tags []*svcapitypes.Tag) map[string]tagState {
	out := make(map[string]tagState, len(tags))
	for _, t := range tags {
		if t == nil || t.Key == nil {
			continue
		}
		var val string
		if t.Value != nil {
			val = *t.Value
		}
		propagate := PropagateAtLaunchDefault
		if t.PropagateAtLaunch != nil {
			propagate = *t.PropagateAtLaunch
		}
		out[*t.Key] = tagState{
			value:             val,
			propagateAtLaunch: propagate,
		}
	}
	return out
}

// Tags examines the Tags in the supplied Resource and calls the
// CreateOrUpdateTags and DeleteTags APIs to ensure that the set of
// associated Tags stays in sync with the Resource.Spec.Tags.
func Tags(
	ctx context.Context,
	desiredTags []*svcapitypes.Tag,
	latestTags []*svcapitypes.Tag,
	resourceID *string,
	_ func([]*svcapitypes.Tag) (acktags.Tags, []string),
	sdkapi *svcsdk.Client,
	metrics *metrics.Metrics,
) (err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("syncTags")
	defer func() { exit(err) }()

	from := tagStateMap(latestTags)
	to := tagStateMap(desiredTags)

	var toAdd []svcsdktypes.Tag
	for key, desired := range to {
		latest, exists := from[key]
		if !exists || latest != desired {
			toAdd = append(toAdd, svcsdktypes.Tag{
				Key:               aws.String(key),
				Value:             aws.String(desired.value),
				ResourceId:        resourceID,
				ResourceType:      aws.String(ResourceType),
				PropagateAtLaunch: aws.Bool(desired.propagateAtLaunch),
			})
		}
	}

	var toRemove []svcsdktypes.Tag
	for key := range from {
		if _, exists := to[key]; !exists {
			toRemove = append(toRemove, svcsdktypes.Tag{
				Key:          aws.String(key),
				ResourceId:   resourceID,
				ResourceType: aws.String(ResourceType),
			})
		}
	}

	if len(toAdd) > 0 {
		rlog.Debug("adding tags to auto-scaling group", "count", len(toAdd))
		_, err = sdkapi.CreateOrUpdateTags(ctx, &svcsdk.CreateOrUpdateTagsInput{
			Tags: toAdd,
		})
		metrics.RecordAPICall("UPDATE", "CreateOrUpdateTags", err)
		if err != nil {
			return err
		}
	}

	if len(toRemove) > 0 {
		rlog.Debug("removing tags from auto-scaling group", "count", len(toRemove))
		_, err = sdkapi.DeleteTags(ctx, &svcsdk.DeleteTagsInput{
			Tags: toRemove,
		})
		metrics.RecordAPICall("UPDATE", "DeleteTags", err)
		if err != nil {
			return err
		}
	}

	return nil
}
