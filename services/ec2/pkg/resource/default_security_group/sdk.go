// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package default_security_group

import (
	"context"
	"errors"
	"fmt"
	"strings"

	ackec2 "github.com/aws-controllers-k8s/ec2-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	smithy "github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
	"github.com/highfivelabs/ack-extensions/services/ec2/pkg/tags"
)

// A VPC's default security group is found, never made, and never deleted
// (Terraform's aws_default_security_group). Until an object has found it
// (no status.id), its read finds nothing, so the runtime's create path
// takes the group over: the account-wide singleton pattern
// (docs/handoff.md), which keeps the runtime from refusing the object as
// "already exists".

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
	return aws.ToString(r.ko.Status.ID) == ""
}

// describeGroup reads one security group: by ID, or the VPC's default.
func (rm *resourceManager) describeGroup(ctx context.Context, groupID, vpcID string) (*svcsdktypes.SecurityGroup, error) {
	input := &svcsdk.DescribeSecurityGroupsInput{}
	if groupID != "" {
		input.GroupIds = []string{groupID}
	} else {
		input.Filters = []svcsdktypes.Filter{
			{Name: aws.String("vpc-id"), Values: []string{vpcID}},
			{Name: aws.String("group-name"), Values: []string{"default"}},
		}
	}
	resp, err := rm.sdkapi.DescribeSecurityGroups(ctx, input)
	rm.metrics.RecordAPICall("READ_ONE", "DescribeSecurityGroups", err)
	if err != nil {
		return nil, err
	}
	for i := range resp.SecurityGroups {
		g := resp.SecurityGroups[i]
		if groupID != "" && aws.ToString(g.GroupId) == groupID {
			return &g, nil
		}
		if groupID == "" && aws.ToString(g.GroupName) == "default" && aws.ToString(g.VpcId) == vpcID {
			return &g, nil
		}
	}
	return nil, nil
}

// observed is r with the group's rules, tags and name as EC2 has them.
// AWS's own tags (aws:) are not the spec's to manage and are left out.
func (rm *resourceManager) observed(r *resource, g *svcsdktypes.SecurityGroup) *resource {
	ko := r.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	ko.Status.ID = g.GroupId
	ko.Status.GroupName = g.GroupName
	var ingress, egress []*ackec2.IPPermission
	for i := range g.IpPermissions {
		ingress = append(ingress, rm.setResourceIPPermission(&g.IpPermissions[i]))
	}
	for i := range g.IpPermissionsEgress {
		egress = append(egress, rm.setResourceIPPermission(&g.IpPermissionsEgress[i]))
	}
	ko.Spec.IngressRules, ko.Spec.EgressRules = ingress, egress
	var t []*svcapitypes.Tag
	for _, tag := range g.Tags {
		if strings.HasPrefix(aws.ToString(tag.Key), "aws:") {
			continue
		}
		t = append(t, &svcapitypes.Tag{Key: tag.Key, Value: tag.Value})
	}
	ko.Spec.Tags = t
	rm.setARN(ko)
	return &resource{ko}
}

// sdkFind reads the group taken over. A group gone (its VPC deleted) is
// not found.
func (rm *resourceManager) sdkFind(ctx context.Context, r *resource) (latest *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkFind")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, ackerr.NotFound
	}
	g, err := rm.describeGroup(ctx, aws.ToString(r.ko.Status.ID), "")
	if err != nil {
		if isAPIError(err, "InvalidGroup.NotFound") {
			return nil, ackerr.NotFound
		}
		return nil, err
	}
	if g == nil {
		return nil, ackerr.NotFound
	}
	return rm.observed(r, g), nil
}

// setARN names the group as EC2 does
// (arn:aws:ec2:region:account:security-group/sg-...).
func (rm *resourceManager) setARN(ko *svcapitypes.DefaultSecurityGroup) {
	arn := ackv1alpha1.AWSResourceName(rm.ARNFromName("security-group/" + aws.ToString(ko.Status.ID)))
	ko.Status.ACKResourceMetadata.ARN = &arn
}

// refusesGroupRefs: a rule's groupRef (ACK's SecurityGroup) is not
// resolved here; the rule gives the group's ID.
func refusesGroupRefs(ko *svcapitypes.DefaultSecurityGroup) error {
	for _, rules := range [][]*ackec2.IPPermission{ko.Spec.IngressRules, ko.Spec.EgressRules} {
		for _, rule := range rules {
			for _, pair := range rule.UserIDGroupPairs {
				if pair != nil && pair.GroupRef != nil {
					return ackerr.NewTerminalError(fmt.Errorf("a DefaultSecurityGroup rule's userIDGroupPairs take groupID, not groupRef"))
				}
			}
		}
	}
	return nil
}

// sdkCreate takes the VPC's default group over: finds it, then makes its
// rules and tags the spec's, revoking every other rule (EC2's default
// egress rule too) and tag.
func (rm *resourceManager) sdkCreate(ctx context.Context, desired *resource) (created *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkCreate")
	defer func() { exit(err) }()
	if err := refusesGroupRefs(desired.ko); err != nil {
		return nil, err
	}
	vpcID := aws.ToString(desired.ko.Spec.VPCID)
	if vpcID == "" {
		return nil, ackerr.NewTerminalError(fmt.Errorf("spec.vpcID or spec.vpcRef is required"))
	}
	g, err := rm.describeGroup(ctx, "", vpcID)
	if err != nil {
		return nil, err
	}
	if g == nil {
		// The VPC is not there yet, or not yet with its default group:
		// retried.
		return nil, fmt.Errorf("VPC %s has no default security group", vpcID)
	}
	current := rm.observed(desired, g)
	want := desired.ko.DeepCopy()
	rm.setStatusDefaults(want)
	want.Status.ID, want.Status.GroupName = g.GroupId, g.GroupName
	rm.setARN(want)
	if err := rm.syncSGRules(ctx, &resource{want}, current); err != nil {
		return nil, err
	}
	if err := tags.Sync(ctx, rm.sdkapi, rm.metrics, aws.ToString(g.GroupId), want.Spec.Tags, current.ko.Spec.Tags); err != nil {
		return nil, err
	}
	return &resource{want}, nil
}

// sdkUpdate makes the rules and tags the spec's again. The VPC is
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
	if err := refusesGroupRefs(desired.ko); err != nil {
		return nil, err
	}
	if aws.ToString(desired.ko.Spec.VPCID) != aws.ToString(latest.ko.Spec.VPCID) {
		return nil, ackerr.NewTerminalError(fmt.Errorf("a DefaultSecurityGroup's VPC cannot change; delete it and create another"))
	}
	want := desired.ko.DeepCopy()
	rm.setStatusDefaults(want)
	want.Status.ID, want.Status.GroupName = latest.ko.Status.ID, latest.ko.Status.GroupName
	rm.setARN(want)
	if err := rm.syncSGRules(ctx, &resource{want}, latest); err != nil {
		return nil, err
	}
	if !tags.Equal(want.Spec.Tags, latest.ko.Spec.Tags) {
		if err := tags.Sync(ctx, rm.sdkapi, rm.metrics, aws.ToString(want.Status.ID), want.Spec.Tags, latest.ko.Spec.Tags); err != nil {
			return nil, err
		}
	}
	return &resource{want}, nil
}

// sdkDelete leaves the group and its rules as they are: EC2 deletes a
// default group only with its VPC.
func (rm *resourceManager) sdkDelete(ctx context.Context, r *resource) (deleted *resource, err error) {
	return nil, nil
}

func (rm *resourceManager) setStatusDefaults(ko *svcapitypes.DefaultSecurityGroup) {
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

// terminalAWSError: errors no retry fixes. A VPC not there yet is not
// terminal (it may be made after this object); a malformed rule or ID, or
// a group past its rule limit, needs a person.
func (rm *resourceManager) terminalAWSError(err error) bool {
	if isAPIError(err, "InvalidParameterValue", "InvalidParameter", "InvalidPermission.Malformed", "RulesPerSecurityGroupLimitExceeded", "TagLimitExceeded") {
		return true
	}
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && strings.HasSuffix(apiErr.ErrorCode(), ".Malformed")
}
