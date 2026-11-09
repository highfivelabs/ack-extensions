// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package db_role_association

import (
	"context"
	"fmt"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ackiamapitypes "github.com/aws-controllers-k8s/iam-controller/apis/v1alpha1"
	ackrdsapitypes "github.com/aws-controllers-k8s/rds-controller/apis/v1alpha1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/rds/apis/v1alpha1"
)

// References name ACK's own kinds (rds.services.k8s.aws and
// iam.services.k8s.aws), the way ACK's generated references.go resolves a
// reference to another service's kind: the referenced object must be
// synced and not terminal, and the field read from it must be set.

// +kubebuilder:rbac:groups=rds.services.k8s.aws,resources=dbinstances;dbclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=iam.services.k8s.aws,resources=roles,verbs=get;list;watch

func (rm *resourceManager) ClearResolvedReferences(res acktypes.AWSResource) acktypes.AWSResource {
	ko := rm.concreteResource(res).ko.DeepCopy()
	if ko.Spec.DBClusterRef != nil {
		ko.Spec.DBClusterIdentifier = nil
	}
	if ko.Spec.DBInstanceRef != nil {
		ko.Spec.DBInstanceIdentifier = nil
	}
	if ko.Spec.RoleRef != nil {
		ko.Spec.RoleARN = nil
	}
	return &resource{ko}
}

func (rm *resourceManager) ResolveReferences(
	ctx context.Context,
	apiReader client.Reader,
	res acktypes.AWSResource,
) (acktypes.AWSResource, bool, error) {
	ko := rm.concreteResource(res).ko
	has := ko.Spec.DBClusterRef != nil || ko.Spec.DBInstanceRef != nil || ko.Spec.RoleRef != nil
	if err := validateReferenceFields(ko); err != nil {
		return &resource{ko}, has, err
	}
	if ref := ko.Spec.DBClusterRef; ref != nil && ref.From != nil {
		obj := &ackrdsapitypes.DBCluster{}
		if err := getReferenced(ctx, apiReader, ko, "DBCluster", ref, obj, &obj.Status.Conditions); err != nil {
			return &resource{ko}, has, err
		}
		if obj.Spec.DBClusterIdentifier == nil {
			return &resource{ko}, has, ackerr.ResourceReferenceMissingTargetFieldFor("DBCluster", obj.Namespace, obj.Name, "Spec.DBClusterIdentifier")
		}
		ko.Spec.DBClusterIdentifier = obj.Spec.DBClusterIdentifier
	}
	if ref := ko.Spec.DBInstanceRef; ref != nil && ref.From != nil {
		obj := &ackrdsapitypes.DBInstance{}
		if err := getReferenced(ctx, apiReader, ko, "DBInstance", ref, obj, &obj.Status.Conditions); err != nil {
			return &resource{ko}, has, err
		}
		if obj.Spec.DBInstanceIdentifier == nil {
			return &resource{ko}, has, ackerr.ResourceReferenceMissingTargetFieldFor("DBInstance", obj.Namespace, obj.Name, "Spec.DBInstanceIdentifier")
		}
		ko.Spec.DBInstanceIdentifier = obj.Spec.DBInstanceIdentifier
	}
	if ref := ko.Spec.RoleRef; ref != nil && ref.From != nil {
		obj := &ackiamapitypes.Role{}
		if err := getReferenced(ctx, apiReader, ko, "Role", ref, obj, &obj.Status.Conditions); err != nil {
			return &resource{ko}, has, err
		}
		if obj.Status.ACKResourceMetadata == nil || obj.Status.ACKResourceMetadata.ARN == nil {
			return &resource{ko}, has, ackerr.ResourceReferenceMissingTargetFieldFor("Role", obj.Namespace, obj.Name, "Status.ACKResourceMetadata.ARN")
		}
		arn := string(*obj.Status.ACKResourceMetadata.ARN)
		ko.Spec.RoleARN = &arn
	}
	return &resource{ko}, has, nil
}

func validateReferenceFields(ko *svcapitypes.DBRoleAssociation) error {
	if ko.Spec.DBClusterRef != nil && ko.Spec.DBClusterIdentifier != nil {
		return ackerr.ResourceReferenceAndIDNotSupportedFor("DBClusterIdentifier", "DBClusterRef")
	}
	if ko.Spec.DBInstanceRef != nil && ko.Spec.DBInstanceIdentifier != nil {
		return ackerr.ResourceReferenceAndIDNotSupportedFor("DBInstanceIdentifier", "DBInstanceRef")
	}
	if ko.Spec.RoleRef != nil && ko.Spec.RoleARN != nil {
		return ackerr.ResourceReferenceAndIDNotSupportedFor("RoleARN", "RoleRef")
	}
	return nil
}

func getReferenced(
	ctx context.Context,
	apiReader client.Reader,
	ko *svcapitypes.DBRoleAssociation,
	kind string,
	ref *ackv1alpha1.AWSResourceReferenceWrapper,
	obj client.Object,
	conditions *[]*ackv1alpha1.Condition,
) error {
	if ref.From.Name == nil || *ref.From.Name == "" {
		return fmt.Errorf("provided resource reference is nil or empty: %sRef", kind)
	}
	namespace := ko.GetNamespace()
	if ref.From.Namespace != nil && *ref.From.Namespace != "" {
		namespace = *ref.From.Namespace
	}
	name := *ref.From.Name
	if err := apiReader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, obj); err != nil {
		return err
	}
	synced := false
	for _, cond := range *conditions {
		if cond.Type == ackv1alpha1.ConditionTypeTerminal && cond.Status == corev1.ConditionTrue {
			return ackerr.ResourceReferenceTerminalFor(kind, namespace, name)
		}
		if cond.Type == ackv1alpha1.ConditionTypeResourceSynced && cond.Status == corev1.ConditionTrue {
			synced = true
		}
	}
	if !synced {
		return ackerr.ResourceReferenceNotSyncedFor(kind, namespace, name)
	}
	return nil
}
