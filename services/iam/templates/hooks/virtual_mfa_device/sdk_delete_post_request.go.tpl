	if err == nil {
		// The Secret goes with the device (its owner reference does the same
		// where garbage collection runs).
		err = rm.deleteSecret(ctx, r.ko)
	}
