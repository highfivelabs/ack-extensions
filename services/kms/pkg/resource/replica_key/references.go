// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package replica_key

import (
	"context"
	"fmt"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ackkmsapitypes "github.com/aws-controllers-k8s/kms-controller/apis/v1alpha1"
)

// keyRef names ACK's own Key (kms.services.k8s.aws), the way
// ACK's generated references.go resolves a reference to another
// controller's kind: the Key must be synced and not terminal, and
// its status.keyID set.

// +kubebuilder:rbac:groups=kms.services.k8s.aws,resources=keys,verbs=get;list;watch

func (rm *resourceManager) ClearResolvedReferences(res acktypes.AWSResource) acktypes.AWSResource {
	ko := rm.concreteResource(res).ko.DeepCopy()
	if ko.Spec.KeyRef != nil {
		ko.Spec.KeyID = nil
	}
	return &resource{ko}
}

func (rm *resourceManager) ResolveReferences(
	ctx context.Context,
	apiReader client.Reader,
	res acktypes.AWSResource,
) (acktypes.AWSResource, bool, error) {
	ko := rm.concreteResource(res).ko
	ref := ko.Spec.KeyRef
	if ref == nil {
		return &resource{ko}, false, nil
	}
	if ko.Spec.KeyID != nil {
		return &resource{ko}, true, ackerr.ResourceReferenceAndIDNotSupportedFor("KeyID", "KeyRef")
	}
	if ref.From == nil || ref.From.Name == nil || *ref.From.Name == "" {
		return &resource{ko}, true, fmt.Errorf("provided resource reference is nil or empty: KeyRef")
	}
	namespace := ko.GetNamespace()
	if ref.From.Namespace != nil && *ref.From.Namespace != "" {
		namespace = *ref.From.Namespace
	}
	name := *ref.From.Name
	obj := &ackkmsapitypes.Key{}
	if err := apiReader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, obj); err != nil {
		return &resource{ko}, true, err
	}
	synced := false
	for _, cond := range obj.Status.Conditions {
		if cond.Type == ackv1alpha1.ConditionTypeTerminal && cond.Status == corev1.ConditionTrue {
			return &resource{ko}, true, ackerr.ResourceReferenceTerminalFor("Key", namespace, name)
		}
		if cond.Type == ackv1alpha1.ConditionTypeResourceSynced && cond.Status == corev1.ConditionTrue {
			synced = true
		}
	}
	if !synced {
		return &resource{ko}, true, ackerr.ResourceReferenceNotSyncedFor("Key", namespace, name)
	}
	if obj.Status.KeyID == nil {
		return &resource{ko}, true, ackerr.ResourceReferenceMissingTargetFieldFor("Key", namespace, name, "Status.KeyID")
	}
	ko = ko.DeepCopy()
	ko.Spec.KeyID = obj.Status.KeyID
	return &resource{ko}, true, nil
}
