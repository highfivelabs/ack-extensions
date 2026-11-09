	if delta.DifferentAt("Spec.Tags") {
		if err = rm.syncTags(ctx, desired, latest); err != nil {
			return nil, err
		}
	}
	if !delta.DifferentExcept("Spec.Tags") {
		// Only the tags changed: UpdatePolicy has nothing to do.
		updated = rm.concreteResource(desired.DeepCopy())
		updated.SetStatus(latest)
		return updated, nil
	}
