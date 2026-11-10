// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	ackiam "github.com/aws-controllers-k8s/iam-controller/apis/v1alpha1"
)

// ACK's kinds (iam.services.k8s.aws) that our generated kinds reference.
// ACK's generator writes a reference to a kind of the same service as a
// type of this package, so this alias lets the generated references code
// read ACK's objects. It is not a kind of ours: it has no CRD here and is
// registered in the scheme from ACK's module (cmd/controller/handwritten.go).

// User is named by AccessKey.spec.userRef.
type User = ackiam.User
