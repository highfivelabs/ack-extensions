// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

// What main.go (generated) cannot know: the kinds written by hand, and the
// ACK kinds they reference.

import (
	ackwafv2apitypes "github.com/aws-controllers-k8s/wafv2-controller/apis/v1alpha1"

	// WebACLAssociation is made with AssociateWebACL and read with
	// GetWebACLForResource, which the generator does not read as a
	// resource; registering its package adds its manager factory.
	_ "github.com/highfivelabs/ack-extensions/services/wafv2/pkg/resource/web_acl_association"
)

func init() {
	// webACLRef names ACK's wafv2.services.k8s.aws WebACL, read through the
	// manager's client.
	_ = ackwafv2apitypes.AddToScheme(scheme)
}
