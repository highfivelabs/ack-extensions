	// The route table is found; the propagation is there only if the
	// table lists the gateway among its propagating gateways.
	if err == nil {
		resp.RouteTables = propagating(resp.RouteTables, r.ko.Spec.GatewayID)
	}
