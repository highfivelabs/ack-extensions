// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package account_password_policy

import (
	"context"
	"errors"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/iam"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	smithy "github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

// The fault IAM returns when the account has no password policy.
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

func i64(v *int32) *int64 {
	if v == nil {
		return nil
	}
	n := int64(*v)
	return &n
}

func i32(v *int64) *int32 {
	if v == nil {
		return nil
	}
	n := int32(*v)
	return &n
}

// specFromPolicy is the spec as IAM reports it. IAM reports every field,
// its default where none was set.
func specFromPolicy(p *svcsdktypes.PasswordPolicy) svcapitypes.AccountPasswordPolicySpec {
	return svcapitypes.AccountPasswordPolicySpec{
		AllowUsersToChangePassword: aws.Bool(p.AllowUsersToChangePassword),
		HardExpiry:                 p.HardExpiry,
		MaxPasswordAge:             i64(p.MaxPasswordAge),
		MinimumPasswordLength:      i64(p.MinimumPasswordLength),
		PasswordReusePrevention:    i64(p.PasswordReusePrevention),
		RequireLowercaseCharacters: aws.Bool(p.RequireLowercaseCharacters),
		RequireNumbers:             aws.Bool(p.RequireNumbers),
		RequireSymbols:             aws.Bool(p.RequireSymbols),
		RequireUppercaseCharacters: aws.Bool(p.RequireUppercaseCharacters),
	}
}

func inputFromSpec(s svcapitypes.AccountPasswordPolicySpec) *svcsdk.UpdateAccountPasswordPolicyInput {
	return &svcsdk.UpdateAccountPasswordPolicyInput{
		AllowUsersToChangePassword: aws.ToBool(s.AllowUsersToChangePassword),
		HardExpiry:                 s.HardExpiry,
		MaxPasswordAge:             i32(s.MaxPasswordAge),
		MinimumPasswordLength:      i32(s.MinimumPasswordLength),
		PasswordReusePrevention:    i32(s.PasswordReusePrevention),
		RequireLowercaseCharacters: aws.ToBool(s.RequireLowercaseCharacters),
		RequireNumbers:             aws.ToBool(s.RequireNumbers),
		RequireSymbols:             aws.ToBool(s.RequireSymbols),
		RequireUppercaseCharacters: aws.ToBool(s.RequireUppercaseCharacters),
	}
}

// sdkFind reads the account's password policy. An account with none has
// no policy to find.
func (rm *resourceManager) sdkFind(ctx context.Context, r *resource) (latest *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkFind")
	defer func() { exit(err) }()
	resp, err := rm.sdkapi.GetAccountPasswordPolicy(ctx, &svcsdk.GetAccountPasswordPolicyInput{})
	rm.metrics.RecordAPICall("READ_ONE", "GetAccountPasswordPolicy", err)
	if err != nil {
		if isAPIError(err, noSuchEntity) {
			return nil, ackerr.NotFound
		}
		return nil, err
	}
	if resp.PasswordPolicy == nil {
		return nil, ackerr.NotFound
	}
	ko := r.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	ko.Spec = specFromPolicy(resp.PasswordPolicy)
	ko.Status.ExpirePasswords = aws.Bool(resp.PasswordPolicy.ExpirePasswords)
	rm.setARN(ko)
	return &resource{ko}, nil
}

// setARN names the policy. IAM has no ARN for it, so this is the IAM ARN
// form with a fixed name; it is an identifier for ACK's metadata, never
// sent to AWS.
func (rm *resourceManager) setARN(ko *svcapitypes.AccountPasswordPolicy) {
	arn := ackv1alpha1.AWSResourceName(rm.ARNFromName("account-password-policy"))
	ko.Status.ACKResourceMetadata.ARN = &arn
}

func (rm *resourceManager) put(ctx context.Context, ko *svcapitypes.AccountPasswordPolicy, op string) error {
	_, err := rm.sdkapi.UpdateAccountPasswordPolicy(ctx, inputFromSpec(ko.Spec))
	rm.metrics.RecordAPICall(op, "UpdateAccountPasswordPolicy", err)
	return err
}

func (rm *resourceManager) sdkCreate(ctx context.Context, desired *resource) (created *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkCreate")
	defer func() { exit(err) }()
	if err := rm.put(ctx, desired.ko, "CREATE"); err != nil {
		return nil, err
	}
	ko := desired.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	rm.setARN(ko)
	return &resource{ko}, nil
}

// sdkUpdate puts the whole policy again: UpdateAccountPasswordPolicy
// replaces it, and a field left out goes back to IAM's default.
func (rm *resourceManager) sdkUpdate(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta interface{},
) (updated *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkUpdate")
	defer func() { exit(err) }()
	if err := rm.put(ctx, desired.ko, "UPDATE"); err != nil {
		return nil, err
	}
	ko := desired.ko.DeepCopy()
	ko.Status = latest.ko.Status
	return &resource{ko}, nil
}

// sdkDelete deletes the policy; an account with none is done.
func (rm *resourceManager) sdkDelete(ctx context.Context, r *resource) (deleted *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkDelete")
	defer func() { exit(err) }()
	_, err = rm.sdkapi.DeleteAccountPasswordPolicy(ctx, &svcsdk.DeleteAccountPasswordPolicyInput{})
	rm.metrics.RecordAPICall("DELETE", "DeleteAccountPasswordPolicy", err)
	if err != nil && !isAPIError(err, noSuchEntity) {
		return nil, err
	}
	return nil, nil
}

func (rm *resourceManager) setStatusDefaults(ko *svcapitypes.AccountPasswordPolicy) {
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

// terminalAWSError: errors no retry fixes. A value IAM refuses
// (MalformedPolicyDocument, ValidationError) needs a person.
func (rm *resourceManager) terminalAWSError(err error) bool {
	return isAPIError(err, "MalformedPolicyDocument", "ValidationError", "InvalidInput")
}
