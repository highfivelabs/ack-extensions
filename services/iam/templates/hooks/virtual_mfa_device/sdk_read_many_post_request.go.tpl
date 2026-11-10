	if err == nil {
		// The list is paged; read on until the device is found (hooks.go).
		resp, err = rm.listUntilFound(ctx, input, resp, r.ko.Status.SerialNumber)
	}
