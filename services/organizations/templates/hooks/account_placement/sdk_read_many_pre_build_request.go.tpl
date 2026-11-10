	// Until this object has placed the account (no ARN in its status), the
	// read finds nothing and the create places it.
	if r.ko.Status.ACKResourceMetadata == nil || r.ko.Status.ACKResourceMetadata.ARN == nil {
		return nil, ackerr.NotFound
	}
