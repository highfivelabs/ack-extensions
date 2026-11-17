// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package lifecycle_hook

import (
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
)

// customSetDefaults treats AWS's server defaults for DefaultResult (ABANDON)
// and HeartbeatTimeout (3600) as no drift when the desired spec leaves them
// unset.
func customSetDefaults(a, b *resource) {
	if ackcompare.IsNil(a.ko.Spec.DefaultResult) && ackcompare.IsNotNil(b.ko.Spec.DefaultResult) {
		a.ko.Spec.DefaultResult = b.ko.Spec.DefaultResult
	}
	if ackcompare.IsNil(a.ko.Spec.HeartbeatTimeout) && ackcompare.IsNotNil(b.ko.Spec.HeartbeatTimeout) {
		a.ko.Spec.HeartbeatTimeout = b.ko.Spec.HeartbeatTimeout
	}
}
