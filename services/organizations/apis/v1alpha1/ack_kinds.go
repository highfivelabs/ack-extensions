// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	ackorg "github.com/aws-controllers-k8s/organizations-controller/apis/v1alpha1"
)

// OrganizationalUnit is ACK's kind (organizations.services.k8s.aws), which
// PolicyAttachment.spec.targetRef names. ACK's generator writes a reference
// to a kind of the same service as a type of this package, so the alias
// lets the generated references code read ACK's object. It is not a kind
// of ours: it has no CRD here and is registered in the scheme from ACK's
// module (cmd/controller/handwritten.go).
type OrganizationalUnit = ackorg.OrganizationalUnit
