// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

// What main.go (generated) cannot know: the kinds written by hand, and the
// ACK kinds our references name.

import (
	ackec2apitypes "github.com/aws-controllers-k8s/ec2-controller/apis/v1alpha1"

	// VolumeAttachment is made with AttachVolume and read from
	// DescribeVolumes, which the generator does not read as a create and a
	// read of an attachment; registering its package adds its manager
	// factory.
	_ "github.com/highfivelabs/ack-extensions/services/ec2/pkg/resource/volume_attachment"

	// ResourceTag is one tag put with CreateTags, whose input is a list
	// of resources and a list of tags, not one tag; the generator would
	// make a kind of both lists.
	_ "github.com/highfivelabs/ack-extensions/services/ec2/pkg/resource/resource_tag"

	// DefaultSecurityGroup takes over a VPC's default security group,
	// found by the VPC and never made or deleted: no Create/Delete pair
	// the generator reads.
	_ "github.com/highfivelabs/ack-extensions/services/ec2/pkg/resource/default_security_group"

	// DefaultNetworkACL, DefaultRouteTable and DefaultVPC take over a VPC's
	// default network ACL and main route table, and the region's default
	// VPC, in the same way.
	_ "github.com/highfivelabs/ack-extensions/services/ec2/pkg/resource/default_network_acl"
	_ "github.com/highfivelabs/ack-extensions/services/ec2/pkg/resource/default_route_table"
	_ "github.com/highfivelabs/ack-extensions/services/ec2/pkg/resource/default_vpc"
)

func init() {
	// vpcRef, routeTableRef, transitGatewayRef and instanceRef name ACK's
	// own VPC, RouteTable, TransitGateway and Instance (ec2.services.k8s.aws),
	// read through the manager's client.
	_ = ackec2apitypes.AddToScheme(scheme)
}
