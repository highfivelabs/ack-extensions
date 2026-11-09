	// DescribeScheduledActions reads a group's actions; ask for this one by
	// name.
	if r.ko.Spec.ScheduledActionName != nil {
		input.ScheduledActionNames = []string{*r.ko.Spec.ScheduledActionName}
	}
