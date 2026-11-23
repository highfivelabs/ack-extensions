// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

// What main.go (generated) cannot know: the extensions of ACK's own kinds.
// The CloudTrail manager has no kind of its own; it runs beside ACK's
// cloudtrail-controller to extend ACK's Trail (docs/extensions.md).

import (
	// Starts and stops a Trail's logging and puts its selectors from
	// cloudtrail.aws.hfl.dev annotations; its init registers it with
	// pkg/extend. ACK's Trail is read as unstructured (cloudtrail-controller
	// is not a module here), so nothing is added to the scheme.
	_ "github.com/highfivelabs/ack-extensions/services/cloudtrail/pkg/extend/trail"
)
