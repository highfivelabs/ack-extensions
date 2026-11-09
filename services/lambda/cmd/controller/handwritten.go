// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

// What main.go (generated) cannot know: the kinds written by hand, and the
// ACK kinds they reference.

import (
	acklambdaapitypes "github.com/aws-controllers-k8s/lambda-controller/apis/v1alpha1"

	// Permission is added with AddPermission and read with GetPolicy, which
	// the generator does not read as a resource; registering its package
	// adds its manager factory.
	_ "github.com/highfivelabs/ack-extensions/services/lambda/pkg/resource/permission"
)

func init() {
	// functionRef names ACK's own Function (lambda.services.k8s.aws), read
	// through the manager's client.
	_ = acklambdaapitypes.AddToScheme(scheme)
}
