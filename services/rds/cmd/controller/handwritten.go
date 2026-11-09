// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

// What main.go (generated) cannot know: the kinds written by hand, and the
// ACK kinds they reference.

import (
	ackiamapitypes "github.com/aws-controllers-k8s/iam-controller/apis/v1alpha1"
	ackrdsapitypes "github.com/aws-controllers-k8s/rds-controller/apis/v1alpha1"

	// DBProxyTarget is registered with RegisterDBProxyTargets, which the
	// generator does not read as a create; registering its package adds
	// its manager factory.
	_ "github.com/highfivelabs/ack-extensions/services/rds/pkg/resource/db_proxy_target"
	// DBRoleAssociation is made with AddRoleToDBCluster and
	// AddRoleToDBInstance, which the generator does not read as a create
	// either.
	_ "github.com/highfivelabs/ack-extensions/services/rds/pkg/resource/db_role_association"
)

func init() {
	// dbProxyRef, dbInstanceRef and dbClusterRef name ACK's own kinds
	// (rds.services.k8s.aws), read through the manager's client.
	_ = ackrdsapitypes.AddToScheme(scheme)
	// DBRoleAssociation's roleRef names ACK's iam.services.k8s.aws Role.
	_ = ackiamapitypes.AddToScheme(scheme)
}
