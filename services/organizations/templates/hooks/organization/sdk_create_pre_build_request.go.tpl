	if adopted, err := rm.adoptExisting(ctx, desired); err != nil || adopted != nil {
		return adopted, err
	}
