// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package auto_scaling_group

import (
	"context"
	"testing"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/autoscaling/apis/v1alpha1"
)

func TestPreserveLaunchTemplateReferenceFields(t *testing.T) {
	ref1 := &ackv1alpha1.AWSResourceReferenceWrapper{
		From: &ackv1alpha1.AWSResourceReference{Name: aws.String("lt-1")},
	}
	ref2 := &ackv1alpha1.AWSResourceReferenceWrapper{
		From: &ackv1alpha1.AWSResourceReference{Name: aws.String("lt-2")},
	}
	ref3 := &ackv1alpha1.AWSResourceReferenceWrapper{
		From: &ackv1alpha1.AWSResourceReference{Name: aws.String("lt-3")},
	}

	desired := &svcapitypes.AutoScalingGroup{
		Spec: svcapitypes.AutoScalingGroupSpec{
			LaunchTemplate: &svcapitypes.LaunchTemplateSpecification{
				LaunchTemplateRef:  ref1,
				LaunchTemplateName: aws.String("lt-1-name"),
			},
			MixedInstancesPolicy: &svcapitypes.MixedInstancesPolicy{
				LaunchTemplate: &svcapitypes.LaunchTemplate{
					LaunchTemplateSpecification: &svcapitypes.LaunchTemplateSpecification{
						LaunchTemplateRef:  ref2,
						LaunchTemplateName: aws.String("lt-2-name"),
					},
					Overrides: []*svcapitypes.LaunchTemplateOverrides{
						{
							LaunchTemplateSpecification: &svcapitypes.LaunchTemplateSpecification{
								LaunchTemplateRef:  ref3,
								LaunchTemplateName: aws.String("lt-3-name"),
							},
						},
					},
				},
			},
		},
	}

	observed := &svcapitypes.AutoScalingGroup{
		Spec: svcapitypes.AutoScalingGroupSpec{
			LaunchTemplate: &svcapitypes.LaunchTemplateSpecification{
				LaunchTemplateName: aws.String("lt-1-name"),
			},
			MixedInstancesPolicy: &svcapitypes.MixedInstancesPolicy{
				LaunchTemplate: &svcapitypes.LaunchTemplate{
					LaunchTemplateSpecification: &svcapitypes.LaunchTemplateSpecification{
						LaunchTemplateName: aws.String("lt-2-name"),
					},
					Overrides: []*svcapitypes.LaunchTemplateOverrides{
						{
							LaunchTemplateSpecification: &svcapitypes.LaunchTemplateSpecification{
								LaunchTemplateName: aws.String("lt-3-name"),
							},
						},
					},
				},
			},
		},
	}

	preserveLaunchTemplateReferenceFields(desired, observed)

	assert.Equal(t, ref1, observed.Spec.LaunchTemplate.LaunchTemplateRef)
	assert.Equal(t, ref2, observed.Spec.MixedInstancesPolicy.LaunchTemplate.LaunchTemplateSpecification.LaunchTemplateRef)
	assert.Equal(t, ref3, observed.Spec.MixedInstancesPolicy.LaunchTemplate.Overrides[0].LaunchTemplateSpecification.LaunchTemplateRef)
}

func TestNormalizeAutoScalingGroupForCompare(t *testing.T) {
	ref := &ackv1alpha1.AWSResourceReferenceWrapper{
		From: &ackv1alpha1.AWSResourceReference{Name: aws.String("lt-override")},
	}

	a := &svcapitypes.AutoScalingGroup{
		Spec: svcapitypes.AutoScalingGroupSpec{
			MixedInstancesPolicy: &svcapitypes.MixedInstancesPolicy{
				LaunchTemplate: &svcapitypes.LaunchTemplate{
					Overrides: []*svcapitypes.LaunchTemplateOverrides{
						{
							InstanceType: aws.String("t3.micro"),
							LaunchTemplateSpecification: &svcapitypes.LaunchTemplateSpecification{
								LaunchTemplateRef:  ref,
								LaunchTemplateName: aws.String("lt-override-name"),
							},
						},
					},
				},
			},
			Tags: []*svcapitypes.Tag{
				{Key: aws.String("omitted"), Value: aws.String("v1")},
				{Key: aws.String("explicit"), Value: aws.String("v2"), PropagateAtLaunch: aws.Bool(true)},
			},
		},
	}
	b := &svcapitypes.AutoScalingGroup{
		Spec: svcapitypes.AutoScalingGroupSpec{
			MixedInstancesPolicy: &svcapitypes.MixedInstancesPolicy{
				LaunchTemplate: &svcapitypes.LaunchTemplate{
					Overrides: []*svcapitypes.LaunchTemplateOverrides{
						{
							InstanceType: aws.String("t3.micro"),
							LaunchTemplateSpecification: &svcapitypes.LaunchTemplateSpecification{
								LaunchTemplateName: aws.String("lt-override-name"),
							},
						},
					},
				},
			},
			Tags: []*svcapitypes.Tag{
				{Key: aws.String("omitted"), Value: aws.String("v1"), PropagateAtLaunch: aws.Bool(false)},
				{Key: aws.String("explicit"), Value: aws.String("v2"), PropagateAtLaunch: aws.Bool(false)},
			},
		},
	}

	delta := newResourceDelta(&resource{ko: a}, &resource{ko: b})

	assert.False(t, delta.DifferentAt("Spec.MixedInstancesPolicy.LaunchTemplate.Overrides"), "overrides should not diff on LaunchTemplateRef")
	require.Nil(t, b.Spec.Tags[0].PropagateAtLaunch, "omitted PropagateAtLaunch should normalize against false")
	require.NotNil(t, b.Spec.Tags[1].PropagateAtLaunch, "explicit true vs false should still diff")
	assert.True(t, delta.DifferentAt("Spec.Tags"), "explicit PropagateAtLaunch true vs false should diff")
}

func TestContextInPayloads(t *testing.T) {
	rm := &resourceManager{}
	r := &resource{
		ko: &svcapitypes.AutoScalingGroup{
			Spec: svcapitypes.AutoScalingGroupSpec{
				Name:    aws.String("asg-ctx"),
				MinSize: aws.Int64(0),
				MaxSize: aws.Int64(1),
				Context: aws.String("ackx-context"),
			},
		},
	}

	createIn, err := rm.newCreateRequestPayload(context.Background(), r)
	require.NoError(t, err)
	assert.Equal(t, aws.String("ackx-context"), createIn.Context)

	updateIn, err := rm.newUpdateRequestPayload(context.Background(), r, nil)
	require.NoError(t, err)
	assert.Equal(t, aws.String("ackx-context"), updateIn.Context)
}
