	// DescribeLifecycleHooks reads a group's hooks; ask for this one by name.
	if r.ko.Spec.LifecycleHookName != nil {
		input.LifecycleHookNames = []string{*r.ko.Spec.LifecycleHookName}
	}
