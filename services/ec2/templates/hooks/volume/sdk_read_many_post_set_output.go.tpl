	// A deleted volume can stay listed for a while, in state deleted or
	// deleting; it is gone.
	if ko.Status.State != nil && (*ko.Status.State == "deleted" || *ko.Status.State == "deleting") {
		return nil, ackerr.NotFound
	}
