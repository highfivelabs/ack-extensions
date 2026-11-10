	// The Secret must not exist yet: a key is never written over another.
	if err := rm.secretFree(ctx, desired.ko); err != nil {
		return nil, err
	}
