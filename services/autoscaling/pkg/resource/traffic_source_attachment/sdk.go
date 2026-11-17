// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package traffic_source_attachment

import (
	"context"
	"errors"
	"strings"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	ackcondition "github.com/aws-controllers-k8s/runtime/pkg/condition"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/autoscaling"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	"github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/autoscaling/apis/v1alpha1"
)

func (rm *resourceManager) sdkFind(ctx context.Context, r *resource) (latest *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkFind")
	defer func() { exit(err) }()

	if r.ko.Spec.AutoScalingGroupName == nil || len(r.ko.Spec.TrafficSources) == 0 {
		return nil, ackerr.NotFound
	}

	desired := make(map[trafficSourceKey]struct{}, len(r.ko.Spec.TrafficSources))
	for _, ts := range r.ko.Spec.TrafficSources {
		if ts != nil {
			desired[keyForTrafficSource(ts)] = struct{}{}
		}
	}

	var matchedStates []*svcapitypes.TrafficSourceState
	var matchedIDs []*svcapitypes.TrafficSourceIdentifier
	var nextToken *string
	for {
		resp, err := rm.sdkapi.DescribeTrafficSources(ctx, &svcsdk.DescribeTrafficSourcesInput{
			AutoScalingGroupName: r.ko.Spec.AutoScalingGroupName,
			NextToken:            nextToken,
		})
		if rm.metrics != nil {
			rm.metrics.RecordAPICall("READ_MANY", "DescribeTrafficSources", err)
		}
		if err != nil {
			if isNotFoundError(err) {
				return nil, ackerr.NotFound
			}
			return nil, err
		}
		for _, ts := range resp.TrafficSources {
			key := trafficSourceKey{
				identifier: aws.ToString(ts.Identifier),
				sourceType: aws.ToString(ts.Type),
			}
			if _, ok := desired[key]; !ok {
				continue
			}
			matchedIDs = append(matchedIDs, &svcapitypes.TrafficSourceIdentifier{
				Identifier: ts.Identifier,
				Type:       ts.Type,
			})
			matchedStates = append(matchedStates, &svcapitypes.TrafficSourceState{
				Identifier:    ts.Identifier,
				State:         ts.State,
				TrafficSource: ts.TrafficSource,
				Type:          ts.Type,
			})
		}
		if resp.NextToken == nil || *resp.NextToken == "" {
			break
		}
		nextToken = resp.NextToken
	}

	if len(matchedStates) == 0 {
		return nil, ackerr.NotFound
	}

	ko := r.ko.DeepCopy()
	ko.Spec.TrafficSources = matchedIDs
	ko.Status.TrafficSources = matchedStates
	rm.setStatusDefaults(ko)
	return &resource{ko}, nil
}

func (rm *resourceManager) sdkCreate(ctx context.Context, desired *resource) (created *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkCreate")
	defer func() { exit(err) }()

	input := &svcsdk.AttachTrafficSourcesInput{
		AutoScalingGroupName:     desired.ko.Spec.AutoScalingGroupName,
		TrafficSources:           toSDKTrafficSources(desired.ko.Spec.TrafficSources),
		SkipZonalShiftValidation: desired.ko.Spec.SkipZonalShiftValidation,
	}
	_, err = rm.sdkapi.AttachTrafficSources(ctx, input)
	if rm.metrics != nil {
		rm.metrics.RecordAPICall("CREATE", "AttachTrafficSources", err)
	}
	if err != nil {
		return nil, err
	}

	ko := desired.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	return &resource{ko}, nil
}

func (rm *resourceManager) sdkUpdate(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta *ackcompare.Delta,
) (updated *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkUpdate")
	defer func() { exit(err) }()

	if !delta.DifferentAt("Spec.TrafficSources") {
		return desired, nil
	}

	desiredMap := trafficSourceMap(desired.ko.Spec.TrafficSources)
	latestMap := trafficSourceMap(latest.ko.Spec.TrafficSources)

	var toDetach []*svcapitypes.TrafficSourceIdentifier
	for k, v := range latestMap {
		if _, ok := desiredMap[k]; !ok {
			toDetach = append(toDetach, v)
		}
	}
	var toAttach []*svcapitypes.TrafficSourceIdentifier
	for k, v := range desiredMap {
		if _, ok := latestMap[k]; !ok {
			toAttach = append(toAttach, v)
		}
	}

	if len(toDetach) > 0 {
		_, err = rm.sdkapi.DetachTrafficSources(ctx, &svcsdk.DetachTrafficSourcesInput{
			AutoScalingGroupName: desired.ko.Spec.AutoScalingGroupName,
			TrafficSources:       toSDKTrafficSources(toDetach),
		})
		if rm.metrics != nil {
			rm.metrics.RecordAPICall("UPDATE", "DetachTrafficSources", err)
		}
		if err != nil {
			return nil, err
		}
	}
	if len(toAttach) > 0 {
		_, err = rm.sdkapi.AttachTrafficSources(ctx, &svcsdk.AttachTrafficSourcesInput{
			AutoScalingGroupName:     desired.ko.Spec.AutoScalingGroupName,
			TrafficSources:           toSDKTrafficSources(toAttach),
			SkipZonalShiftValidation: desired.ko.Spec.SkipZonalShiftValidation,
		})
		if rm.metrics != nil {
			rm.metrics.RecordAPICall("UPDATE", "AttachTrafficSources", err)
		}
		if err != nil {
			return nil, err
		}
	}

	ko := desired.ko.DeepCopy()
	ko.Status = latest.ko.Status
	rm.setStatusDefaults(ko)
	return &resource{ko}, nil
}

func (rm *resourceManager) sdkDelete(ctx context.Context, r *resource) (latest *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkDelete")
	defer func() { exit(err) }()

	if r.ko.Spec.AutoScalingGroupName == nil || len(r.ko.Spec.TrafficSources) == 0 {
		return nil, nil
	}

	_, err = rm.sdkapi.DetachTrafficSources(ctx, &svcsdk.DetachTrafficSourcesInput{
		AutoScalingGroupName: r.ko.Spec.AutoScalingGroupName,
		TrafficSources:       toSDKTrafficSources(r.ko.Spec.TrafficSources),
	})
	if rm.metrics != nil {
		rm.metrics.RecordAPICall("DELETE", "DetachTrafficSources", err)
	}
	if err != nil && !isNotFoundError(err) {
		return r, err
	}
	return nil, nil
}

func (rm *resourceManager) setStatusDefaults(ko *svcapitypes.TrafficSourceAttachment) {
	if ko.Status.ACKResourceMetadata == nil {
		ko.Status.ACKResourceMetadata = &ackv1alpha1.ResourceMetadata{}
	}
	if ko.Status.ACKResourceMetadata.OwnerAccountID == nil {
		ko.Status.ACKResourceMetadata.OwnerAccountID = &rm.awsAccountID
	}
	if ko.Status.ACKResourceMetadata.Region == nil {
		ko.Status.ACKResourceMetadata.Region = &rm.awsRegion
	}
}

func (rm *resourceManager) updateConditions(r *resource, synced bool, err error) (*resource, bool) {
	ko := r.ko.DeepCopy()
	res := &resource{ko}
	if err != nil {
		var terminalErr ackerr.TerminalError
		if errors.As(err, &terminalErr) {
			msg := err.Error()
			ackcondition.SetTerminal(res, corev1.ConditionTrue, &msg, nil)
		}
		msg := err.Error()
		ackcondition.SetSynced(res, corev1.ConditionFalse, &msg, nil)
		return res, true
	}
	if synced {
		ackcondition.SetSynced(res, corev1.ConditionTrue, nil, nil)
	}
	return res, true
}

func toSDKTrafficSources(items []*svcapitypes.TrafficSourceIdentifier) []svcsdktypes.TrafficSourceIdentifier {
	out := make([]svcsdktypes.TrafficSourceIdentifier, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		out = append(out, svcsdktypes.TrafficSourceIdentifier{
			Identifier: item.Identifier,
			Type:       item.Type,
		})
	}
	return out
}

func trafficSourceMap(items []*svcapitypes.TrafficSourceIdentifier) map[trafficSourceKey]*svcapitypes.TrafficSourceIdentifier {
	m := make(map[trafficSourceKey]*svcapitypes.TrafficSourceIdentifier, len(items))
	for _, item := range items {
		if item != nil {
			m[keyForTrafficSource(item)] = item
		}
	}
	return m
}

func isNotFoundError(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		if code == "ResourceNotFoundFault" || code == "ValidationError" && strings.Contains(apiErr.ErrorMessage(), "not found") {
			return true
		}
	}
	return false
}
