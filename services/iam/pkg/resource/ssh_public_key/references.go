// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package ssh_public_key

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
)

// userRef names ACK's own User (iam.services.k8s.aws), resolved the way
// ACK's generated references.go resolves a reference: the referenced
// object must be synced and not terminal, and the field read from it must
// be set.

// +kubebuilder:rbac:groups=iam.services.k8s.aws,resources=users,verbs=get;list;watch

func (rm *resourceManager) ClearResolvedReferences(res acktypes.AWSResource) acktypes.AWSResource {
	ko := rm.concreteResource(res).ko.DeepCopy()
	if ko.Spec.UserRef != nil {
		ko.Spec.UserName = nil
	}
	return &resource{ko}
}

func (rm *resourceManager) ResolveReferences(
	ctx context.Context,
	apiReader client.Reader,
	res acktypes.AWSResource,
) (acktypes.AWSResource, bool, error) {
	ko := rm.concreteResource(res).ko
	has := ko.Spec.UserRef != nil
	if ko.Spec.UserRef != nil && ko.Spec.UserName != nil {
		return &resource{ko}, has, ackerr.ResourceReferenceAndIDNotSupportedFor("UserName", "UserRef")
	}
	ref := ko.Spec.UserRef
	if ref == nil || ref.From == nil {
		return &resource{ko}, has, nil
	}
	if ref.From.Name == nil || *ref.From.Name == "" {
		return &resource{ko}, has, fmt.Errorf("provided resource reference is nil or empty: UserRef")
	}
	namespace := ko.GetNamespace()
	if ref.From.Namespace != nil && *ref.From.Namespace != "" {
		namespace = *ref.From.Namespace
	}
	name := *ref.From.Name
	obj := &ackiamapitypes.User{}
	if err := apiReader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, obj); err != nil {
		return &resource{ko}, has, err
	}
	synced := false
	for _, cond := range obj.Status.Conditions {
		if cond.Type == ackv1alpha1.ConditionTypeTerminal && cond.Status == corev1.ConditionTrue {
			return &resource{ko}, has, ackerr.ResourceReferenceTerminalFor("User", namespace, name)
		}
		if cond.Type == ackv1alpha1.ConditionTypeResourceSynced && cond.Status == corev1.ConditionTrue {
			synced = true
		}
	}
	if !synced {
		return &resource{ko}, has, ackerr.ResourceReferenceNotSyncedFor("User", namespace, name)
	}
	if obj.Spec.Name == nil {
		return &resource{ko}, has, ackerr.ResourceReferenceMissingTargetFieldFor("User", namespace, name, "Spec.Name")
	}
	ko.Spec.UserName = obj.Spec.Name
	return &resource{ko}, has, nil
}
