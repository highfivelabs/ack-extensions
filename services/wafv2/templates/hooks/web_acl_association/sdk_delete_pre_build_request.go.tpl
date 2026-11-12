	if leave, err := rm.associatedElsewhere(ctx, r); err != nil || leave {
		return nil, err
	}
