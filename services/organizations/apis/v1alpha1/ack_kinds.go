// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	ackorg "github.com/aws-controllers-k8s/organizations-controller/apis/v1alpha1"
)

// ACK's kinds (organizations.services.k8s.aws) that our kinds reference.
// ACK's generator writes a reference to a kind of the same service as a
// type of this package, so these aliases let the generated references code
// read ACK's objects. They are not kinds of ours: they have no CRD here and
// are registered in the scheme from ACK's module
// (cmd/controller/handwritten.go).

// Account is named by DelegatedAdministrator.spec.accountRef.
type Account = ackorg.Account
