// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

// What main.go (generated) cannot know: the extensions of ACK's own kinds.
// The S3 manager has no kind of its own; it runs beside ACK's
// s3-controller to extend ACK's Bucket (docs/extensions.md).

import (
	// Empties a Bucket annotated s3.aws.hfl.dev/force-destroy on delete;
	// its init registers it with pkg/extend. ACK's Bucket is read as
	// unstructured, so nothing is added to the scheme.
	_ "github.com/highfivelabs/ack-extensions/services/s3/pkg/extend/bucket"
)
