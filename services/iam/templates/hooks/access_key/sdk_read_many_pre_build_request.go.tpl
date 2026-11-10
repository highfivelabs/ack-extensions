	// Until this object has made its key, the read finds nothing and the
	// create makes one. A status that lost the key's ID takes it back from
	// the Secret the object owns (hooks.go).
	if r.ko.Status.AccessKeyID == nil {
		id, err := rm.keyIDFromSecret(ctx, r.ko)
		if err != nil {
			return nil, err
		}
		if id == "" {
			return nil, ackerr.NotFound
		}
		r.ko.Status.AccessKeyID = &id
	}
