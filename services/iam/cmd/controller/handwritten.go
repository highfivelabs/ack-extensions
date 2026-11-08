// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

// What main.go (generated) cannot know: the kinds written by hand, and the
// ACK kinds they reference.

import (
	ackiamapitypes "github.com/aws-controllers-k8s/iam-controller/apis/v1alpha1"

	// RolePolicyAttachment has no Create/Delete pair in IAM's model, so it
	// is not generated; registering its package adds its manager factory.
	_ "github.com/highfivelabs/ack-extensions/services/iam/pkg/resource/role_policy_attachment"
)

func init() {
	// roleRef and policyRef name ACK's own Role and Policy
	// (iam.services.k8s.aws), read through the manager's client.
	_ = ackiamapitypes.AddToScheme(scheme)
}
