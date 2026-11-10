	// Static routes are made after the connection (CreateVpnConnectionRoute).
	if len(ko.Spec.StaticRoutes) > 0 {
		if err = rm.syncStaticRoutes(ctx, ko, ko.Spec.StaticRoutes, nil); err != nil {
			return nil, err
		}
	}
