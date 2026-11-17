	if !delta.DifferentAt("Spec.LogDeliveryConfigurations") {
		input.LogDeliveryConfigurations = nil
	}
	if !delta.DifferentAt("Spec.TransitEncryptionEnabled") {
		input.TransitEncryptionEnabled = nil
	}
	if !delta.DifferentAt("Spec.TransitEncryptionMode") {
		input.TransitEncryptionMode = ""
	}
	if !delta.DifferentAt("Spec.ClusterMode") {
		input.ClusterMode = ""
	}
	rm.applyAuthTokenUpdatePayload(input, desired, latest, delta)
	rm.updateUserGroupIDsPayload(input, desired, latest, delta)
