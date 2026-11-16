	// Real EC2's CreateVpnConnection answers without the connection's type
	// (moto's carries it), so the output above sets spec.type_ to nil, and
	// the spec patch after the create is refused (type_ is required; live
	// runs ackx-11160252-142c, ackx-11160402-fec2). It is immutable: keep
	// the type asked for.
	if ko.Spec.Type == nil {
		ko.Spec.Type = desired.ko.Spec.Type
	}
	// Static routes are made after the connection (CreateVpnConnectionRoute).
	if len(ko.Spec.StaticRoutes) > 0 {
		if err = rm.syncStaticRoutes(ctx, ko, ko.Spec.StaticRoutes, nil); err != nil {
			return nil, err
		}
	}
