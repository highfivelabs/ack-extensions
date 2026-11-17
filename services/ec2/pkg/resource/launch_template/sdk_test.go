// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package launch_template

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
)

func TestLaunchTemplateBatch2FieldsInPayloads(t *testing.T) {
	rm := &resourceManager{}
	r := &resource{
		ko: &svcapitypes.LaunchTemplate{
			Spec: svcapitypes.LaunchTemplateSpec{
				Name:           aws.String("lt-batch2"),
				DefaultVersion: aws.Int64(2),
				Data: &svcapitypes.RequestLaunchTemplateData{
					InstanceType: aws.String("c6i.large"),
					NetworkPerformanceOptions: &svcapitypes.LaunchTemplateNetworkPerformanceOptionsRequest{
						BandwidthWeighting: aws.String("vpc-1"),
					},
					NetworkInterfaces: []*svcapitypes.LaunchTemplateInstanceNetworkInterfaceSpecificationRequest{
						{
							DeviceIndex: aws.Int64(0),
							ConnectionTrackingSpecification: &svcapitypes.ConnectionTrackingSpecificationRequest{
								TCPEstablishedTimeout: aws.Int64(600),
								UDPStreamTimeout:      aws.Int64(120),
								UDPTimeout:            aws.Int64(30),
							},
							ENASrdSpecification: &svcapitypes.ENASrdSpecificationRequest{
								ENASrdEnabled: aws.Bool(true),
								ENASrdUDPSpecification: &svcapitypes.ENASrdUDPSpecificationRequest{
									ENASrdUDPEnabled: aws.Bool(true),
								},
							},
						},
					},
					BlockDeviceMappings: []*svcapitypes.LaunchTemplateBlockDeviceMappingRequest{
						{
							DeviceName: aws.String("/dev/xvda"),
							EBS: &svcapitypes.LaunchTemplateEBSBlockDeviceRequest{
								VolumeSize:               aws.Int64(20),
								VolumeType:               aws.String("gp3"),
								VolumeInitializationRate: aws.Int64(200),
							},
						},
					},
				},
			},
			Status: svcapitypes.LaunchTemplateStatus{
				ID: aws.String("lt-0123456789abcdef0"),
			},
		},
	}

	createIn, err := rm.newCreateRequestPayload(context.Background(), r)
	require.NoError(t, err)
	require.NotNil(t, createIn.LaunchTemplateData.NetworkPerformanceOptions)
	assert.Equal(t, "vpc-1", string(createIn.LaunchTemplateData.NetworkPerformanceOptions.BandwidthWeighting))
	require.Len(t, createIn.LaunchTemplateData.NetworkInterfaces, 1)
	ni := createIn.LaunchTemplateData.NetworkInterfaces[0]
	require.NotNil(t, ni.ConnectionTrackingSpecification)
	assert.Equal(t, int32(600), aws.ToInt32(ni.ConnectionTrackingSpecification.TcpEstablishedTimeout))
	require.NotNil(t, ni.EnaSrdSpecification)
	assert.Equal(t, aws.Bool(true), ni.EnaSrdSpecification.EnaSrdEnabled)
	require.Len(t, createIn.LaunchTemplateData.BlockDeviceMappings, 1)
	require.NotNil(t, createIn.LaunchTemplateData.BlockDeviceMappings[0].Ebs)
	assert.Equal(t, int32(200), aws.ToInt32(createIn.LaunchTemplateData.BlockDeviceMappings[0].Ebs.VolumeInitializationRate))

	updateIn, err := rm.newUpdateRequestPayload(context.Background(), r, nil)
	require.NoError(t, err)
	require.Len(t, updateIn.LaunchTemplateData.BlockDeviceMappings, 1)
	assert.Equal(t, int32(200), aws.ToInt32(updateIn.LaunchTemplateData.BlockDeviceMappings[0].Ebs.VolumeInitializationRate))

	modIn, err := newUpdateLaunchTemplateVersionRequestPayload(r)
	require.NoError(t, err)
	assert.Equal(t, aws.String("2"), modIn.DefaultVersion)
}
