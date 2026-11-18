	// A file system with no policy answers PolicyNotFound: not made yet.
	if isPolicyNotFound(err) {
		return nil, ackerr.NotFound
	}
