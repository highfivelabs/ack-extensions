// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package traffic_source_attachment

import (
	"context"
	"testing"

	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/autoscaling"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/autoscaling/apis/v1alpha1"
)

type fakeTrafficSourceClient struct {
	attached   map[trafficSourceKey]svcsdktypes.TrafficSourceState
	attachIn   []*svcsdk.AttachTrafficSourcesInput
	detachIn   []*svcsdk.DetachTrafficSourcesInput
	describeFn func(*svcsdk.DescribeTrafficSourcesInput) (*svcsdk.DescribeTrafficSourcesOutput, error)
}

func (f *fakeTrafficSourceClient) AttachTrafficSources(_ context.Context, in *svcsdk.AttachTrafficSourcesInput, _ ...func(*svcsdk.Options)) (*svcsdk.AttachTrafficSourcesOutput, error) {
	f.attachIn = append(f.attachIn, in)
	if f.attached == nil {
		f.attached = map[trafficSourceKey]svcsdktypes.TrafficSourceState{}
	}
	for _, ts := range in.TrafficSources {
		k := trafficSourceKey{identifier: aws.ToString(ts.Identifier), sourceType: aws.ToString(ts.Type)}
		f.attached[k] = svcsdktypes.TrafficSourceState{
			Identifier:    ts.Identifier,
			Type:          ts.Type,
			TrafficSource: ts.Identifier,
			State:         aws.String("Added"),
		}
	}
	return &svcsdk.AttachTrafficSourcesOutput{}, nil
}

func (f *fakeTrafficSourceClient) DescribeTrafficSources(_ context.Context, in *svcsdk.DescribeTrafficSourcesInput, _ ...func(*svcsdk.Options)) (*svcsdk.DescribeTrafficSourcesOutput, error) {
	if f.describeFn != nil {
		return f.describeFn(in)
	}
	var out []svcsdktypes.TrafficSourceState
	for _, v := range f.attached {
		out = append(out, v)
	}
	return &svcsdk.DescribeTrafficSourcesOutput{TrafficSources: out}, nil
}

func (f *fakeTrafficSourceClient) DetachTrafficSources(_ context.Context, in *svcsdk.DetachTrafficSourcesInput, _ ...func(*svcsdk.Options)) (*svcsdk.DetachTrafficSourcesOutput, error) {
	f.detachIn = append(f.detachIn, in)
	for _, ts := range in.TrafficSources {
		k := trafficSourceKey{identifier: aws.ToString(ts.Identifier), sourceType: aws.ToString(ts.Type)}
		delete(f.attached, k)
	}
	return &svcsdk.DetachTrafficSourcesOutput{}, nil
}

func TestTrafficSourceAttachmentCRUD(t *testing.T) {
	ctx := context.Background()
	fake := &fakeTrafficSourceClient{}
	rm := &resourceManager{
		awsAccountID: "111122223333",
		awsRegion:    "us-east-1",
		sdkapi:       fake,
	}

	tg1 := &svcapitypes.TrafficSourceIdentifier{
		Identifier: aws.String("arn:aws:elasticloadbalancing:us-east-1:111122223333:targetgroup/tg-1/123"),
		Type:       aws.String("elbv2"),
	}
	tg2 := &svcapitypes.TrafficSourceIdentifier{
		Identifier: aws.String("arn:aws:elasticloadbalancing:us-east-1:111122223333:targetgroup/tg-2/456"),
		Type:       aws.String("elbv2"),
	}
	other := svcsdktypes.TrafficSourceState{
		Identifier: aws.String("arn:aws:elasticloadbalancing:us-east-1:111122223333:targetgroup/unrelated/999"),
		Type:       aws.String("elbv2"),
		State:      aws.String("Added"),
	}

	r := &resource{
		ko: &svcapitypes.TrafficSourceAttachment{
			Spec: svcapitypes.TrafficSourceAttachmentSpec{
				AutoScalingGroupName:     aws.String("asg-1"),
				SkipZonalShiftValidation: aws.Bool(true),
				TrafficSources:           []*svcapitypes.TrafficSourceIdentifier{tg1},
			},
		},
	}

	// Not found before create.
	_, err := rm.sdkFind(ctx, r)
	require.ErrorIs(t, err, ackerr.NotFound)

	// Create attaches tg1.
	created, err := rm.sdkCreate(ctx, r)
	require.NoError(t, err)
	require.Len(t, fake.attachIn, 1)
	assert.Equal(t, aws.Bool(true), fake.attachIn[0].SkipZonalShiftValidation)
	assert.Equal(t, "asg-1", aws.ToString(created.ko.Spec.AutoScalingGroupName))

	// Find ignores unrelated traffic sources on the ASG.
	fake.attached[trafficSourceKey{identifier: aws.ToString(other.Identifier), sourceType: "elbv2"}] = other
	found, err := rm.sdkFind(ctx, r)
	require.NoError(t, err)
	require.Len(t, found.ko.Status.TrafficSources, 1)
	assert.Equal(t, aws.ToString(tg1.Identifier), aws.ToString(found.ko.Status.TrafficSources[0].Identifier))

	synced, err := rm.IsSynced(ctx, found)
	require.NoError(t, err)
	assert.True(t, synced)

	// Update swaps tg1 -> tg2.
	desired := &resource{
		ko: &svcapitypes.TrafficSourceAttachment{
			Spec: svcapitypes.TrafficSourceAttachmentSpec{
				AutoScalingGroupName: aws.String("asg-1"),
				TrafficSources:       []*svcapitypes.TrafficSourceIdentifier{tg2},
			},
		},
	}
	delta := newResourceDelta(desired, found)
	require.True(t, delta.DifferentAt("Spec.TrafficSources"))
	_, err = rm.sdkUpdate(ctx, desired, found, delta)
	require.NoError(t, err)
	require.Len(t, fake.detachIn, 1)
	assert.Equal(t, aws.ToString(tg1.Identifier), aws.ToString(fake.detachIn[0].TrafficSources[0].Identifier))
	require.Len(t, fake.attachIn, 2)
	assert.Equal(t, aws.ToString(tg2.Identifier), aws.ToString(fake.attachIn[1].TrafficSources[0].Identifier))

	// Delete detaches tg2.
	_, err = rm.sdkDelete(ctx, desired)
	require.NoError(t, err)
	require.Len(t, fake.detachIn, 2)
	_, ok := fake.attached[trafficSourceKey{identifier: aws.ToString(tg2.Identifier), sourceType: "elbv2"}]
	assert.False(t, ok)
}

func TestTrafficSourceFindNotFoundError(t *testing.T) {
	fake := &fakeTrafficSourceClient{
		describeFn: func(*svcsdk.DescribeTrafficSourcesInput) (*svcsdk.DescribeTrafficSourcesOutput, error) {
			return nil, &smithy.GenericAPIError{Code: "ValidationError", Message: "AutoScalingGroup name not found"}
		},
	}
	rm := &resourceManager{sdkapi: fake}
	r := &resource{
		ko: &svcapitypes.TrafficSourceAttachment{
			Spec: svcapitypes.TrafficSourceAttachmentSpec{
				AutoScalingGroupName: aws.String("missing"),
				TrafficSources: []*svcapitypes.TrafficSourceIdentifier{
					{Identifier: aws.String("tg"), Type: aws.String("elbv2")},
				},
			},
		},
	}
	_, err := rm.sdkFind(context.Background(), r)
	require.ErrorIs(t, err, ackerr.NotFound)
}
