// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

// What main.go (generated) cannot know: the kinds written by hand.

import (
	// VolumeAttachment is made with AttachVolume and read from
	// DescribeVolumes, which the generator does not read as a create and a
	// read of an attachment; registering its package adds its manager
	// factory.
	_ "github.com/highfivelabs/ack-extensions/services/ec2/pkg/resource/volume_attachment"
)
