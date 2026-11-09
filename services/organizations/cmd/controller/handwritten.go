// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

// What main.go (generated) cannot know: the ACK kinds our kinds reference.

import (
	ackorgapitypes "github.com/aws-controllers-k8s/organizations-controller/apis/v1alpha1"
)

func init() {
	// Our kinds' references name ACK's Account and OrganizationalUnit
	// (organizations.services.k8s.aws), read through the manager's client.
	_ = ackorgapitypes.AddToScheme(scheme)
}
