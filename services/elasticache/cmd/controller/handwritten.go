// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"github.com/highfivelabs/ack-extensions/services/elasticache/pkg/ackmirrors"
)

func init() {
	_ = ackmirrors.AddToScheme(scheme)
}
