// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package permission

import (
	"context"
	"fmt"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	acklambdaapitypes "github.com/aws-controllers-k8s/lambda-controller/apis/v1alpha1"
)

// functionRef names ACK's own Function (lambda.services.k8s.aws), the way
// ACK's generated references.go resolves a reference to another
// controller's kind: the Function must be synced and not terminal, and
// its spec.name set.

// +kubebuilder:rbac:groups=lambda.services.k8s.aws,resources=functions,verbs=get;list;watch

func (rm *resourceManager) ClearResolvedReferences(res acktypes.AWSResource) acktypes.AWSResource {
	ko := rm.concreteResource(res).ko.DeepCopy()
	if ko.Spec.FunctionRef != nil {
		ko.Spec.FunctionName = nil
	}
	return &resource{ko}
}

func (rm *resourceManager) ResolveReferences(
	ctx context.Context,
	apiReader client.Reader,
	res acktypes.AWSResource,
) (acktypes.AWSResource, bool, error) {
	ko := rm.concreteResource(res).ko
	ref := ko.Spec.FunctionRef
	if ref == nil {
		return &resource{ko}, false, nil
	}
	if ko.Spec.FunctionName != nil {
		return &resource{ko}, true, ackerr.ResourceReferenceAndIDNotSupportedFor("FunctionName", "FunctionRef")
	}
	if ref.From == nil || ref.From.Name == nil || *ref.From.Name == "" {
		return &resource{ko}, true, fmt.Errorf("provided resource reference is nil or empty: FunctionRef")
	}
	namespace := ko.GetNamespace()
	if ref.From.Namespace != nil && *ref.From.Namespace != "" {
		namespace = *ref.From.Namespace
	}
	name := *ref.From.Name
	obj := &acklambdaapitypes.Function{}
	if err := apiReader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, obj); err != nil {
		return &resource{ko}, true, err
	}
	synced := false
	for _, cond := range obj.Status.Conditions {
		if cond.Type == ackv1alpha1.ConditionTypeTerminal && cond.Status == corev1.ConditionTrue {
			return &resource{ko}, true, ackerr.ResourceReferenceTerminalFor("Function", namespace, name)
		}
		if cond.Type == ackv1alpha1.ConditionTypeResourceSynced && cond.Status == corev1.ConditionTrue {
			synced = true
		}
	}
	if !synced {
		return &resource{ko}, true, ackerr.ResourceReferenceNotSyncedFor("Function", namespace, name)
	}
	if obj.Spec.Name == nil {
		return &resource{ko}, true, ackerr.ResourceReferenceMissingTargetFieldFor("Function", namespace, name, "Spec.Name")
	}
	ko = ko.DeepCopy()
	ko.Spec.FunctionName = obj.Spec.Name
	return &resource{ko}, true, nil
}
