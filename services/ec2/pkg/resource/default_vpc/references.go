// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package default_vpc

import (
	"context"

	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// A DefaultVPC references nothing: the region names its VPC.

func (rm *resourceManager) ClearResolvedReferences(res acktypes.AWSResource) acktypes.AWSResource {
	return res
}

func (rm *resourceManager) ResolveReferences(
	ctx context.Context,
	apiReader client.Reader,
	res acktypes.AWSResource,
) (acktypes.AWSResource, bool, error) {
	return res, false, nil
}
