	if placed, err := rm.moveFrom(ctx, desired, input); err != nil || placed != nil {
		return placed, err
	}
