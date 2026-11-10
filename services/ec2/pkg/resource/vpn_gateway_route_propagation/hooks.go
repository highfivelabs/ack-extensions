// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package vpn_gateway_route_propagation

import (
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// propagating keeps the route tables that list the gateway among their
// propagating gateways: the generated read then finds the propagation only
// when EC2 has it.
func propagating(tables []svcsdktypes.RouteTable, gateway *string) []svcsdktypes.RouteTable {
	if gateway == nil {
		return nil
	}
	var out []svcsdktypes.RouteTable
	for _, t := range tables {
		for _, g := range t.PropagatingVgws {
			if g.GatewayId != nil && *g.GatewayId == *gateway {
				out = append(out, t)
				break
			}
		}
	}
	return out
}
