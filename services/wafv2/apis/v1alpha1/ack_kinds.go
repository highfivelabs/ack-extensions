// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	ackwafv2 "github.com/aws-controllers-k8s/wafv2-controller/apis/v1alpha1"
)

// ACK's kinds (wafv2.services.k8s.aws) that our kinds reference. ACK's
// generator writes a reference to a kind of the same service as a type of
// this package, so this alias lets the generated references code read
// ACK's objects. It is not a kind of ours: it has no CRD here and is
// registered in the scheme from ACK's module (cmd/controller/handwritten.go).

// WebACL is named by WebACLAssociation.spec.webACLRef.
type WebACL = ackwafv2.WebACL
