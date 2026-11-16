// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package default_network_acl

import (
	"context"
	"fmt"

	ackec2 "github.com/aws-controllers-k8s/ec2-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrt "github.com/aws-controllers-k8s/runtime/pkg/runtime"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// vpcRef names ACK's VPC and subnetRefs ACK's Subnets
// (ec2.services.k8s.aws), resolved as ACK's generated references.go
// resolves a reference: the object must be synced and not terminal, and
// its ID in status set.

// +kubebuilder:rbac:groups=ec2.services.k8s.aws,resources=vpcs;subnets,verbs=get;list;watch

func (rm *resourceManager) ClearResolvedReferences(res acktypes.AWSResource) acktypes.AWSResource {
	ko := rm.concreteResource(res).ko.DeepCopy()
	if ko.Spec.VPCRef != nil {
		ko.Spec.VPCID = nil
	}
	if len(ko.Spec.SubnetRefs) > 0 {
		ko.Spec.SubnetIDs = nil
	}
	return &resource{ko}
}

// ackStatus checks an ACK object's conditions as ACK's references do.
func ackStatus(kind, namespace, name string, conds []*ackv1alpha1.Condition) error {
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

func (rm *resourceManager) refName(namespace string, ref *ackv1alpha1.AWSResourceReferenceWrapper, field string) (types.NamespacedName, error) {
	if ref.From == nil || ref.From.Name == nil || *ref.From.Name == "" {
		return types.NamespacedName{}, fmt.Errorf("provided resource reference is nil or empty: %s", field)
	}
	ns, _, err := ackrt.ValidateCrossNamespaceReference(rm.cfg.EnableCrossNamespace, namespace, ref.From.Namespace, *ref.From.Name)
	if err != nil {
		return types.NamespacedName{}, err
	}
	return types.NamespacedName{Namespace: ns, Name: *ref.From.Name}, nil
}

func (rm *resourceManager) ResolveReferences(
	ctx context.Context,
	apiReader client.Reader,
	res acktypes.AWSResource,
) (acktypes.AWSResource, bool, error) {
	ko := rm.concreteResource(res).ko.DeepCopy()
	has := ko.Spec.VPCRef != nil || len(ko.Spec.SubnetRefs) > 0
	if ko.Spec.VPCRef != nil && ko.Spec.VPCID != nil {
		return &resource{ko}, has, ackerr.ResourceReferenceAndIDNotSupportedFor("VPCID", "VPCRef")
	}
	if len(ko.Spec.SubnetRefs) > 0 && len(ko.Spec.SubnetIDs) > 0 {
		return &resource{ko}, has, ackerr.ResourceReferenceAndIDNotSupportedFor("SubnetIDs", "SubnetRefs")
	}
	if ref := ko.Spec.VPCRef; ref != nil && ref.From != nil {
		nn, err := rm.refName(ko.GetNamespace(), ref, "VPCRef")
		if err != nil {
			return &resource{ko}, has, err
		}
		obj := &ackec2.VPC{}
		if err := apiReader.Get(ctx, nn, obj); err != nil {
			return &resource{ko}, has, err
		}
		if err := ackStatus("VPC", nn.Namespace, nn.Name, obj.Status.Conditions); err != nil {
			return &resource{ko}, has, err
		}
		if obj.Status.VPCID == nil {
			return &resource{ko}, has, ackerr.ResourceReferenceMissingTargetFieldFor("VPC", nn.Namespace, nn.Name, "Status.VPCID")
		}
		ko.Spec.VPCID = obj.Status.VPCID
	}
	if len(ko.Spec.SubnetRefs) > 0 {
		var ids []*string
		for _, ref := range ko.Spec.SubnetRefs {
			if ref == nil {
				continue
			}
			nn, err := rm.refName(ko.GetNamespace(), ref, "SubnetRefs")
			if err != nil {
				return &resource{ko}, has, err
			}
			obj := &ackec2.Subnet{}
			if err := apiReader.Get(ctx, nn, obj); err != nil {
				return &resource{ko}, has, err
			}
			if err := ackStatus("Subnet", nn.Namespace, nn.Name, obj.Status.Conditions); err != nil {
				return &resource{ko}, has, err
			}
			if obj.Status.SubnetID == nil {
				return &resource{ko}, has, ackerr.ResourceReferenceMissingTargetFieldFor("Subnet", nn.Namespace, nn.Name, "Status.SubnetID")
			}
			ids = append(ids, obj.Status.SubnetID)
		}
		ko.Spec.SubnetIDs = ids
	}
	return &resource{ko}, has, nil
}
