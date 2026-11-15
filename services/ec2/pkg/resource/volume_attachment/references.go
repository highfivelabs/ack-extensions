// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package volume_attachment

import (
	"context"
	"fmt"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
)

// volumeRef names our own Volume (ec2.aws.hfl.dev) and instanceRef ACK's
// Instance (ec2.services.k8s.aws; apis/v1alpha1/ack_kinds.go), resolved the
// way ACK's generated references.go resolves a reference: the referenced
// object must be synced and not terminal, and the field read from it must
// be set.

// +kubebuilder:rbac:groups=ec2.aws.hfl.dev,resources=volumes,verbs=get;list;watch
// +kubebuilder:rbac:groups=ec2.services.k8s.aws,resources=instances,verbs=get;list;watch

func (rm *resourceManager) ClearResolvedReferences(res acktypes.AWSResource) acktypes.AWSResource {
	ko := rm.concreteResource(res).ko.DeepCopy()
	if ko.Spec.VolumeRef != nil {
		ko.Spec.VolumeID = nil
	}
	if ko.Spec.InstanceRef != nil {
		ko.Spec.InstanceID = nil
	}
	return &resource{ko}
}

func (rm *resourceManager) ResolveReferences(
	ctx context.Context,
	apiReader client.Reader,
	res acktypes.AWSResource,
) (acktypes.AWSResource, bool, error) {
	ko := rm.concreteResource(res).ko.DeepCopy()
	has := ko.Spec.VolumeRef != nil || ko.Spec.InstanceRef != nil
	if ko.Spec.VolumeRef != nil && ko.Spec.VolumeID != nil {
		return &resource{ko}, has, ackerr.ResourceReferenceAndIDNotSupportedFor("VolumeID", "VolumeRef")
	}
	if ko.Spec.InstanceRef != nil && ko.Spec.InstanceID != nil {
		return &resource{ko}, has, ackerr.ResourceReferenceAndIDNotSupportedFor("InstanceID", "InstanceRef")
	}
	if ref := ko.Spec.VolumeRef; ref != nil && ref.From != nil {
		obj := &svcapitypes.Volume{}
		if err := getReferenced(ctx, apiReader, ko.GetNamespace(), ref, "VolumeRef", "Volume", obj, &obj.Status.Conditions); err != nil {
			return &resource{ko}, has, err
		}
		if obj.Status.VolumeID == nil {
			return &resource{ko}, has, ackerr.ResourceReferenceMissingTargetFieldFor("Volume", obj.Namespace, obj.Name, "Status.VolumeID")
		}
		ko.Spec.VolumeID = obj.Status.VolumeID
	}
	if ref := ko.Spec.InstanceRef; ref != nil && ref.From != nil {
		obj := &svcapitypes.Instance{}
		if err := getReferenced(ctx, apiReader, ko.GetNamespace(), ref, "InstanceRef", "Instance", obj, &obj.Status.Conditions); err != nil {
			return &resource{ko}, has, err
		}
		if obj.Status.InstanceID == nil {
			return &resource{ko}, has, ackerr.ResourceReferenceMissingTargetFieldFor("Instance", obj.Namespace, obj.Name, "Status.InstanceID")
		}
		ko.Spec.InstanceID = obj.Status.InstanceID
	}
	return &resource{ko}, has, nil
}

// getReferenced reads the object a reference names (in the referencing
// object's namespace unless the reference names another) into obj, and
// fails unless it is synced and not terminal.
func getReferenced(
	ctx context.Context,
	apiReader client.Reader,
	ownNamespace string,
	ref *ackv1alpha1.AWSResourceReferenceWrapper,
	field, kind string,
	obj client.Object,
	conditions *[]*ackv1alpha1.Condition,
) error {
	if ref.From.Name == nil || *ref.From.Name == "" {
		return fmt.Errorf("provided resource reference is nil or empty: %s", field)
	}
	namespace := ownNamespace
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
