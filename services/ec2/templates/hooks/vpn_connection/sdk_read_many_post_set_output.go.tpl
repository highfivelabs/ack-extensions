	// A deleted connection stays listed for a while, in state deleted; it
	// is gone.
	if ko.Status.State != nil && (*ko.Status.State == "deleted" || *ko.Status.State == "deleting") {
		return nil, ackerr.NotFound
	}
	ko.Spec.StaticRoutes = staticRoutesOf(ko)
	if ko.Spec.Options, err = readOptions(ko.Spec.Options, awsOptions); err != nil {
		return nil, err
	}
