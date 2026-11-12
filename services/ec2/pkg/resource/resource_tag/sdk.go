// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package resource_tag

import (
	"context"
	"errors"
	"fmt"
	"strings"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	smithy "github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
)

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

// notFound is EC2's answer for a resource that does not exist: the code
// is per resource type (InvalidVpcID.NotFound, InvalidSubnetID.NotFound,
// InvalidInstanceID.NotFound, ...).
func notFound(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && strings.HasSuffix(apiErr.ErrorCode(), ".NotFound")
}

func (rm *resourceManager) requiredFieldsMissingFromReadOneInput(r *resource) bool {
	return aws.ToString(r.ko.Spec.ResourceID) == "" || aws.ToString(r.ko.Spec.Key) == ""
}

// describeTag is the tag of the resource with the key, if any.
func (rm *resourceManager) describeTag(ctx context.Context, resourceID, key string) (*svcsdktypes.TagDescription, error) {
	resp, err := rm.sdkapi.DescribeTags(ctx, &svcsdk.DescribeTagsInput{
		Filters: []svcsdktypes.Filter{
			{Name: aws.String("resource-id"), Values: []string{resourceID}},
			{Name: aws.String("key"), Values: []string{key}},
		},
	})
	rm.metrics.RecordAPICall("READ_ONE", "DescribeTags", err)
	if err != nil {
		return nil, err
	}
	for i := range resp.Tags {
		t := resp.Tags[i]
		if aws.ToString(t.ResourceId) == resourceID && aws.ToString(t.Key) == key {
			return &t, nil
		}
	}
	return nil, nil
}

// sdkFind reads the tag. A resource without the tag, or no resource, has
// no tag; the value read becomes the spec's, so a value changed outside
// Kubernetes shows as a delta and is put back.
func (rm *resourceManager) sdkFind(ctx context.Context, r *resource) (latest *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkFind")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, ackerr.NotFound
	}
	t, err := rm.describeTag(ctx, aws.ToString(r.ko.Spec.ResourceID), aws.ToString(r.ko.Spec.Key))
	if err != nil {
		if notFound(err) {
			return nil, ackerr.NotFound
		}
		return nil, err
	}
	if t == nil {
		return nil, ackerr.NotFound
	}
	ko := r.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	ko.Spec.Value = aws.String(aws.ToString(t.Value))
	ko.Status.ResourceType = aws.String(string(t.ResourceType))
	rm.setARN(ko)
	return &resource{ko}, nil
}

// tagARN names the tag. EC2 has no ARN for one, so this is an identifier
// for ACK's metadata, never sent to AWS.
func (rm *resourceManager) setARN(ko *svcapitypes.ResourceTag) {
	arn := ackv1alpha1.AWSResourceName(rm.ARNFromName(fmt.Sprintf("tag/%s/%s", aws.ToString(ko.Spec.ResourceID), aws.ToString(ko.Spec.Key))))
	ko.Status.ACKResourceMetadata.ARN = &arn
}

// putTag sets the key to the value on the resource; CreateTags overwrites
// a tag of the same key.
func (rm *resourceManager) putTag(ctx context.Context, ko *svcapitypes.ResourceTag, op string) error {
	_, err := rm.sdkapi.CreateTags(ctx, &svcsdk.CreateTagsInput{
		Resources: []string{aws.ToString(ko.Spec.ResourceID)},
		Tags:      []svcsdktypes.Tag{{Key: ko.Spec.Key, Value: aws.String(aws.ToString(ko.Spec.Value))}},
	})
	rm.metrics.RecordAPICall(op, "CreateTags", err)
	return err
}

func (rm *resourceManager) sdkCreate(ctx context.Context, desired *resource) (created *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkCreate")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(desired) || desired.ko.Spec.Value == nil {
		return nil, ackerr.NewTerminalError(fmt.Errorf("spec.resourceID, spec.key and spec.value are required"))
	}
	if err := rm.putTag(ctx, desired.ko, "CREATE"); err != nil {
		return nil, err
	}
	ko := desired.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	rm.setARN(ko)
	return &resource{ko}, nil
}

// sdkUpdate puts the value. The resource and the key are immutable (the
// CRD refuses a change); a difference there needs a person.
func (rm *resourceManager) sdkUpdate(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta interface{},
) (updated *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkUpdate")
	defer func() { exit(err) }()
	if aws.ToString(desired.ko.Spec.ResourceID) != aws.ToString(latest.ko.Spec.ResourceID) ||
		aws.ToString(desired.ko.Spec.Key) != aws.ToString(latest.ko.Spec.Key) {
		return nil, ackerr.NewTerminalError(fmt.Errorf("a ResourceTag's resourceID and key cannot change; delete it and create another"))
	}
	if err := rm.putTag(ctx, desired.ko, "UPDATE"); err != nil {
		return nil, err
	}
	ko := desired.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	ko.Status.ResourceType = latest.ko.Status.ResourceType
	rm.setARN(ko)
	return &resource{ko}, nil
}

// sdkDelete removes the key whatever its value (DeleteTags with no value).
// A resource already gone has no tag.
func (rm *resourceManager) sdkDelete(ctx context.Context, r *resource) (deleted *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkDelete")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, nil
	}
	_, err = rm.sdkapi.DeleteTags(ctx, &svcsdk.DeleteTagsInput{
		Resources: []string{aws.ToString(r.ko.Spec.ResourceID)},
		Tags:      []svcsdktypes.Tag{{Key: r.ko.Spec.Key}},
	})
	rm.metrics.RecordAPICall("DELETE", "DeleteTags", err)
	if err != nil && !notFound(err) {
		return nil, err
	}
	return nil, nil
}

func (rm *resourceManager) setStatusDefaults(ko *svcapitypes.ResourceTag) {
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

// terminalAWSError: errors no retry fixes. A resource that does not exist
// yet (*.NotFound) is not terminal: it may be made after this object. A
// malformed ID (InvalidID, *.Malformed), a reserved or invalid key
// (aws: is AWS's own prefix) or value, or a resource past its tag limit
// needs a person.
func (rm *resourceManager) terminalAWSError(err error) bool {
	if isAPIError(err, "InvalidID", "InvalidParameterValue", "InvalidParameter", "TagLimitExceeded") {
		return true
	}
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && strings.HasSuffix(apiErr.ErrorCode(), ".Malformed")
}
