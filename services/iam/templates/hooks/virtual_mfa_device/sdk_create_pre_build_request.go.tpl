	// The Secret must not exist yet: a seed is never written over another.
	if err := rm.secretFree(ctx, desired.ko); err != nil {
		return nil, err
	}
