	// A completed one-time action is already gone from EC2 Auto Scaling.
	if completed(r) {
		return nil, nil
	}
