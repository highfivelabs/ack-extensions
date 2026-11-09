// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package volume_attachment

import (
	"context"
	"errors"
	"fmt"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	smithy "github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
)

// EC2's states of an attachment (VolumeAttachment.State).
const (
	stateAttached = "attached"
	stateDetached = "detached"
)

// The faults EC2 returns when the volume, or the attachment, does not exist.
var (
	volumeNotFound     = []string{"InvalidVolume.NotFound"}
	attachmentNotFound = []string{"InvalidAttachment.NotFound", "IncorrectState"}
)

// attachmentAttached is the readiness rule (manager.go, IsSynced).
func attachmentAttached(state *string) bool {
	return aws.ToString(state) == stateAttached
}

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
	return aws.ToString(r.ko.Spec.VolumeID) == "" || aws.ToString(r.ko.Spec.InstanceID) == ""
}

// attachmentTo is the volume's attachment to the instance, if any. A volume
// attaches to an instance once, so the instance identifies it; the device
// is read back, and a different one shows as drift.
func attachmentTo(v svcsdktypes.Volume, instanceID string) (svcsdktypes.VolumeAttachment, bool) {
	for _, a := range v.Attachments {
		if aws.ToString(a.InstanceId) == instanceID && string(a.State) != stateDetached {
			return a, true
		}
	}
	return svcsdktypes.VolumeAttachment{}, false
}

// sdkFind reads the volume and keeps its attachment to the instance. A
// volume that does not exist has no attachment.
func (rm *resourceManager) sdkFind(ctx context.Context, r *resource) (latest *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkFind")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, ackerr.NotFound
	}
	resp, err := rm.sdkapi.DescribeVolumes(ctx, &svcsdk.DescribeVolumesInput{VolumeIds: []string{aws.ToString(r.ko.Spec.VolumeID)}})
	rm.metrics.RecordAPICall("READ_ONE", "DescribeVolumes", err)
	if err != nil {
		if isAPIError(err, volumeNotFound...) {
			return nil, ackerr.NotFound
		}
		return nil, err
	}
	for _, v := range resp.Volumes {
		if aws.ToString(v.VolumeId) != aws.ToString(r.ko.Spec.VolumeID) {
			continue
		}
		a, ok := attachmentTo(v, aws.ToString(r.ko.Spec.InstanceID))
		if !ok {
			break
		}
		ko := r.ko.DeepCopy()
		rm.setStatusDefaults(ko)
		ko.Spec.DeviceName = a.Device
		setObserved(ko, a)
		arn := ackv1alpha1.AWSResourceName(rm.attachmentARN(ko))
		ko.Status.ACKResourceMetadata.ARN = &arn
		return &resource{ko}, nil
	}
	return nil, ackerr.NotFound
}

func setObserved(ko *svcapitypes.VolumeAttachment, a svcsdktypes.VolumeAttachment) {
	ko.Status.State = aws.String(string(a.State))
	ko.Status.DeleteOnTermination = a.DeleteOnTermination
	ko.Status.AttachTime = nil
	if a.AttachTime != nil {
		t := metav1.NewTime(*a.AttachTime)
		ko.Status.AttachTime = &t
	}
}

// attachmentARN names the attachment. EC2 has no ARN for one, so this is
// the volume's ARN form with the instance appended; it is an identifier
// for ACK's metadata, never sent to AWS.
func (rm *resourceManager) attachmentARN(ko *svcapitypes.VolumeAttachment) string {
	return rm.ARNFromName(fmt.Sprintf("volume/%s/instance/%s", aws.ToString(ko.Spec.VolumeID), aws.ToString(ko.Spec.InstanceID)))
}

func (rm *resourceManager) sdkCreate(ctx context.Context, desired *resource) (created *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkCreate")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(desired) || aws.ToString(desired.ko.Spec.DeviceName) == "" {
		return nil, ackerr.NewTerminalError(fmt.Errorf("spec.volumeID (or spec.volumeRef), spec.instanceID and spec.deviceName are required"))
	}
	resp, err := rm.sdkapi.AttachVolume(ctx, &svcsdk.AttachVolumeInput{
		VolumeId:   desired.ko.Spec.VolumeID,
		InstanceId: desired.ko.Spec.InstanceID,
		Device:     desired.ko.Spec.DeviceName,
	})
	rm.metrics.RecordAPICall("CREATE", "AttachVolume", err)
	if err != nil {
		return nil, err
	}
	ko := desired.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	setObserved(ko, svcsdktypes.VolumeAttachment{
		State: resp.State, AttachTime: resp.AttachTime, DeleteOnTermination: resp.DeleteOnTermination,
	})
	arn := ackv1alpha1.AWSResourceName(rm.attachmentARN(ko))
	ko.Status.ACKResourceMetadata.ARN = &arn
	return &resource{ko}, nil
}

// sdkUpdate refuses every change: an attachment is identified by its
// volume, instance and device (the CRD also marks them immutable). A
// device changed outside Kubernetes shows here, and needs a person.
func (rm *resourceManager) sdkUpdate(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta interface{},
) (updated *resource, err error) {
	return nil, ackerr.NewTerminalError(fmt.Errorf("a VolumeAttachment cannot be changed; delete it and create another"))
}

// sdkDelete detaches the volume. A volume or attachment already gone is
// detached.
func (rm *resourceManager) sdkDelete(ctx context.Context, r *resource) (deleted *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkDelete")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, nil
	}
	_, err = rm.sdkapi.DetachVolume(ctx, &svcsdk.DetachVolumeInput{
		VolumeId:   r.ko.Spec.VolumeID,
		InstanceId: r.ko.Spec.InstanceID,
		Device:     r.ko.Spec.DeviceName,
	})
	rm.metrics.RecordAPICall("DELETE", "DetachVolume", err)
	if err != nil && !isAPIError(err, append(append([]string{}, volumeNotFound...), attachmentNotFound...)...) {
		return nil, err
	}
	return nil, nil
}

func (rm *resourceManager) setStatusDefaults(ko *svcapitypes.VolumeAttachment) {
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

// terminalAWSError: errors no retry fixes. A volume or instance that does
// not exist yet, or is not yet available, or a volume still attached
// elsewhere (VolumeInUse), is not terminal: it may be created, or come
// free, after this object. A bad device name or a volume in another
// Availability Zone than the instance needs a person.
func (rm *resourceManager) terminalAWSError(err error) bool {
	return isAPIError(err, "InvalidParameterValue", "InvalidParameterCombination")
}
