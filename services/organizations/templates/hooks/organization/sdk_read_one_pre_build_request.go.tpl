	// Until this object has made or adopted the organization (no ARN in
	// its status), the read finds nothing and the create adopts it.
	if r.ko.Status.ACKResourceMetadata == nil || r.ko.Status.ACKResourceMetadata.ARN == nil {
		return nil, ackerr.NotFound
	}
