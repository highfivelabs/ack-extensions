// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package web_acl_association

import (
	"context"
	"errors"
	"fmt"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/wafv2"
	smithy "github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/wafv2/apis/v1alpha1"
)

// The fault WAFv2 returns when the resource or the web ACL does not exist.
var nonexistent = []string{"WAFNonexistentItemException"}

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
	return aws.ToString(r.ko.Spec.ResourceARN) == ""
}

// webACLFor is the ARN and name of the web ACL the resource is associated
// with; an empty ARN when there is none.
func (rm *resourceManager) webACLFor(ctx context.Context, resourceARN string) (arn, name string, err error) {
	resp, err := rm.sdkapi.GetWebACLForResource(ctx, &svcsdk.GetWebACLForResourceInput{ResourceArn: aws.String(resourceARN)})
	rm.metrics.RecordAPICall("READ_ONE", "GetWebACLForResource", err)
	if err != nil {
		if isAPIError(err, nonexistent...) {
			return "", "", nil
		}
		return "", "", err
	}
	if resp.WebACL == nil {
		return "", "", nil
	}
	return aws.ToString(resp.WebACL.ARN), aws.ToString(resp.WebACL.Name), nil
}

// sdkFind reads the resource's web ACL. A resource with none, or that does
// not exist, has no association. A different web ACL is read back as the
// spec's, so it shows as drift that sdkUpdate puts right.
func (rm *resourceManager) sdkFind(ctx context.Context, r *resource) (latest *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkFind")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, ackerr.NotFound
	}
	arn, name, err := rm.webACLFor(ctx, aws.ToString(r.ko.Spec.ResourceARN))
	if err != nil {
		return nil, err
	}
	if arn == "" {
		return nil, ackerr.NotFound
	}
	ko := r.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	ko.Spec.WebACLARN = aws.String(arn)
	ko.Status.WebACLName = aws.String(name)
	rm.setARN(ko)
	return &resource{ko}, nil
}

// setARN names the association. WAFv2 has no ARN for one, so this is the
// WAFv2 ARN form with the resource's ARN appended; it is an identifier for
// ACK's metadata, never sent to AWS.
func (rm *resourceManager) setARN(ko *svcapitypes.WebACLAssociation) {
	arn := ackv1alpha1.AWSResourceName(rm.ARNFromName("webacl-association/" + aws.ToString(ko.Spec.ResourceARN)))
	ko.Status.ACKResourceMetadata.ARN = &arn
}

func (rm *resourceManager) associate(ctx context.Context, ko *svcapitypes.WebACLAssociation, op string) error {
	_, err := rm.sdkapi.AssociateWebACL(ctx, &svcsdk.AssociateWebACLInput{
		ResourceArn: ko.Spec.ResourceARN,
		WebACLArn:   ko.Spec.WebACLARN,
	})
	rm.metrics.RecordAPICall(op, "AssociateWebACL", err)
	return err
}

func (rm *resourceManager) sdkCreate(ctx context.Context, desired *resource) (created *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkCreate")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(desired) || aws.ToString(desired.ko.Spec.WebACLARN) == "" {
		return nil, ackerr.NewTerminalError(fmt.Errorf("spec.resourceARN and spec.webACLARN (or spec.webACLRef) are required"))
	}
	if err := rm.associate(ctx, desired.ko, "CREATE"); err != nil {
		return nil, err
	}
	ko := desired.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	rm.setARN(ko)
	return &resource{ko}, nil
}

// sdkUpdate puts the spec's web ACL back. Both fields are immutable (the
// CRD refuses a change), so a difference comes from outside Kubernetes:
// the resource was associated with another web ACL. AssociateWebACL
// replaces it.
func (rm *resourceManager) sdkUpdate(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta interface{},
) (updated *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkUpdate")
	defer func() { exit(err) }()
	if err := rm.associate(ctx, desired.ko, "UPDATE"); err != nil {
		return nil, err
	}
	ko := desired.ko.DeepCopy()
	ko.Status = latest.ko.Status
	ko.Status.WebACLName = nil
	return &resource{ko}, nil
}

// sdkDelete disassociates the web ACL, only if it is still the spec's: a
// resource since associated with another web ACL is left alone. A
// resource already gone, or with no web ACL, is disassociated.
func (rm *resourceManager) sdkDelete(ctx context.Context, r *resource) (deleted *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkDelete")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, nil
	}
	arn, _, err := rm.webACLFor(ctx, aws.ToString(r.ko.Spec.ResourceARN))
	if err != nil {
		return nil, err
	}
	if arn == "" || arn != aws.ToString(r.ko.Spec.WebACLARN) {
		return nil, nil
	}
	_, err = rm.sdkapi.DisassociateWebACL(ctx, &svcsdk.DisassociateWebACLInput{ResourceArn: r.ko.Spec.ResourceARN})
	rm.metrics.RecordAPICall("DELETE", "DisassociateWebACL", err)
	if err != nil && !isAPIError(err, nonexistent...) {
		return nil, err
	}
	return nil, nil
}

func (rm *resourceManager) setStatusDefaults(ko *svcapitypes.WebACLAssociation) {
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

// terminalAWSError: errors no retry fixes. A resource or web ACL that does
// not exist yet is retried (WAFNonexistentItemException: it may be created
// after this object). A web ACL of the wrong scope, or a resource WAFv2
// cannot protect, needs a person.
func (rm *resourceManager) terminalAWSError(err error) bool {
	return isAPIError(err, "WAFInvalidParameterException", "WAFInvalidOperationException")
}
