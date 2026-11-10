	if err == nil {
		// The seed and QR code are returned only here: into the Secret, and
		// out of the response before anything else reads them (hooks.go).
		if err := rm.storeSeed(ctx, desired.ko, resp.VirtualMFADevice); err != nil {
			return nil, err
		}
	}
