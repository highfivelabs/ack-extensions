// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package user_group_membership

import (
	"context"
	"errors"
	"fmt"
	"sort"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/iam"
	smithy "github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

// The fault IAM returns when the user, the group, or the membership does
// not exist.
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

func names(ps []*string) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		if p != nil && *p != "" {
			out = append(out, *p)
		}
	}
	return out
}

func set(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// managed is the read side of the non-exclusive rule: of the groups the
// user is in, the ones this object has a say over, which are those it
// lists now and those it put the user in before (status.groups). The
// order is the spec's, then the rest sorted.
func managed(userGroups, listed, put []string) []string {
	in := set(userGroups)
	seen := map[string]bool{}
	var out, rest []string
	for _, g := range listed {
		if in[g] && !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	for _, g := range put {
		if in[g] && !seen[g] {
			seen[g] = true
			rest = append(rest, g)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// changes are the groups to add and to remove to go from what is (the
// managed groups read) to what the spec lists.
func changes(listed, current []string) (add, remove []string) {
	want, have := set(listed), set(current)
	for _, g := range listed {
		if !have[g] {
			add = append(add, g)
			have[g] = true
		}
	}
	for _, g := range current {
		if !want[g] {
			remove = append(remove, g)
		}
	}
	return add, remove
}

func ptrs(xs []string) []*string {
	out := make([]*string, len(xs))
	for i := range xs {
		out[i] = aws.String(xs[i])
	}
	return out
}

func (rm *resourceManager) requiredFieldsMissingFromReadOneInput(r *resource) bool {
	return aws.ToString(r.ko.Spec.UserName) == ""
}

// groupsForUser lists every group the user is in; ackerr.NotFound when the
// user does not exist.
func (rm *resourceManager) groupsForUser(ctx context.Context, user string) ([]string, error) {
	var out []string
	p := svcsdk.NewListGroupsForUserPaginator(rm.sdkapi, &svcsdk.ListGroupsForUserInput{UserName: aws.String(user)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		rm.metrics.RecordAPICall("READ_MANY", "ListGroupsForUser", err)
		if err != nil {
			if isAPIError(err, noSuchEntity) {
				return nil, ackerr.NotFound
			}
			return nil, err
		}
		for _, g := range page.Groups {
			out = append(out, aws.ToString(g.GroupName))
		}
	}
	return out, nil
}

// sdkFind reads the user's groups and keeps those this object has a say
// over (managed). A user that does not exist has no membership; neither
// has one never made (no ARN yet) whose listed groups the user is in
// none of. Once made, a membership with none of its groups left is read
// as present and empty, so the update adds them back.
func (rm *resourceManager) sdkFind(ctx context.Context, r *resource) (latest *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkFind")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, ackerr.NotFound
	}
	userGroups, err := rm.groupsForUser(ctx, aws.ToString(r.ko.Spec.UserName))
	if err != nil {
		return nil, err
	}
	current := managed(userGroups, names(r.ko.Spec.Groups), names(r.ko.Status.Groups))
	made := r.ko.Status.ACKResourceMetadata != nil && r.ko.Status.ACKResourceMetadata.ARN != nil
	if len(current) == 0 && !made {
		return nil, ackerr.NotFound
	}
	ko := r.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	ko.Spec.Groups = ptrs(current)
	rm.setARN(ko)
	return &resource{ko}, nil
}

// setARN names the membership. IAM has no ARN for one, so this is the
// IAM ARN form with the user appended; it is an identifier for ACK's
// metadata, never sent to AWS.
func (rm *resourceManager) setARN(ko *svcapitypes.UserGroupMembership) {
	arn := ackv1alpha1.AWSResourceName(rm.ARNFromName("user-group-membership/" + aws.ToString(ko.Spec.UserName)))
	ko.Status.ACKResourceMetadata.ARN = &arn
}

func (rm *resourceManager) apply(ctx context.Context, user string, add, remove []string, op string) error {
	for _, g := range add {
		_, err := rm.sdkapi.AddUserToGroup(ctx, &svcsdk.AddUserToGroupInput{UserName: aws.String(user), GroupName: aws.String(g)})
		rm.metrics.RecordAPICall(op, "AddUserToGroup", err)
		if err != nil {
			return err
		}
	}
	for _, g := range remove {
		_, err := rm.sdkapi.RemoveUserFromGroup(ctx, &svcsdk.RemoveUserFromGroupInput{UserName: aws.String(user), GroupName: aws.String(g)})
		rm.metrics.RecordAPICall(op, "RemoveUserFromGroup", err)
		if err != nil && !isAPIError(err, noSuchEntity) {
			return err
		}
	}
	return nil
}

func (rm *resourceManager) sdkCreate(ctx context.Context, desired *resource) (created *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkCreate")
	defer func() { exit(err) }()
	listed := names(desired.ko.Spec.Groups)
	if rm.requiredFieldsMissingFromReadOneInput(desired) || len(listed) == 0 {
		return nil, ackerr.NewTerminalError(fmt.Errorf("spec.userName (or spec.userRef) and spec.groups (or spec.groupRefs) are required"))
	}
	if err := rm.apply(ctx, aws.ToString(desired.ko.Spec.UserName), listed, nil, "CREATE"); err != nil {
		return nil, err
	}
	ko := desired.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	ko.Status.Groups = ptrs(listed)
	rm.setARN(ko)
	return &resource{ko}, nil
}

// sdkUpdate adds the listed groups the user is not in, and removes the
// groups this object put the user in that are no longer listed.
func (rm *resourceManager) sdkUpdate(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta interface{},
) (updated *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkUpdate")
	defer func() { exit(err) }()
	listed := names(desired.ko.Spec.Groups)
	add, remove := changes(listed, names(latest.ko.Spec.Groups))
	if err := rm.apply(ctx, aws.ToString(desired.ko.Spec.UserName), add, remove, "UPDATE"); err != nil {
		return nil, err
	}
	ko := desired.ko.DeepCopy()
	ko.Status = latest.ko.Status
	ko.Status.Groups = ptrs(listed)
	return &resource{ko}, nil
}

// sdkDelete takes the user out of the listed groups and those this object
// put the user in. A user, group or membership already gone is removed.
func (rm *resourceManager) sdkDelete(ctx context.Context, r *resource) (deleted *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkDelete")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, nil
	}
	listed, put := names(r.ko.Spec.Groups), names(r.ko.Status.Groups)
	// Every listed or put group, once each.
	remove := managed(append(append([]string{}, listed...), put...), listed, put)
	if err := rm.apply(ctx, aws.ToString(r.ko.Spec.UserName), nil, remove, "DELETE"); err != nil {
		if isAPIError(err, noSuchEntity) {
			return nil, nil
		}
		return nil, err
	}
	return nil, nil
}

func (rm *resourceManager) setStatusDefaults(ko *svcapitypes.UserGroupMembership) {
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

// terminalAWSError: errors no retry fixes. A user or group that does not
// exist yet is retried (NoSuchEntity: it may be created after this
// object). A user at IAM's limit of groups needs a person.
func (rm *resourceManager) terminalAWSError(err error) bool {
	return isAPIError(err, "LimitExceeded", "InvalidInput")
}
