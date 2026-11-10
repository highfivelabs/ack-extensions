	// The Secret's name, written into the spec once the key is made, so that
	// the CRD's immutability holds for it from then on (hooks.go).
	ko.Spec.SecretName = aws.String(secretName(ko))
