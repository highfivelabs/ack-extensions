	// Until this object has made its device, the read finds nothing and the
	// create makes one. A status that lost the serial number takes it back
	// from the Secret the object owns (hooks.go).
	if r.ko.Status.SerialNumber == nil {
		serial, err := rm.serialFromSecret(ctx, r.ko)
		if err != nil {
			return nil, err
		}
		if serial == "" {
			return nil, ackerr.NotFound
		}
		r.ko.Status.SerialNumber = &serial
	}
