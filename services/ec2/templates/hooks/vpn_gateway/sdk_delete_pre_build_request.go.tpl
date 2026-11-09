	// EC2 refuses to delete an attached VPN gateway: detach it first.
	if vpc := rm.getAttachedVPC(r); vpc != nil && r.ko.Status.VPNGatewayID != nil {
		if err = rm.detachFromVPC(ctx, *vpc, *r.ko.Status.VPNGatewayID); err != nil {
			return nil, err
		}
	}
