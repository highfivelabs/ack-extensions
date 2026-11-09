// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package web_acl_association

import (
	"context"
	"fmt"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	ackwafv2apitypes "github.com/aws-controllers-k8s/wafv2-controller/apis/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// webACLRef names ACK's own WebACL (wafv2.services.k8s.aws), resolved the
// way ACK's generated references.go resolves a reference to another
// controller's kind: the referenced object must be synced and not
// terminal, and the field read from it must be set.

// +kubebuilder:rbac:groups=wafv2.services.k8s.aws,resources=webacls,verbs=get;list;watch

func (rm *resourceManager) ClearResolvedReferences(res acktypes.AWSResource) acktypes.AWSResource {
	ko := rm.concreteResource(res).ko.DeepCopy()
	if ko.Spec.WebACLRef != nil {
		ko.Spec.WebACLARN = nil
	}
	return &resource{ko}
}

func (rm *resourceManager) ResolveReferences(
	ctx context.Context,
	apiReader client.Reader,
	res acktypes.AWSResource,
) (acktypes.AWSResource, bool, error) {
	ko := rm.concreteResource(res).ko
	has := ko.Spec.WebACLRef != nil
	if ko.Spec.WebACLRef != nil && ko.Spec.WebACLARN != nil {
		return &resource{ko}, has, ackerr.ResourceReferenceAndIDNotSupportedFor("WebACLARN", "WebACLRef")
	}
	ref := ko.Spec.WebACLRef
	if ref == nil || ref.From == nil {
		return &resource{ko}, has, nil
	}
	if ref.From.Name == nil || *ref.From.Name == "" {
		return &resource{ko}, has, fmt.Errorf("provided resource reference is nil or empty: WebACLRef")
	}
	namespace := ko.GetNamespace()
	if ref.From.Namespace != nil && *ref.From.Namespace != "" {
		namespace = *ref.From.Namespace
	}
	name := *ref.From.Name
	obj := &ackwafv2apitypes.WebACL{}
	if err := apiReader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, obj); err != nil {
		return &resource{ko}, has, err
	}
	synced := false
	for _, cond := range obj.Status.Conditions {
		if cond.Type == ackv1alpha1.ConditionTypeTerminal && cond.Status == corev1.ConditionTrue {
			return &resource{ko}, has, ackerr.ResourceReferenceTerminalFor("WebACL", namespace, name)
		}
		if cond.Type == ackv1alpha1.ConditionTypeResourceSynced && cond.Status == corev1.ConditionTrue {
			synced = true
		}
	}
	if !synced {
		return &resource{ko}, has, ackerr.ResourceReferenceNotSyncedFor("WebACL", namespace, name)
	}
	if obj.Status.ACKResourceMetadata == nil || obj.Status.ACKResourceMetadata.ARN == nil {
		return &resource{ko}, has, ackerr.ResourceReferenceMissingTargetFieldFor("WebACL", namespace, name, "Status.ACKResourceMetadata.ARN")
	}
	arn := string(*obj.Status.ACKResourceMetadata.ARN)
	ko.Spec.WebACLARN = &arn
	return &resource{ko}, has, nil
}
