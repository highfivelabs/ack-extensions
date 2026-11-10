	// The options as EC2 has them, the pre-shared keys and the device
	// configuration cleared from the response first (hooks.go).
	var awsOptions *svcsdktypes.VpnConnectionOptions
	if err == nil {
		awsOptions = awsOptionsOf(resp, r.ko.Status.VPNConnectionID)
	}
