// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package default_security_group

import (
	"context"
	"fmt"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrt "github.com/aws-controllers-k8s/runtime/pkg/runtime"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
)

// vpcRef names ACK's VPC (ec2.services.k8s.aws; apis/v1alpha1/ack_kinds.go),
// resolved as ACK's generated references.go resolves a reference: the VPC
// must be synced and not terminal, and its status.vpcID set.

// +kubebuilder:rbac:groups=ec2.services.k8s.aws,resources=vpcs,verbs=get;list;watch

func (rm *resourceManager) ClearResolvedReferences(res acktypes.AWSResource) acktypes.AWSResource {
	ko := rm.concreteResource(res).ko.DeepCopy()
	if ko.Spec.VPCRef != nil {
		ko.Spec.VPCID = nil
	}
	return &resource{ko}
}

func (rm *resourceManager) ResolveReferences(
	ctx context.Context,
	apiReader client.Reader,
	res acktypes.AWSResource,
) (acktypes.AWSResource, bool, error) {
	ko := rm.concreteResource(res).ko.DeepCopy()
	ref := ko.Spec.VPCRef
	has := ref != nil
	if ref != nil && ko.Spec.VPCID != nil {
		return &resource{ko}, has, ackerr.ResourceReferenceAndIDNotSupportedFor("VPCID", "VPCRef")
	}
	if ref == nil || ref.From == nil {
		return &resource{ko}, has, nil
	}
	if ref.From.Name == nil || *ref.From.Name == "" {
		return &resource{ko}, has, fmt.Errorf("provided resource reference is nil or empty: VPCRef")
	}
	namespace, _, err := ackrt.ValidateCrossNamespaceReference(rm.cfg.EnableCrossNamespace, ko.GetNamespace(), ref.From.Namespace, *ref.From.Name)
	if err != nil {
		return &resource{ko}, has, err
	}
	name := *ref.From.Name
	obj := &svcapitypes.VPC{}
	if err := apiReader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, obj); err != nil {
		return &resource{ko}, has, err
	}
	synced := false
	for _, cond := range obj.Status.Conditions {
		if cond.Type == ackv1alpha1.ConditionTypeTerminal && cond.Status == corev1.ConditionTrue {
			return &resource{ko}, has, ackerr.ResourceReferenceTerminalFor("VPC", namespace, name)
		}
		if cond.Type == ackv1alpha1.ConditionTypeResourceSynced && cond.Status == corev1.ConditionTrue {
			synced = true
		}
	}
	if !synced {
		return &resource{ko}, has, ackerr.ResourceReferenceNotSyncedFor("VPC", namespace, name)
	}
	if obj.Status.VPCID == nil {
		return &resource{ko}, has, ackerr.ResourceReferenceMissingTargetFieldFor("VPC", namespace, name, "Status.VPCID")
	}
	ko.Spec.VPCID = obj.Status.VPCID
	return &resource{ko}, has, nil
}
