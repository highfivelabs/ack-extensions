// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

// What main.go (generated) cannot know: the kinds written by hand, and the
// ACK kinds they reference.

import (
	ackkmsapitypes "github.com/aws-controllers-k8s/kms-controller/apis/v1alpha1"

	// ReplicaKey is created in one Region and lives in another, which the
	// generator's single regional client cannot model; registering its
	// package adds its manager factory.
	_ "github.com/highfivelabs/ack-extensions/services/kms/pkg/resource/replica_key"
)

func init() {
	// keyRef names ACK's own Key (kms.services.k8s.aws), read through the
	// manager's client.
	_ = ackkmsapitypes.AddToScheme(scheme)
}
