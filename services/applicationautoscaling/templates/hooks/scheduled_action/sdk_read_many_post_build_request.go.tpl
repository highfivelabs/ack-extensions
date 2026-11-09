	// DescribeScheduledActions reads a namespace's actions; ask for this
	// one by name, resource and dimension.
	if r.ko.Spec.ScheduledActionName != nil {
		input.ScheduledActionNames = []string{*r.ko.Spec.ScheduledActionName}
	}
