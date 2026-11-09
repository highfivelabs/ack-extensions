// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

// What main.go (generated) cannot know: the kinds written by hand.

import (
	// ReplicationConfiguration is put with PutReplicationConfiguration and
	// read with DescribeRegistry, which the generator does not read as a
	// resource; registering its package adds its manager factory.
	_ "github.com/highfivelabs/ack-extensions/services/ecr/pkg/resource/replication_configuration"
)
