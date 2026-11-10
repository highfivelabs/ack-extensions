	if err == nil {
		// The secret access key is returned only here: into the Secret, and
		// out of the response before anything else reads it (hooks.go).
		if err := rm.storeKey(ctx, desired.ko, resp.AccessKey); err != nil {
			return nil, err
		}
	}
