// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package db_role_association

import (
	"context"
	"errors"
	"fmt"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/rds"
	smithy "github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/rds/apis/v1alpha1"
)

// RDS's state of an association (DBClusterRole.Status, DBInstanceRole.Status).
const statusActive = "ACTIVE"

// The faults RDS returns when the cluster or instance, or the association,
// does not exist (their codes in aws-sdk-go-v2's service/rds/types).
var (
	clusterNotFound  = []string{"DBClusterNotFoundFault"}
	instanceNotFound = []string{"DBInstanceNotFound"}
	roleNotFound     = []string{"DBClusterRoleNotFound", "DBInstanceRoleNotFound"}
)

// associated returns the cluster or instance this object associates the
// role with, and whether it is a cluster.
func associated(ko *svcapitypes.DBRoleAssociation) (id string, cluster bool, err error) {
	inst, clus := aws.ToString(ko.Spec.DBInstanceIdentifier), aws.ToString(ko.Spec.DBClusterIdentifier)
	switch {
	case clus != "" && inst == "":
		return clus, true, nil
	case inst != "" && clus == "":
		return inst, false, nil
	default:
		return "", false, ackerr.NewTerminalError(fmt.Errorf("exactly one of spec.dbClusterIdentifier and spec.dbInstanceIdentifier must be set"))
	}
}

// featureName is the spec's feature, nil when it is unset or empty (RDS
// takes no FeatureName for an Aurora MySQL cluster).
func featureName(ko *svcapitypes.DBRoleAssociation) *string {
	if aws.ToString(ko.Spec.FeatureName) == "" {
		return nil
	}
	return ko.Spec.FeatureName
}

// observedRole is one entry of a cluster's or instance's AssociatedRoles.
type observedRole struct {
	roleARN, featureName, status string
}

// matching is the association this object describes: the same role and the
// same feature (none matching none). RDS identifies an association by
// both: one role may serve two features of one database.
func matching(roles []observedRole, roleARN, feature string) (observedRole, bool) {
	for _, r := range roles {
		if r.roleARN == roleARN && r.featureName == feature {
			return r, true
		}
	}
	return observedRole{}, false
}

// associationActive is the readiness rule (manager.go, IsSynced).
func associationActive(status *string) bool {
	return aws.ToString(status) == statusActive
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
	if aws.ToString(r.ko.Spec.RoleARN) == "" {
		return true
	}
	_, _, err := associated(r.ko)
	return err != nil
}

// associatedRoles reads the cluster's or instance's AssociatedRoles.
func (rm *resourceManager) associatedRoles(ctx context.Context, id string, cluster bool) ([]observedRole, error) {
	var out []observedRole
	if cluster {
		resp, err := rm.sdkapi.DescribeDBClusters(ctx, &svcsdk.DescribeDBClustersInput{DBClusterIdentifier: aws.String(id)})
		rm.metrics.RecordAPICall("READ_ONE", "DescribeDBClusters", err)
		if err != nil {
			if isAPIError(err, clusterNotFound...) {
				return nil, ackerr.NotFound
			}
			return nil, err
		}
		for _, c := range resp.DBClusters {
			if aws.ToString(c.DBClusterIdentifier) != id {
				continue
			}
			for _, r := range c.AssociatedRoles {
				out = append(out, observedRole{aws.ToString(r.RoleArn), aws.ToString(r.FeatureName), aws.ToString(r.Status)})
			}
		}
		return out, nil
	}
	resp, err := rm.sdkapi.DescribeDBInstances(ctx, &svcsdk.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(id)})
	rm.metrics.RecordAPICall("READ_ONE", "DescribeDBInstances", err)
	if err != nil {
		if isAPIError(err, instanceNotFound...) {
			return nil, ackerr.NotFound
		}
		return nil, err
	}
	for _, d := range resp.DBInstances {
		if aws.ToString(d.DBInstanceIdentifier) != id {
			continue
		}
		for _, r := range d.AssociatedRoles {
			out = append(out, observedRole{aws.ToString(r.RoleArn), aws.ToString(r.FeatureName), aws.ToString(r.Status)})
		}
	}
	return out, nil
}

// sdkFind reads the cluster or instance and keeps the association with
// this role and feature. A cluster or instance that does not exist has no
// association.
func (rm *resourceManager) sdkFind(ctx context.Context, r *resource) (latest *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkFind")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, ackerr.NotFound
	}
	id, cluster, _ := associated(r.ko)
	roles, err := rm.associatedRoles(ctx, id, cluster)
	if err != nil {
		return nil, err
	}
	role, ok := matching(roles, aws.ToString(r.ko.Spec.RoleARN), aws.ToString(featureName(r.ko)))
	if !ok {
		return nil, ackerr.NotFound
	}
	ko := r.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	ko.Status.Status = aws.String(role.status)
	arn := ackv1alpha1.AWSResourceName(rm.associationARN(ko, id, cluster))
	ko.Status.ACKResourceMetadata.ARN = &arn
	return &resource{ko}, nil
}

// associationARN names the association. RDS has no ARN for one, so this
// is the cluster's or instance's ARN form with the role and feature
// appended; it is an identifier for ACK's metadata, never sent to AWS.
func (rm *resourceManager) associationARN(ko *svcapitypes.DBRoleAssociation, id string, cluster bool) string {
	kind := "db"
	if cluster {
		kind = "cluster"
	}
	name := fmt.Sprintf("%s:%s/role:%s", kind, id, aws.ToString(ko.Spec.RoleARN))
	if f := featureName(ko); f != nil {
		name += "/feature:" + *f
	}
	return rm.ARNFromName(name)
}

func (rm *resourceManager) sdkCreate(ctx context.Context, desired *resource) (created *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkCreate")
	defer func() { exit(err) }()
	if aws.ToString(desired.ko.Spec.RoleARN) == "" {
		return nil, ackerr.NewTerminalError(fmt.Errorf("spec.roleARN is required"))
	}
	id, cluster, err := associated(desired.ko)
	if err != nil {
		return nil, err
	}
	if cluster {
		_, err = rm.sdkapi.AddRoleToDBCluster(ctx, &svcsdk.AddRoleToDBClusterInput{
			DBClusterIdentifier: aws.String(id),
			RoleArn:             desired.ko.Spec.RoleARN,
			FeatureName:         featureName(desired.ko),
		})
		rm.metrics.RecordAPICall("CREATE", "AddRoleToDBCluster", err)
	} else {
		if featureName(desired.ko) == nil {
			return nil, ackerr.NewTerminalError(fmt.Errorf("spec.featureName is required for a DB instance"))
		}
		_, err = rm.sdkapi.AddRoleToDBInstance(ctx, &svcsdk.AddRoleToDBInstanceInput{
			DBInstanceIdentifier: aws.String(id),
			RoleArn:              desired.ko.Spec.RoleARN,
			FeatureName:          featureName(desired.ko),
		})
		rm.metrics.RecordAPICall("CREATE", "AddRoleToDBInstance", err)
	}
	if err != nil {
		return nil, err
	}
	ko := desired.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	arn := ackv1alpha1.AWSResourceName(rm.associationARN(ko, id, cluster))
	ko.Status.ACKResourceMetadata.ARN = &arn
	return &resource{ko}, nil
}

// sdkUpdate refuses every change: an association is identified by all of
// its fields (the CRD also marks them immutable).
func (rm *resourceManager) sdkUpdate(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta interface{},
) (updated *resource, err error) {
	return nil, ackerr.NewTerminalError(fmt.Errorf("a DBRoleAssociation cannot be changed; delete it and create another"))
}

func (rm *resourceManager) sdkDelete(ctx context.Context, r *resource) (deleted *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkDelete")
	defer func() { exit(err) }()
	if aws.ToString(r.ko.Spec.RoleARN) == "" {
		return r, nil
	}
	id, cluster, err := associated(r.ko)
	if err != nil {
		return r, nil
	}
	var gone []string
	if cluster {
		_, err = rm.sdkapi.RemoveRoleFromDBCluster(ctx, &svcsdk.RemoveRoleFromDBClusterInput{
			DBClusterIdentifier: aws.String(id),
			RoleArn:             r.ko.Spec.RoleARN,
			FeatureName:         featureName(r.ko),
		})
		rm.metrics.RecordAPICall("DELETE", "RemoveRoleFromDBCluster", err)
		gone = append(append(gone, roleNotFound...), clusterNotFound...)
	} else {
		_, err = rm.sdkapi.RemoveRoleFromDBInstance(ctx, &svcsdk.RemoveRoleFromDBInstanceInput{
			DBInstanceIdentifier: aws.String(id),
			RoleArn:              r.ko.Spec.RoleARN,
			FeatureName:          featureName(r.ko),
		})
		rm.metrics.RecordAPICall("DELETE", "RemoveRoleFromDBInstance", err)
		gone = append(append(gone, roleNotFound...), instanceNotFound...)
	}
	if err != nil && !isAPIError(err, gone...) {
		return nil, err
	}
	return nil, nil
}

func (rm *resourceManager) setStatusDefaults(ko *svcapitypes.DBRoleAssociation) {
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

// terminalAWSError: errors no retry fixes. A cluster or instance that does
// not exist yet, or is not yet available, is not terminal (it may be
// created or finish modifying after this object), so those faults are
// retried. An association that already exists for another role or
// feature, or a cluster at its quota of roles, needs a person.
func (rm *resourceManager) terminalAWSError(err error) bool {
	return isAPIError(err,
		"InvalidParameterValue", "InvalidParameterCombination",
		"DBClusterRoleAlreadyExists", "DBInstanceRoleAlreadyExists",
		"DBClusterRoleQuotaExceeded", "DBInstanceRoleQuotaExceeded",
	)
}
