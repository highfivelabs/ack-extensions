	rm.setIdentifiers(ko, resp.Policy)
	if err := rm.setResourceAdditionalFields(ctx, ko); err != nil {
		return nil, err
	}
