// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package account_alias

import (
	"context"
	"errors"
	"fmt"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/iam"
	smithy "github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

// The fault IAM returns when the alias does not exist.
const noSuchEntity = "NoSuchEntity"

func isAPIError(err error, codes ...string) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	for _, c := range codes {
		if apiErr.ErrorCode() == c {
			return true
		}
	}
	return false
}

func (rm *resourceManager) requiredFieldsMissingFromReadOneInput(r *resource) bool {
	return aws.ToString(r.ko.Spec.AccountAlias) == ""
}

// currentAlias is the account's alias, "" when it has none.
func (rm *resourceManager) currentAlias(ctx context.Context) (string, error) {
	resp, err := rm.sdkapi.ListAccountAliases(ctx, &svcsdk.ListAccountAliasesInput{})
	rm.metrics.RecordAPICall("READ_ONE", "ListAccountAliases", err)
	if err != nil {
		return "", err
	}
	if len(resp.AccountAliases) == 0 {
		return "", nil
	}
	return resp.AccountAliases[0], nil
}

// sdkFind reads the account's alias. Any other alias, or none, is not
// this object's: the create that follows sets it back (a new alias
// replaces the old).
func (rm *resourceManager) sdkFind(ctx context.Context, r *resource) (latest *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkFind")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, ackerr.NotFound
	}
	alias, err := rm.currentAlias(ctx)
	if err != nil {
		return nil, err
	}
	if alias != aws.ToString(r.ko.Spec.AccountAlias) {
		return nil, ackerr.NotFound
	}
	ko := r.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	rm.setARN(ko)
	return &resource{ko}, nil
}

// setARN names the alias. IAM has no ARN for one, so this is the IAM ARN
// form with the alias appended; it is an identifier for ACK's metadata,
// never sent to AWS.
func (rm *resourceManager) setARN(ko *svcapitypes.AccountAlias) {
	arn := ackv1alpha1.AWSResourceName(rm.ARNFromName("account-alias/" + aws.ToString(ko.Spec.AccountAlias)))
	ko.Status.ACKResourceMetadata.ARN = &arn
}

func (rm *resourceManager) sdkCreate(ctx context.Context, desired *resource) (created *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkCreate")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(desired) {
		return nil, ackerr.NewTerminalError(fmt.Errorf("spec.accountAlias is required"))
	}
	_, err = rm.sdkapi.CreateAccountAlias(ctx, &svcsdk.CreateAccountAliasInput{AccountAlias: desired.ko.Spec.AccountAlias})
	rm.metrics.RecordAPICall("CREATE", "CreateAccountAlias", err)
	if err != nil {
		return nil, err
	}
	ko := desired.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	rm.setARN(ko)
	return &resource{ko}, nil
}

// sdkUpdate has nothing to do: the alias is immutable (the CRD refuses a
// change), and another alias read is not found, not a difference.
func (rm *resourceManager) sdkUpdate(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta interface{},
) (updated *resource, err error) {
	return nil, ackerr.NewTerminalError(fmt.Errorf("an AccountAlias cannot be changed; delete it and create another"))
}

// sdkDelete deletes the alias, only while it is still this object's: an
// alias since replaced is someone else's.
func (rm *resourceManager) sdkDelete(ctx context.Context, r *resource) (deleted *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkDelete")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, nil
	}
	alias, err := rm.currentAlias(ctx)
	if err != nil {
		return nil, err
	}
	if alias != aws.ToString(r.ko.Spec.AccountAlias) {
		return nil, nil
	}
	_, err = rm.sdkapi.DeleteAccountAlias(ctx, &svcsdk.DeleteAccountAliasInput{AccountAlias: r.ko.Spec.AccountAlias})
	rm.metrics.RecordAPICall("DELETE", "DeleteAccountAlias", err)
	if err != nil && !isAPIError(err, noSuchEntity) {
		return nil, err
	}
	return nil, nil
}

func (rm *resourceManager) setStatusDefaults(ko *svcapitypes.AccountAlias) {
	if ko.Status.ACKResourceMetadata == nil {
		ko.Status.ACKResourceMetadata = &ackv1alpha1.ResourceMetadata{}
	}
	if ko.Status.ACKResourceMetadata.Region == nil {
		ko.Status.ACKResourceMetadata.Region = &rm.awsRegion
	}
	if ko.Status.ACKResourceMetadata.Partition == nil {
		ko.Status.ACKResourceMetadata.Partition = &rm.awsPartition
	}
	if ko.Status.ACKResourceMetadata.OwnerAccountID == nil {
		ko.Status.ACKResourceMetadata.OwnerAccountID = &rm.awsAccountID
	}
	if ko.Status.Conditions == nil {
		ko.Status.Conditions = []*ackv1alpha1.Condition{}
	}
}

// updateConditions sets the Terminal and Recoverable conditions as ACK's
// generated resources do (code-generator templates/pkg/resource/sdk.go.tpl)
// and leaves ACK.ResourceSynced to the runtime, which then asks IsSynced.
func (rm *resourceManager) updateConditions(r *resource, onSuccess bool, err error) (*resource, bool) {
	ko := r.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	var terminalCondition, recoverableCondition, syncCondition *ackv1alpha1.Condition
	for _, condition := range ko.Status.Conditions {
		switch condition.Type {
		case ackv1alpha1.ConditionTypeTerminal:
			terminalCondition = condition
		case ackv1alpha1.ConditionTypeRecoverable:
			recoverableCondition = condition
		case ackv1alpha1.ConditionTypeResourceSynced:
			syncCondition = condition
		}
	}
	var termError *ackerr.TerminalError
	if rm.terminalAWSError(err) || errors.As(err, &termError) {
		if terminalCondition == nil {
			terminalCondition = &ackv1alpha1.Condition{Type: ackv1alpha1.ConditionTypeTerminal}
			ko.Status.Conditions = append(ko.Status.Conditions, terminalCondition)
		}
		msg := err.Error()
		if awsErr, _ := ackerr.AWSError(err); awsErr != nil {
			msg = awsErr.Error()
		}
		terminalCondition.Status = corev1.ConditionTrue
		terminalCondition.Message = &msg
	} else {
		if terminalCondition != nil {
			terminalCondition.Status = corev1.ConditionFalse
			terminalCondition.Message = nil
		}
		if err != nil {
			if recoverableCondition == nil {
				recoverableCondition = &ackv1alpha1.Condition{Type: ackv1alpha1.ConditionTypeRecoverable}
				ko.Status.Conditions = append(ko.Status.Conditions, recoverableCondition)
			}
			msg := err.Error()
			if awsErr, _ := ackerr.AWSError(err); awsErr != nil {
				msg = awsErr.Error()
			}
			recoverableCondition.Status = corev1.ConditionTrue
			recoverableCondition.Message = &msg
		} else if recoverableCondition != nil {
			recoverableCondition.Status = corev1.ConditionFalse
			recoverableCondition.Message = nil
		}
	}
	if terminalCondition != nil || recoverableCondition != nil || syncCondition != nil {
		return &resource{ko}, true
	}
	return nil, false
}

// terminalAWSError: errors no retry fixes. An alias another AWS account
// already has (EntityAlreadyExists), or one IAM refuses, needs a person.
func (rm *resourceManager) terminalAWSError(err error) bool {
	return isAPIError(err, "EntityAlreadyExists", "ValidationError", "InvalidInput")
}
