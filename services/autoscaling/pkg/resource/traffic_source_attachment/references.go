// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package traffic_source_attachment

import (
	"context"
	"fmt"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/autoscaling/apis/v1alpha1"
)

// +kubebuilder:rbac:groups=autoscaling.aws.hfl.dev,resources=autoscalinggroups,verbs=get;list;watch

func (rm *resourceManager) ClearResolvedReferences(res acktypes.AWSResource) acktypes.AWSResource {
	ko := rm.concreteResource(res).ko.DeepCopy()
	if ko.Spec.AutoScalingGroupRef != nil {
		ko.Spec.AutoScalingGroupName = nil
	}
	return &resource{ko}
}

func (rm *resourceManager) ResolveReferences(
	ctx context.Context,
	apiReader client.Reader,
	res acktypes.AWSResource,
) (acktypes.AWSResource, bool, error) {
	ko := rm.concreteResource(res).ko
	has := ko.Spec.AutoScalingGroupRef != nil
	if ko.Spec.AutoScalingGroupRef != nil && ko.Spec.AutoScalingGroupName != nil {
		return &resource{ko}, has, ackerr.ResourceReferenceAndIDNotSupportedFor("AutoScalingGroupName", "AutoScalingGroupRef")
	}
	if ref := ko.Spec.AutoScalingGroupRef; ref != nil && ref.From != nil {
		obj := &svcapitypes.AutoScalingGroup{}
		if err := getReferenced(ctx, apiReader, ko, "AutoScalingGroup", ref, obj, &obj.Status.Conditions); err != nil {
			return &resource{ko}, has, err
		}
		if obj.Spec.Name == nil {
			return &resource{ko}, has, ackerr.ResourceReferenceMissingTargetFieldFor("AutoScalingGroup", obj.Namespace, obj.Name, "Spec.Name")
		}
		ko.Spec.AutoScalingGroupName = obj.Spec.Name
	}
	return &resource{ko}, has, nil
}

func getReferenced(
	ctx context.Context,
	apiReader client.Reader,
	ko *svcapitypes.TrafficSourceAttachment,
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
	if isTerminal(conditions) {
		return ackerr.ResourceReferenceTerminal
	}
	if !isSynced(conditions) {
		return ackerr.ResourceReferenceNotSynced
	}
	return nil
}

func isSynced(conditions *[]*ackv1alpha1.Condition) bool {
	if conditions == nil {
		return false
	}
	for _, c := range *conditions {
		if c != nil && c.Type == ackv1alpha1.ConditionTypeResourceSynced {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func isTerminal(conditions *[]*ackv1alpha1.Condition) bool {
	if conditions == nil {
		return false
	}
	for _, c := range *conditions {
		if c != nil && c.Type == ackv1alpha1.ConditionTypeTerminal {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
