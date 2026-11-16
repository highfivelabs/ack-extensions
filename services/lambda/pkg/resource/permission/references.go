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

	ackelbv2apitypes "github.com/aws-controllers-k8s/elbv2-controller/apis/v1alpha1"
	acklambdaapitypes "github.com/aws-controllers-k8s/lambda-controller/apis/v1alpha1"
)

// functionRef names ACK's own Function (lambda.services.k8s.aws) and
// targetGroupRef ACK's elbv2 TargetGroup (elbv2.services.k8s.aws), the
// way ACK's generated references.go resolves a reference to another
// controller's kind: the object must be synced and not terminal, and the
// field read (the Function's spec.name, the TargetGroup's ARN) set.

// +kubebuilder:rbac:groups=lambda.services.k8s.aws,resources=functions,verbs=get;list;watch
// +kubebuilder:rbac:groups=elbv2.services.k8s.aws,resources=targetgroups,verbs=get;list

func (rm *resourceManager) ClearResolvedReferences(res acktypes.AWSResource) acktypes.AWSResource {
	ko := rm.concreteResource(res).ko.DeepCopy()
	if ko.Spec.FunctionRef != nil {
		ko.Spec.FunctionName = nil
	}
	if ko.Spec.TargetGroupRef != nil {
		ko.Spec.SourceARN = nil
	}
	return &resource{ko}
}

func (rm *resourceManager) ResolveReferences(
	ctx context.Context,
	apiReader client.Reader,
	res acktypes.AWSResource,
) (acktypes.AWSResource, bool, error) {
	ko := rm.concreteResource(res).ko
	if ko.Spec.FunctionRef == nil && ko.Spec.TargetGroupRef == nil {
		return &resource{ko}, false, nil
	}
	ko = ko.DeepCopy()
	if ref := ko.Spec.FunctionRef; ref != nil {
		if ko.Spec.FunctionName != nil {
			return &resource{ko}, true, ackerr.ResourceReferenceAndIDNotSupportedFor("FunctionName", "FunctionRef")
		}
		namespace, name, err := refTarget(ko.GetNamespace(), ref, "FunctionRef")
		if err != nil {
			return &resource{ko}, true, err
		}
		obj := &acklambdaapitypes.Function{}
		if err := apiReader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, obj); err != nil {
			return &resource{ko}, true, err
		}
		if err := syncedTarget("Function", namespace, name, obj.Status.Conditions); err != nil {
			return &resource{ko}, true, err
		}
		if obj.Spec.Name == nil {
			return &resource{ko}, true, ackerr.ResourceReferenceMissingTargetFieldFor("Function", namespace, name, "Spec.Name")
		}
		ko.Spec.FunctionName = obj.Spec.Name
	}
	if ref := ko.Spec.TargetGroupRef; ref != nil {
		if ko.Spec.SourceARN != nil {
			return &resource{ko}, true, ackerr.ResourceReferenceAndIDNotSupportedFor("SourceARN", "TargetGroupRef")
		}
		namespace, name, err := refTarget(ko.GetNamespace(), ref, "TargetGroupRef")
		if err != nil {
			return &resource{ko}, true, err
		}
		obj := &ackelbv2apitypes.TargetGroup{}
		if err := apiReader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, obj); err != nil {
			return &resource{ko}, true, err
		}
		if err := syncedTarget("TargetGroup", namespace, name, obj.Status.Conditions); err != nil {
			return &resource{ko}, true, err
		}
		if obj.Status.ACKResourceMetadata == nil || obj.Status.ACKResourceMetadata.ARN == nil || *obj.Status.ACKResourceMetadata.ARN == "" {
			return &resource{ko}, true, ackerr.ResourceReferenceMissingTargetFieldFor("TargetGroup", namespace, name, "Status.ACKResourceMetadata.ARN")
		}
		arn := string(*obj.Status.ACKResourceMetadata.ARN)
		ko.Spec.SourceARN = &arn
	}
	return &resource{ko}, true, nil
}

// refTarget is the namespace and name a reference names: its own
// namespace, or the object's.
func refTarget(owner string, ref *ackv1alpha1.AWSResourceReferenceWrapper, field string) (string, string, error) {
	if ref.From == nil || ref.From.Name == nil || *ref.From.Name == "" {
		return "", "", fmt.Errorf("provided resource reference is nil or empty: %s", field)
	}
	namespace := owner
	if ref.From.Namespace != nil && *ref.From.Namespace != "" {
		namespace = *ref.From.Namespace
	}
	return namespace, *ref.From.Name, nil
}

// syncedTarget is nil when the referenced object is synced and not
// terminal, else ACK's error for it.
func syncedTarget(kind, namespace, name string, conds []*ackv1alpha1.Condition) error {
	synced := false
	for _, cond := range conds {
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
