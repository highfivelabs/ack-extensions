	// A revoked permission stays listed for a while; it is gone.
	if ko.Status.PermissionState != nil && ko.Status.PermissionState.State != nil &&
		(*ko.Status.PermissionState.State == "revoking" || *ko.Status.PermissionState.State == "revoked") {
		return nil, ackerr.NotFound
	}
