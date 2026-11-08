// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package db_proxy_target

import (
	"context"
	"errors"
	"fmt"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/rds"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	smithy "github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/rds/apis/v1alpha1"
)

// The proxy's only target group; RDS Proxy names it "default".
const defaultTargetGroup = "default"

// What RDS Proxy reports as the target for a registration of each kind:
// an instance as RDS_INSTANCE, a cluster as TRACKED_CLUSTER (plus one
// RDS_INSTANCE per member, whose RdsResourceId is the member's).
const (
	targetTypeInstance = string(svcsdktypes.TargetTypeRdsInstance)
	targetTypeCluster  = string(svcsdktypes.TargetTypeTrackedCluster)
)

func targetGroupName(ko *svcapitypes.DBProxyTarget) *string {
	if ko.Spec.TargetGroupName != nil && *ko.Spec.TargetGroupName != "" {
		return ko.Spec.TargetGroupName
	}
	return aws.String(defaultTargetGroup)
}

// registered returns the identifier this object registers and whether it
// is a cluster.
func registered(ko *svcapitypes.DBProxyTarget) (id string, cluster bool, err error) {
	inst, clus := aws.ToString(ko.Spec.DBInstanceIdentifier), aws.ToString(ko.Spec.DBClusterIdentifier)
	switch {
	case inst != "" && clus == "":
		return inst, false, nil
	case clus != "" && inst == "":
		return clus, true, nil
	default:
		return "", false, ackerr.NewTerminalError(fmt.Errorf("exactly one of spec.dbInstanceIdentifier and spec.dbClusterIdentifier must be set"))
	}
}

func (rm *resourceManager) requiredFieldsMissingFromReadOneInput(r *resource) bool {
	if r.ko.Spec.DBProxyName == nil {
		return true
	}
	_, _, err := registered(r.ko)
	return err != nil
}

// sdkFind reads the proxy's targets and keeps those of this registration:
// for an instance, the RDS_INSTANCE target with its identifier; for a
// cluster, the TRACKED_CLUSTER target with its identifier and every
// instance target RDS Proxy reports beside it.
func (rm *resourceManager) sdkFind(ctx context.Context, r *resource) (latest *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkFind")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, ackerr.NotFound
	}
	id, cluster, _ := registered(r.ko)
	targets, err := rm.describeTargets(ctx, r.ko)
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "DBProxyNotFoundFault" || apiErr.ErrorCode() == "DBProxyTargetGroupNotFoundFault") {
			return nil, ackerr.NotFound
		}
		return nil, err
	}
	var own []svcsdktypes.DBProxyTarget
	found := false
	for _, t := range targets {
		tid, ttype := aws.ToString(t.RdsResourceId), string(t.Type)
		switch {
		case !cluster && ttype == targetTypeInstance && tid == id:
			own, found = append(own, t), true
		case cluster && ttype == targetTypeCluster && tid == id:
			own, found = append(own, t), true
		case cluster && ttype == targetTypeInstance && t.TrackedClusterId != nil && aws.ToString(t.TrackedClusterId) == id:
			own = append(own, t)
		}
	}
	if !found {
		return nil, ackerr.NotFound
	}
	ko := r.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	ko.Status.Targets = observations(own)
	arn := ackv1alpha1.AWSResourceName(rm.targetARN(ko, id, cluster))
	ko.Status.ACKResourceMetadata.ARN = &arn
	return &resource{ko}, nil
}

func (rm *resourceManager) describeTargets(ctx context.Context, ko *svcapitypes.DBProxyTarget) ([]svcsdktypes.DBProxyTarget, error) {
	input := &svcsdk.DescribeDBProxyTargetsInput{
		DBProxyName:     ko.Spec.DBProxyName,
		TargetGroupName: targetGroupName(ko),
	}
	var out []svcsdktypes.DBProxyTarget
	paginator := svcsdk.NewDescribeDBProxyTargetsPaginator(rm.sdkapi, input)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		rm.metrics.RecordAPICall("READ_MANY", "DescribeDBProxyTargets", err)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Targets...)
	}
	return out, nil
}

// targetARN names the registration. RDS has no ARN for a proxy target, so
// this is the target group's ARN form with the registered resource
// appended; it is an identifier for ACK's metadata, never sent to AWS.
func (rm *resourceManager) targetARN(ko *svcapitypes.DBProxyTarget, id string, cluster bool) string {
	kind := "db"
	if cluster {
		kind = "cluster"
	}
	return rm.ARNFromName(fmt.Sprintf("db-proxy:%s/target-group:%s/%s:%s", aws.ToString(ko.Spec.DBProxyName), aws.ToString(targetGroupName(ko)), kind, id))
}

func observations(targets []svcsdktypes.DBProxyTarget) []*svcapitypes.DBProxyTargetObservation {
	out := make([]*svcapitypes.DBProxyTargetObservation, 0, len(targets))
	for _, t := range targets {
		o := &svcapitypes.DBProxyTargetObservation{
			Endpoint:      t.Endpoint,
			RDSResourceID: t.RdsResourceId,
			TargetARN:     t.TargetArn,
		}
		if t.Port != nil {
			o.Port = aws.Int64(int64(*t.Port))
		}
		if t.Role != "" {
			o.Role = aws.String(string(t.Role))
		}
		if t.Type != "" {
			o.Type = aws.String(string(t.Type))
		}
		if h := t.TargetHealth; h != nil {
			o.TargetHealth = &svcapitypes.DBProxyTargetHealth{Description: h.Description}
			if h.State != "" {
				o.TargetHealth.State = aws.String(string(h.State))
			}
			if h.Reason != "" {
				o.TargetHealth.Reason = aws.String(string(h.Reason))
			}
		}
		out = append(out, o)
	}
	return out
}

// allTargetsAvailable is the readiness rule (manager.go, IsSynced).
func allTargetsAvailable(targets []*svcapitypes.DBProxyTargetObservation) bool {
	if len(targets) == 0 {
		return false
	}
	for _, t := range targets {
		if t.TargetHealth == nil || aws.ToString(t.TargetHealth.State) != string(svcsdktypes.TargetStateAvailable) {
			return false
		}
	}
	return true
}

func (rm *resourceManager) sdkCreate(ctx context.Context, desired *resource) (created *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkCreate")
	defer func() { exit(err) }()
	if desired.ko.Spec.DBProxyName == nil {
		return nil, ackerr.NewTerminalError(fmt.Errorf("spec.dbProxyName is required"))
	}
	id, cluster, err := registered(desired.ko)
	if err != nil {
		return nil, err
	}
	input := &svcsdk.RegisterDBProxyTargetsInput{
		DBProxyName:     desired.ko.Spec.DBProxyName,
		TargetGroupName: targetGroupName(desired.ko),
	}
	if cluster {
		input.DBClusterIdentifiers = []string{id}
	} else {
		input.DBInstanceIdentifiers = []string{id}
	}
	resp, err := rm.sdkapi.RegisterDBProxyTargets(ctx, input)
	rm.metrics.RecordAPICall("CREATE", "RegisterDBProxyTargets", err)
	if err != nil {
		return nil, err
	}
	ko := desired.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	ko.Status.Targets = observations(resp.DBProxyTargets)
	arn := ackv1alpha1.AWSResourceName(rm.targetARN(ko, id, cluster))
	ko.Status.ACKResourceMetadata.ARN = &arn
	return &resource{ko}, nil
}

// sdkUpdate refuses every change: a registration is identified by all of
// its fields (the CRD also marks them immutable).
func (rm *resourceManager) sdkUpdate(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta interface{},
) (updated *resource, err error) {
	return nil, ackerr.NewTerminalError(fmt.Errorf("a DBProxyTarget cannot be changed; delete it and create another"))
}

func (rm *resourceManager) sdkDelete(ctx context.Context, r *resource) (deleted *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkDelete")
	defer func() { exit(err) }()
	if r.ko.Spec.DBProxyName == nil {
		return r, nil
	}
	id, cluster, err := registered(r.ko)
	if err != nil {
		return r, nil
	}
	input := &svcsdk.DeregisterDBProxyTargetsInput{
		DBProxyName:     r.ko.Spec.DBProxyName,
		TargetGroupName: targetGroupName(r.ko),
	}
	if cluster {
		input.DBClusterIdentifiers = []string{id}
	} else {
		input.DBInstanceIdentifiers = []string{id}
	}
	_, err = rm.sdkapi.DeregisterDBProxyTargets(ctx, input)
	rm.metrics.RecordAPICall("DELETE", "DeregisterDBProxyTargets", err)
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.ErrorCode() {
			case "DBProxyNotFoundFault", "DBProxyTargetGroupNotFoundFault", "DBProxyTargetNotFoundFault":
				return nil, nil
			}
		}
		return nil, err
	}
	return nil, nil
}

func (rm *resourceManager) setStatusDefaults(ko *svcapitypes.DBProxyTarget) {
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

// terminalAWSError: errors no retry fixes. A proxy or instance that does
// not exist yet is not terminal (it may be created after this object), so
// those faults are retried.
func (rm *resourceManager) terminalAWSError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.ErrorCode() {
	case "InvalidParameterValue", "InvalidParameterCombination", "InsufficientAvailableIPsInSubnetFault":
		return true
	default:
		return false
	}
}
