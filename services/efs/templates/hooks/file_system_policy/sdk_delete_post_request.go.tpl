	// Already gone: the policy, or the file system with it.
	if isPolicyNotFound(err) {
		return nil, nil
	}
