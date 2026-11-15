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
// aws-controllers-k8s/ec2-controller v1.23.1, pkg/resource/route_table/sdk.go
// (compareCreateRouteInput, newCreateRouteInput, newDeleteRouteInput, the
// reading of a route, the vpce- correction) and hooks.go (getRoutesDifference).
// Changes: package; the route types are ACK's own (ackec2); a route read
// as a function (routeFromSDK); only routes made with CreateRoute are read
// (the local route and propagated routes are EC2's), and VPC gateway
// endpoint routes (vpce-, to a prefix list) are left out, as Terraform's
// aws_default_route_table ignores them.

package default_route_table

import (
	"strings"

	ackec2 "github.com/aws-controllers-k8s/ec2-controller/apis/v1alpha1"
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// routeFromSDK reads a route as ACK's RouteTable does, or nil for a route
// that is EC2's or an endpoint's.
func routeFromSDK(f4iter svcsdktypes.Route) *ackec2.CreateRouteInput {
	if f4iter.Origin != svcsdktypes.RouteOriginCreateRoute {
		return nil
	}
	if f4iter.GatewayId != nil && strings.HasPrefix(*f4iter.GatewayId, "vpce-") && f4iter.DestinationPrefixListId != nil {
		return nil
	}
	f4elem := &ackec2.CreateRouteInput{}
	if f4iter.CarrierGatewayId != nil {
		f4elem.CarrierGatewayID = f4iter.CarrierGatewayId
	}
	if f4iter.CoreNetworkArn != nil {
		f4elem.CoreNetworkARN = f4iter.CoreNetworkArn
	}
	if f4iter.DestinationCidrBlock != nil {
		f4elem.DestinationCIDRBlock = f4iter.DestinationCidrBlock
	}
	if f4iter.DestinationIpv6CidrBlock != nil {
		f4elem.DestinationIPv6CIDRBlock = f4iter.DestinationIpv6CidrBlock
	}
	if f4iter.DestinationPrefixListId != nil {
		f4elem.DestinationPrefixListID = f4iter.DestinationPrefixListId
	}
	if f4iter.EgressOnlyInternetGatewayId != nil {
		f4elem.EgressOnlyInternetGatewayID = f4iter.EgressOnlyInternetGatewayId
	}
	if f4iter.GatewayId != nil {
		f4elem.GatewayID = f4iter.GatewayId
	}
	if f4iter.InstanceId != nil {
		f4elem.InstanceID = f4iter.InstanceId
	}
	if f4iter.LocalGatewayId != nil {
		f4elem.LocalGatewayID = f4iter.LocalGatewayId
	}
	if f4iter.NatGatewayId != nil {
		f4elem.NATGatewayID = f4iter.NatGatewayId
	}
	if f4iter.NetworkInterfaceId != nil {
		f4elem.NetworkInterfaceID = f4iter.NetworkInterfaceId
	}
	if f4iter.TransitGatewayId != nil {
		f4elem.TransitGatewayID = f4iter.TransitGatewayId
	}
	if f4iter.VpcPeeringConnectionId != nil {
		f4elem.VPCPeeringConnectionID = f4iter.VpcPeeringConnectionId
	}
	// Even if route is created with arguments as VPCEndpointID,
	// when aws api is called to describe the route (inside skdFind), it
	// returns VPCEndpointID as GatewayID.
	if f4elem.GatewayID != nil && strings.HasPrefix(*f4elem.GatewayID, "vpce-") {
		f4elem.VPCEndpointID = f4elem.GatewayID
		f4elem.GatewayID = nil
	}
	return f4elem
}

func getRoutesDifference(desired, latest []*ackec2.CreateRouteInput) (toAdd, toDelete []*ackec2.CreateRouteInput) {
	toDelete = make([]*ackec2.CreateRouteInput, len(latest))
	copy(toDelete, latest)

	remove := func(s []*ackec2.CreateRouteInput, i int) []*ackec2.CreateRouteInput {
		if i < len(s)-1 { // if not last element just copy the last element to where the removed element was
			s[i] = s[len(s)-1]
		}
		return s[:len(s)-1]
	}

	// Routes that are desired, but already exist in latest, can be ignored. The
	// toDelete slice is a copy of latest and will be slowly modified so that at
	// the end it only contains routes that exist in latest, but are not
	// desired.
	for _, routeA := range desired {
		found := false
		for idx, routeB := range toDelete {
			if delta := compareCreateRouteInput(routeA, routeB); len(delta.Differences) == 0 {
				toDelete = remove(toDelete, idx)
				found = true
				break
			}
		}
		if !found {
			toAdd = append(toAdd, routeA.DeepCopy())
		}
	}

	return toAdd, toDelete
}

func compareCreateRouteInput(
	a *ackec2.CreateRouteInput,
	b *ackec2.CreateRouteInput,
) *ackcompare.Delta {
	delta := ackcompare.NewDelta()
	if ackcompare.HasNilDifference(a.CarrierGatewayID, b.CarrierGatewayID) {
		delta.Add("CreateRouteInput.CarrierGatewayID", a.CarrierGatewayID, b.CarrierGatewayID)
	} else if a.CarrierGatewayID != nil && b.CarrierGatewayID != nil {
		if *a.CarrierGatewayID != *b.CarrierGatewayID {
			delta.Add("CreateRouteInput.CarrierGatewayID", a.CarrierGatewayID, b.CarrierGatewayID)
		}
	}
	if ackcompare.HasNilDifference(a.CoreNetworkARN, b.CoreNetworkARN) {
		delta.Add("CreateRouteInput.CoreNetworkARN", a.CoreNetworkARN, b.CoreNetworkARN)
	} else if a.CoreNetworkARN != nil && b.CoreNetworkARN != nil {
		if *a.CoreNetworkARN != *b.CoreNetworkARN {
			delta.Add("CreateRouteInput.CoreNetworkARN", a.CoreNetworkARN, b.CoreNetworkARN)
		}
	}
	if ackcompare.HasNilDifference(a.DestinationCIDRBlock, b.DestinationCIDRBlock) {
		delta.Add("CreateRouteInput.DestinationCIDRBlock", a.DestinationCIDRBlock, b.DestinationCIDRBlock)
	} else if a.DestinationCIDRBlock != nil && b.DestinationCIDRBlock != nil {
		if *a.DestinationCIDRBlock != *b.DestinationCIDRBlock {
			delta.Add("CreateRouteInput.DestinationCIDRBlock", a.DestinationCIDRBlock, b.DestinationCIDRBlock)
		}
	}
	if ackcompare.HasNilDifference(a.DestinationIPv6CIDRBlock, b.DestinationIPv6CIDRBlock) {
		delta.Add("CreateRouteInput.DestinationIPv6CIDRBlock", a.DestinationIPv6CIDRBlock, b.DestinationIPv6CIDRBlock)
	} else if a.DestinationIPv6CIDRBlock != nil && b.DestinationIPv6CIDRBlock != nil {
		if *a.DestinationIPv6CIDRBlock != *b.DestinationIPv6CIDRBlock {
			delta.Add("CreateRouteInput.DestinationIPv6CIDRBlock", a.DestinationIPv6CIDRBlock, b.DestinationIPv6CIDRBlock)
		}
	}
	if ackcompare.HasNilDifference(a.DestinationPrefixListID, b.DestinationPrefixListID) {
		delta.Add("CreateRouteInput.DestinationPrefixListID", a.DestinationPrefixListID, b.DestinationPrefixListID)
	} else if a.DestinationPrefixListID != nil && b.DestinationPrefixListID != nil {
		if *a.DestinationPrefixListID != *b.DestinationPrefixListID {
			delta.Add("CreateRouteInput.DestinationPrefixListID", a.DestinationPrefixListID, b.DestinationPrefixListID)
		}
	}
	if ackcompare.HasNilDifference(a.EgressOnlyInternetGatewayID, b.EgressOnlyInternetGatewayID) {
		delta.Add("CreateRouteInput.EgressOnlyInternetGatewayID", a.EgressOnlyInternetGatewayID, b.EgressOnlyInternetGatewayID)
	} else if a.EgressOnlyInternetGatewayID != nil && b.EgressOnlyInternetGatewayID != nil {
		if *a.EgressOnlyInternetGatewayID != *b.EgressOnlyInternetGatewayID {
			delta.Add("CreateRouteInput.EgressOnlyInternetGatewayID", a.EgressOnlyInternetGatewayID, b.EgressOnlyInternetGatewayID)
		}
	}
	if ackcompare.HasNilDifference(a.GatewayID, b.GatewayID) {
		delta.Add("CreateRouteInput.GatewayID", a.GatewayID, b.GatewayID)
	} else if a.GatewayID != nil && b.GatewayID != nil {
		if *a.GatewayID != *b.GatewayID {
			delta.Add("CreateRouteInput.GatewayID", a.GatewayID, b.GatewayID)
		}
	}
	if ackcompare.HasNilDifference(a.InstanceID, b.InstanceID) {
		delta.Add("CreateRouteInput.InstanceID", a.InstanceID, b.InstanceID)
	} else if a.InstanceID != nil && b.InstanceID != nil {
		if *a.InstanceID != *b.InstanceID {
			delta.Add("CreateRouteInput.InstanceID", a.InstanceID, b.InstanceID)
		}
	}
	if ackcompare.HasNilDifference(a.LocalGatewayID, b.LocalGatewayID) {
		delta.Add("CreateRouteInput.LocalGatewayID", a.LocalGatewayID, b.LocalGatewayID)
	} else if a.LocalGatewayID != nil && b.LocalGatewayID != nil {
		if *a.LocalGatewayID != *b.LocalGatewayID {
			delta.Add("CreateRouteInput.LocalGatewayID", a.LocalGatewayID, b.LocalGatewayID)
		}
	}
	if ackcompare.HasNilDifference(a.NATGatewayID, b.NATGatewayID) {
		delta.Add("CreateRouteInput.NATGatewayID", a.NATGatewayID, b.NATGatewayID)
	} else if a.NATGatewayID != nil && b.NATGatewayID != nil {
		if *a.NATGatewayID != *b.NATGatewayID {
			delta.Add("CreateRouteInput.NATGatewayID", a.NATGatewayID, b.NATGatewayID)
		}
	}
	if ackcompare.HasNilDifference(a.NetworkInterfaceID, b.NetworkInterfaceID) {
		delta.Add("CreateRouteInput.NetworkInterfaceID", a.NetworkInterfaceID, b.NetworkInterfaceID)
	} else if a.NetworkInterfaceID != nil && b.NetworkInterfaceID != nil {
		if *a.NetworkInterfaceID != *b.NetworkInterfaceID {
			delta.Add("CreateRouteInput.NetworkInterfaceID", a.NetworkInterfaceID, b.NetworkInterfaceID)
		}
	}
	if ackcompare.HasNilDifference(a.TransitGatewayID, b.TransitGatewayID) {
		delta.Add("CreateRouteInput.TransitGatewayID", a.TransitGatewayID, b.TransitGatewayID)
	} else if a.TransitGatewayID != nil && b.TransitGatewayID != nil {
		if *a.TransitGatewayID != *b.TransitGatewayID {
			delta.Add("CreateRouteInput.TransitGatewayID", a.TransitGatewayID, b.TransitGatewayID)
		}
	}
	if ackcompare.HasNilDifference(a.VPCEndpointID, b.VPCEndpointID) {
		delta.Add("CreateRouteInput.VPCEndpointID", a.VPCEndpointID, b.VPCEndpointID)
	} else if a.VPCEndpointID != nil && b.VPCEndpointID != nil {
		if *a.VPCEndpointID != *b.VPCEndpointID {
			delta.Add("CreateRouteInput.VPCEndpointID", a.VPCEndpointID, b.VPCEndpointID)
		}
	}
	if ackcompare.HasNilDifference(a.VPCPeeringConnectionID, b.VPCPeeringConnectionID) {
		delta.Add("CreateRouteInput.VPCPeeringConnectionID", a.VPCPeeringConnectionID, b.VPCPeeringConnectionID)
	} else if a.VPCPeeringConnectionID != nil && b.VPCPeeringConnectionID != nil {
		if *a.VPCPeeringConnectionID != *b.VPCPeeringConnectionID {
			delta.Add("CreateRouteInput.VPCPeeringConnectionID", a.VPCPeeringConnectionID, b.VPCPeeringConnectionID)
		}
	}

	return delta
}

func newCreateRouteInput(
	c ackec2.CreateRouteInput,
) *svcsdk.CreateRouteInput {
	res := &svcsdk.CreateRouteInput{}

	if c.CarrierGatewayID != nil {
		res.CarrierGatewayId = c.CarrierGatewayID
	}
	if c.CoreNetworkARN != nil {
		res.CoreNetworkArn = c.CoreNetworkARN
	}
	if c.DestinationCIDRBlock != nil {
		res.DestinationCidrBlock = c.DestinationCIDRBlock
	}
	if c.DestinationIPv6CIDRBlock != nil {
		res.DestinationIpv6CidrBlock = c.DestinationIPv6CIDRBlock
	}
	if c.DestinationPrefixListID != nil {
		res.DestinationPrefixListId = c.DestinationPrefixListID
	}
	if c.EgressOnlyInternetGatewayID != nil {
		res.EgressOnlyInternetGatewayId = c.EgressOnlyInternetGatewayID
	}
	if c.GatewayID != nil {
		res.GatewayId = c.GatewayID
	}
	if c.InstanceID != nil {
		res.InstanceId = c.InstanceID
	}
	if c.LocalGatewayID != nil {
		res.LocalGatewayId = c.LocalGatewayID
	}
	if c.NATGatewayID != nil {
		res.NatGatewayId = c.NATGatewayID
	}
	if c.NetworkInterfaceID != nil {
		res.NetworkInterfaceId = c.NetworkInterfaceID
	}
	if c.TransitGatewayID != nil {
		res.TransitGatewayId = c.TransitGatewayID
	}
	if c.VPCEndpointID != nil {
		res.VpcEndpointId = c.VPCEndpointID
	}
	if c.VPCPeeringConnectionID != nil {
		res.VpcPeeringConnectionId = c.VPCPeeringConnectionID
	}

	return res
}

func newDeleteRouteInput(
	c ackec2.CreateRouteInput,
) *svcsdk.DeleteRouteInput {
	res := &svcsdk.DeleteRouteInput{}

	if c.DestinationCIDRBlock != nil {
		res.DestinationCidrBlock = c.DestinationCIDRBlock
	}
	if c.DestinationIPv6CIDRBlock != nil {
		res.DestinationIpv6CidrBlock = c.DestinationIPv6CIDRBlock
	}
	if c.DestinationPrefixListID != nil {
		res.DestinationPrefixListId = c.DestinationPrefixListID
	}

	return res
}
