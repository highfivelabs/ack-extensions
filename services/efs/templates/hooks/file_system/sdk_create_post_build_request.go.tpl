	// This is an idempotency token required in the API call...
	if input.CreationToken == nil || *input.CreationToken == "" {
		input.CreationToken = aws.String(getIdempotencyToken())
	}
