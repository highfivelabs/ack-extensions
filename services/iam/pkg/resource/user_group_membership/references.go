// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package user_group_membership

import (
	"context"
	"fmt"

	ackiamapitypes "github.com/aws-controllers-k8s/iam-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

// userRef and groupRefs name ACK's own User and Group
// (iam.services.k8s.aws), resolved the way ACK's generated references.go
// resolves a reference: the referenced object must be synced and not
// terminal, and the field read from it must be set.

// +kubebuilder:rbac:groups=iam.services.k8s.aws,resources=users;groups,verbs=get;list;watch

func (rm *resourceManager) ClearResolvedReferences(res acktypes.AWSResource) acktypes.AWSResource {
	ko := rm.concreteResource(res).ko.DeepCopy()
	if ko.Spec.UserRef != nil {
		ko.Spec.UserName = nil
	}
	if len(ko.Spec.GroupRefs) > 0 {
		ko.Spec.Groups = nil
	}
	return &resource{ko}
}

func (rm *resourceManager) ResolveReferences(
	ctx context.Context,
	apiReader client.Reader,
	res acktypes.AWSResource,
) (acktypes.AWSResource, bool, error) {
	ko := rm.concreteResource(res).ko
	has := ko.Spec.UserRef != nil || len(ko.Spec.GroupRefs) > 0
	if ko.Spec.UserRef != nil && ko.Spec.UserName != nil {
		return &resource{ko}, has, ackerr.ResourceReferenceAndIDNotSupportedFor("UserName", "UserRef")
	}
	if len(ko.Spec.GroupRefs) > 0 && len(ko.Spec.Groups) > 0 {
		return &resource{ko}, has, ackerr.ResourceReferenceAndIDNotSupportedFor("Groups", "GroupRefs")
	}
	if ref := ko.Spec.UserRef; ref != nil && ref.From != nil {
		obj := &ackiamapitypes.User{}
		if err := getReferenced(ctx, apiReader, ko, "User", ref, obj, &obj.Status.Conditions); err != nil {
			return &resource{ko}, has, err
		}
		if obj.Spec.Name == nil {
			return &resource{ko}, has, ackerr.ResourceReferenceMissingTargetFieldFor("User", obj.Namespace, obj.Name, "Spec.Name")
		}
		ko.Spec.UserName = obj.Spec.Name
	}
	var groups []*string
	for _, ref := range ko.Spec.GroupRefs {
		if ref == nil || ref.From == nil {
			continue
		}
		obj := &ackiamapitypes.Group{}
		if err := getReferenced(ctx, apiReader, ko, "Group", ref, obj, &obj.Status.Conditions); err != nil {
			return &resource{ko}, has, err
		}
		if obj.Spec.Name == nil {
			return &resource{ko}, has, ackerr.ResourceReferenceMissingTargetFieldFor("Group", obj.Namespace, obj.Name, "Spec.Name")
		}
		groups = append(groups, obj.Spec.Name)
	}
	if len(ko.Spec.GroupRefs) > 0 {
		ko.Spec.Groups = groups
	}
	return &resource{ko}, has, nil
}

func getReferenced(
	ctx context.Context,
	apiReader client.Reader,
	ko *svcapitypes.UserGroupMembership,
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
