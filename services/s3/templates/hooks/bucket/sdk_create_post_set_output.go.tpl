	ackcondition.SetSynced(&resource{ko}, corev1.ConditionFalse, aws.String("bucket created, requeue for updates"), nil)
	// ack-extensions: s3-controller v1.15.0 returns here unconditionally,
	// which leaves the generated "return ..., nil" after this hook
	// unreachable (go vet's unreachable check, which task check runs).
	// NeededAfter always returns an error, so the behaviour is the same.
	if err = ackrequeue.NeededAfter(fmt.Errorf("Reconciling to sync additional fields"), time.Second); err != nil {
		return &resource{ko}, err
	}
