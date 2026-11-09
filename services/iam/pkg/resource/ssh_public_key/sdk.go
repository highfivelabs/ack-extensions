// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package ssh_public_key

import (
	"context"
	"errors"
	"fmt"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/iam"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	smithy "github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

// The fault IAM returns when the user or the key does not exist.
const noSuchEntity = "NoSuchEntity"

// statusActive is IAM's default for an uploaded key.
const statusActive = "Active"

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

// keyID is the uploaded key's ID, known only once it is uploaded.
func keyID(ko *svcapitypes.SSHPublicKey) string {
	return aws.ToString(ko.Status.SSHPublicKeyID)
}

// wantStatus is the spec's status, Active when unset.
func wantStatus(ko *svcapitypes.SSHPublicKey) string {
	if s := aws.ToString(ko.Spec.Status); s != "" {
		return s
	}
	return statusActive
}

func (rm *resourceManager) requiredFieldsMissingFromReadOneInput(r *resource) bool {
	return aws.ToString(r.ko.Spec.UserName) == "" || keyID(r.ko) == ""
}

func setObserved(ko *svcapitypes.SSHPublicKey, k *svcsdktypes.SSHPublicKey) {
	ko.Status.SSHPublicKeyID = k.SSHPublicKeyId
	ko.Status.Fingerprint = k.Fingerprint
	ko.Status.UploadDate = nil
	if k.UploadDate != nil {
		t := metav1.NewTime(*k.UploadDate)
		ko.Status.UploadDate = &t
	}
}

// sdkFind reads the key by the ID the upload returned. A key not uploaded
// yet, or deleted, or whose user is gone, is not found. The body is not
// compared: it is immutable, and IAM may return it in another form; the
// status is, so a key deactivated outside Kubernetes is set back.
func (rm *resourceManager) sdkFind(ctx context.Context, r *resource) (latest *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkFind")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, ackerr.NotFound
	}
	resp, err := rm.sdkapi.GetSSHPublicKey(ctx, &svcsdk.GetSSHPublicKeyInput{
		UserName:       r.ko.Spec.UserName,
		SSHPublicKeyId: r.ko.Status.SSHPublicKeyID,
		Encoding:       svcsdktypes.EncodingTypeSsh,
	})
	rm.metrics.RecordAPICall("READ_ONE", "GetSSHPublicKey", err)
	if err != nil {
		if isAPIError(err, noSuchEntity) {
			return nil, ackerr.NotFound
		}
		return nil, err
	}
	if resp.SSHPublicKey == nil {
		return nil, ackerr.NotFound
	}
	ko := r.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	if ko.Spec.SSHPublicKeyBody == nil {
		// Adopted by ID: the body is AWS's.
		ko.Spec.SSHPublicKeyBody = resp.SSHPublicKey.SSHPublicKeyBody
	}
	ko.Spec.Status = aws.String(string(resp.SSHPublicKey.Status))
	setObserved(ko, resp.SSHPublicKey)
	rm.setARN(ko)
	return &resource{ko}, nil
}

// setARN names the key. IAM has no ARN for one, so this is the IAM ARN
// form with the user and key ID appended; it is an identifier for ACK's
// metadata, never sent to AWS.
func (rm *resourceManager) setARN(ko *svcapitypes.SSHPublicKey) {
	arn := ackv1alpha1.AWSResourceName(rm.ARNFromName(fmt.Sprintf("user/%s/ssh-public-key/%s", aws.ToString(ko.Spec.UserName), keyID(ko))))
	ko.Status.ACKResourceMetadata.ARN = &arn
}

func (rm *resourceManager) setKeyStatus(ctx context.Context, ko *svcapitypes.SSHPublicKey, op string) error {
	_, err := rm.sdkapi.UpdateSSHPublicKey(ctx, &svcsdk.UpdateSSHPublicKeyInput{
		UserName:       ko.Spec.UserName,
		SSHPublicKeyId: ko.Status.SSHPublicKeyID,
		Status:         svcsdktypes.StatusType(wantStatus(ko)),
	})
	rm.metrics.RecordAPICall(op, "UpdateSSHPublicKey", err)
	return err
}

func (rm *resourceManager) sdkCreate(ctx context.Context, desired *resource) (created *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkCreate")
	defer func() { exit(err) }()
	if aws.ToString(desired.ko.Spec.UserName) == "" || aws.ToString(desired.ko.Spec.SSHPublicKeyBody) == "" {
		return nil, ackerr.NewTerminalError(fmt.Errorf("spec.userName (or spec.userRef) and spec.sshPublicKeyBody are required"))
	}
	resp, err := rm.sdkapi.UploadSSHPublicKey(ctx, &svcsdk.UploadSSHPublicKeyInput{
		UserName:         desired.ko.Spec.UserName,
		SSHPublicKeyBody: desired.ko.Spec.SSHPublicKeyBody,
	})
	rm.metrics.RecordAPICall("CREATE", "UploadSSHPublicKey", err)
	if err != nil {
		return nil, err
	}
	ko := desired.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	setObserved(ko, resp.SSHPublicKey)
	rm.setARN(ko)
	if wantStatus(ko) != string(resp.SSHPublicKey.Status) {
		if err := rm.setKeyStatus(ctx, ko, "CREATE"); err != nil {
			return &resource{ko}, err
		}
	}
	return &resource{ko}, nil
}

// sdkUpdate sets the key Active or Inactive; the user and body are
// immutable (the CRD refuses a change).
func (rm *resourceManager) sdkUpdate(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta interface{},
) (updated *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkUpdate")
	defer func() { exit(err) }()
	ko := desired.ko.DeepCopy()
	ko.Status = latest.ko.Status
	if err := rm.setKeyStatus(ctx, ko, "UPDATE"); err != nil {
		return nil, err
	}
	return &resource{ko}, nil
}

// sdkDelete deletes the key. A key or user already gone is deleted.
func (rm *resourceManager) sdkDelete(ctx context.Context, r *resource) (deleted *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkDelete")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, nil
	}
	_, err = rm.sdkapi.DeleteSSHPublicKey(ctx, &svcsdk.DeleteSSHPublicKeyInput{
		UserName:       r.ko.Spec.UserName,
		SSHPublicKeyId: r.ko.Status.SSHPublicKeyID,
	})
	rm.metrics.RecordAPICall("DELETE", "DeleteSSHPublicKey", err)
	if err != nil && !isAPIError(err, noSuchEntity) {
		return nil, err
	}
	return nil, nil
}

func (rm *resourceManager) setStatusDefaults(ko *svcapitypes.SSHPublicKey) {
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

// terminalAWSError: errors no retry fixes. A user not there yet is retried
// (NoSuchEntity). A malformed or unsupported key, one already uploaded for
// the user, or a user at IAM's limit of keys needs a person.
func (rm *resourceManager) terminalAWSError(err error) bool {
	return isAPIError(err, "InvalidPublicKey", "UnrecognizedPublicKeyEncoding", "DuplicateSSHPublicKey", "LimitExceeded")
}
