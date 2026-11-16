// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"github.com/highfivelabs/ack-extensions/services/iam/pkg/ekscluster"
)

// Cluster is ACK's eks Cluster (eks.services.k8s.aws), which
// OpenIDConnectProvider.spec.urlRef names: ACK's generator writes a
// reference to a kind of the same service as a type of this package, as for
// ack_kinds.go's aliases. It is a mirror of the fields the reference reads
// (pkg/ekscluster), not a kind of ours.
type Cluster = ekscluster.Cluster
