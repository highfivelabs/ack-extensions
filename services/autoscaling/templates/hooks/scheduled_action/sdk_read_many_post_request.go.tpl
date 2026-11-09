	// A one-time action EC2 Auto Scaling ran and deleted is completed, not
	// gone (hooks.go).
	if err == nil {
		if done := rm.completedOneTimeAction(r, resp); done != nil {
			rm.metrics.RecordAPICall("READ_MANY", "DescribeScheduledActions", nil)
			return done, nil
		}
	}
