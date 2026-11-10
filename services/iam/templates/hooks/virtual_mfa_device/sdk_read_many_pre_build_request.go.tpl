	// Until this object has made its device, the read finds nothing and the
	// create makes one. A status that lost the serial number takes it back
	// from the Secret the object owns (hooks.go), on a copy: the desired
	// object is the base of the runtime's status patch, and a serial number
	// set on it would never be written back.
	if r.ko.Status.SerialNumber == nil {
		serial, err := rm.serialFromSecret(ctx, r.ko)
		if err != nil {
			return nil, err
		}
		if serial == "" {
			return nil, ackerr.NotFound
		}
		r = &resource{ko: r.ko.DeepCopy()}
		r.ko.Status.SerialNumber = &serial
	}
