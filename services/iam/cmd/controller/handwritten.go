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
	// UserGroupMembership is AddUserToGroup and RemoveUserFromGroup, which
	// the generator does not read as a create and delete either.
	_ "github.com/highfivelabs/ack-extensions/services/iam/pkg/resource/user_group_membership"
	// AccountAlias is CreateAccountAlias and ListAccountAliases, an
	// account-wide setting the generator does not read as a resource.
	_ "github.com/highfivelabs/ack-extensions/services/iam/pkg/resource/account_alias"
)

func init() {
	// roleRef and policyRef name ACK's own Role and Policy, and userRef and
	// groupRefs its User and Group (iam.services.k8s.aws), read through the
	// manager's client.
	_ = ackiamapitypes.AddToScheme(scheme)
}
