	// A deleted VPN gateway stays listed for a while, in state deleted; it
	// is gone.
	if ko.Status.State != nil && (*ko.Status.State == "deleted" || *ko.Status.State == "deleting") {
		return nil, ackerr.NotFound
	}
	ko.Spec.VPC = rm.getAttachedVPC(&resource{ko})
