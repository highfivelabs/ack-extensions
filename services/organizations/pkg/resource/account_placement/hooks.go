// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package account_placement

import (
	"context"
	"fmt"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/organizations"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/organizations/apis/v1alpha1"
)

// accountRef names ACK's Account and parentRef ACK's OrganizationalUnit;
// the generated references code reads them, and the generator writes no
// RBAC marker for a kind of the same service that is not ours.
// +kubebuilder:rbac:groups=organizations.services.k8s.aws,resources=accounts,verbs=get;list
// +kubebuilder:rbac:groups=organizations.services.k8s.aws,resources=accounts/status,verbs=get;list
// +kubebuilder:rbac:groups=organizations.services.k8s.aws,resources=organizationalunits,verbs=get;list
// +kubebuilder:rbac:groups=organizations.services.k8s.aws,resources=organizationalunits/status,verbs=get;list

// currentParent is the ID of the root or OU the account is in.
func (rm *resourceManager) currentParent(ctx context.Context, accountID *string) (string, error) {
	resp, err := rm.sdkapi.ListParents(ctx, &svcsdk.ListParentsInput{ChildId: accountID})
	rm.metrics.RecordAPICall("READ_MANY", "ListParents", err)
	if err != nil {
		return "", err
	}
	if len(resp.Parents) == 0 || resp.Parents[0].Id == nil {
		return "", fmt.Errorf("account %s has no parent", deref(accountID))
	}
	return *resp.Parents[0].Id, nil
}

// moveFrom completes MoveAccount's input with the parent the account is in
// now. If that is already the parent wanted, nothing is moved (MoveAccount
// would refuse it, DuplicateAccountException) and the placed resource is
// returned for the create to return.
func (rm *resourceManager) moveFrom(
	ctx context.Context,
	desired *resource,
	input *svcsdk.MoveAccountInput,
) (*resource, error) {
	from, err := rm.currentParent(ctx, input.AccountId)
	if err != nil {
		return nil, err
	}
	if input.DestinationParentId != nil && from == *input.DestinationParentId {
		ko := desired.ko.DeepCopy()
		rm.setStatusDefaults(ko)
		if err := rm.setAccountARN(ctx, ko); err != nil {
			return nil, err
		}
		return &resource{ko}, nil
	}
	input.SourceParentId = &from
	return nil, nil
}

// setAccountARN puts the account's ARN in the status. MoveAccount returns
// nothing; the ARN is what marks the account placed by this object, so
// the read finds it from then on.
func (rm *resourceManager) setAccountARN(ctx context.Context, ko *svcapitypes.AccountPlacement) error {
	resp, err := rm.sdkapi.DescribeAccount(ctx, &svcsdk.DescribeAccountInput{AccountId: ko.Spec.AccountID})
	rm.metrics.RecordAPICall("READ_ONE", "DescribeAccount", err)
	if err != nil {
		return err
	}
	if resp.Account == nil || resp.Account.Arn == nil {
		return fmt.Errorf("DescribeAccount returned no ARN for account %s", deref(ko.Spec.AccountID))
	}
	if ko.Status.ACKResourceMetadata == nil {
		ko.Status.ACKResourceMetadata = &ackv1alpha1.ResourceMetadata{}
	}
	arn := ackv1alpha1.AWSResourceName(*resp.Account.Arn)
	ko.Status.ACKResourceMetadata.ARN = &arn
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
