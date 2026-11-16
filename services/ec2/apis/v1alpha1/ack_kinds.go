// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	ackec2 "github.com/aws-controllers-k8s/ec2-controller/apis/v1alpha1"
)

// ACK's kinds (ec2.services.k8s.aws) that our kinds reference (open
// question 16, answered 2026-11-13: ACK's ec2-controller as a Go module,
// at the version UPSTREAM.md pins). ACK's generator writes a reference to
// a kind of the same service as a type of this package, so these aliases
// let the generated references code read ACK's objects. They are not kinds
// of ours: they have no CRD here and are registered in the scheme from
// ACK's module (cmd/controller/handwritten.go). The SDK shapes of the same
// names are ignored in generator.yaml.

// Instance is named by VolumeAttachment.spec.instanceRef and
// Volume.spec.availabilityZoneRef.
type Instance = ackec2.Instance

// RouteTable is named by VPNGatewayRoutePropagation.spec.routeTableRef.
type RouteTable = ackec2.RouteTable

// SecurityGroup is named by LaunchTemplate.spec.data.securityGroupRefs.
type SecurityGroup = ackec2.SecurityGroup

// Subnet is named by LaunchTemplate.spec.data.networkInterfaces[].subnetRef.
type Subnet = ackec2.Subnet

// TransitGateway is named by VPNConnection.spec.transitGatewayRef.
type TransitGateway = ackec2.TransitGateway

// VPC is named by VPNGateway.spec.vpcRef.
type VPC = ackec2.VPC
