// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package delegated_administrator

import (
	"context"
	"errors"

	svcsdk "github.com/aws/aws-sdk-go-v2/service/organizations"
	smithy "github.com/aws/smithy-go"
)

// accountRef names ACK's Account; the generated references code reads it,
// and the generator writes no RBAC marker for a kind of the same service
// that is not ours.
// +kubebuilder:rbac:groups=organizations.services.k8s.aws,resources=accounts,verbs=get;list
// +kubebuilder:rbac:groups=organizations.services.k8s.aws,resources=accounts/status,verbs=get;list

// readAllAdministrators reads the rest of ListDelegatedAdministrators'
// pages into resp, so the generated match on accountID sees every one.
func (rm *resourceManager) readAllAdministrators(
	ctx context.Context,
	input *svcsdk.ListDelegatedAdministratorsInput,
	resp *svcsdk.ListDelegatedAdministratorsOutput,
) error {
	for resp.NextToken != nil && *resp.NextToken != "" {
		next := *input
		next.NextToken = resp.NextToken
		page, err := rm.sdkapi.ListDelegatedAdministrators(ctx, &next)
		rm.metrics.RecordAPICall("READ_MANY", "ListDelegatedAdministrators", err)
		if err != nil {
			return err
		}
		resp.DelegatedAdministrators = append(resp.DelegatedAdministrators, page.DelegatedAdministrators...)
		resp.NextToken = page.NextToken
	}
	return nil
}

// alreadyDeregistered reports whether DeregisterDelegatedAdministrator
// failed because the account is no longer the service's delegated
// administrator, or no longer in the organization.
func alreadyDeregistered(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.ErrorCode() {
	case "AccountNotRegisteredException", "AccountNotFoundException":
		return true
	}
	return false
}
