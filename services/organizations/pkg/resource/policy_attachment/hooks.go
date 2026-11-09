// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package policy_attachment

import (
	"context"
	"errors"

	svcsdk "github.com/aws/aws-sdk-go-v2/service/organizations"
	smithy "github.com/aws/smithy-go"
)

// targetRef names ACK's OrganizationalUnit; the generated references code
// reads it, and the generator writes no RBAC marker for a kind of the same
// service that is not ours.
// +kubebuilder:rbac:groups=organizations.services.k8s.aws,resources=organizationalunits,verbs=get;list
// +kubebuilder:rbac:groups=organizations.services.k8s.aws,resources=organizationalunits/status,verbs=get;list

// readAllTargets reads the rest of ListTargetsForPolicy's pages into resp,
// so the generated match on targetID sees every target: Organizations
// returns at most 20 a page.
func (rm *resourceManager) readAllTargets(
	ctx context.Context,
	input *svcsdk.ListTargetsForPolicyInput,
	resp *svcsdk.ListTargetsForPolicyOutput,
) error {
	for resp.NextToken != nil && *resp.NextToken != "" {
		next := *input
		next.NextToken = resp.NextToken
		page, err := rm.sdkapi.ListTargetsForPolicy(ctx, &next)
		rm.metrics.RecordAPICall("READ_MANY", "ListTargetsForPolicy", err)
		if err != nil {
			return err
		}
		resp.Targets = append(resp.Targets, page.Targets...)
		resp.NextToken = page.NextToken
	}
	return nil
}

// alreadyDetached reports whether DetachPolicy failed because there is
// nothing left to detach: the policy is not attached, or the policy or the
// target is gone.
func alreadyDetached(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.ErrorCode() {
	case "PolicyNotAttachedException", "PolicyNotFoundException", "TargetNotFoundException",
		"AccountNotFoundException", "OrganizationalUnitNotFoundException":
		return true
	}
	return false
}
