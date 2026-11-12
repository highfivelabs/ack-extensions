// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ResourceTagSpec is one tag on one EC2 resource made elsewhere (by
// another team, by AWS for an EKS node group, by Terraform, or shared
// from another account with RAM): put with CreateTags, read with
// DescribeTags, removed with DeleteTags. It is Terraform's aws_ec2_tag.
//
// It is for resources whose tags nothing else manages. On a resource an
// ACK kind manages, ACK syncs the whole tag set and removes a tag its spec
// does not list, so the two would undo each other: list the tag in that
// kind's spec.tags instead.
type ResourceTagSpec struct {
	// The ID of the EC2 resource: a VPC, subnet, security group, route
	// table, transit gateway attachment, instance, volume, or any other
	// resource CreateTags takes. Immutable.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	ResourceID *string `json:"resourceID"`
	// The tag's key. Immutable: another key is another tag.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	Key *string `json:"key"`
	// The tag's value; may be empty. Changed in place, and put back if
	// changed outside Kubernetes.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=256
	Value *string `json:"value"`
}

// ResourceTagStatus defines the observed state of ResourceTag.
type ResourceTagStatus struct {
	// +kubebuilder:validation:Optional
	ACKResourceMetadata *ackv1alpha1.ResourceMetadata `json:"ackResourceMetadata"`
	// +kubebuilder:validation:Optional
	Conditions []*ackv1alpha1.Condition `json:"conditions"`
	// The type of the tagged resource, as DescribeTags gives it (vpc,
	// subnet, security-group, ...).
	// +kubebuilder:validation:Optional
	ResourceType *string `json:"resourceType,omitempty"`
}

// ResourceTag is the Schema for the ResourceTags API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=aws;hfl,shortName=hflresourcetag
// +kubebuilder:printcolumn:name="RESOURCE",type=string,JSONPath=`.spec.resourceID`
// +kubebuilder:printcolumn:name="KEY",type=string,JSONPath=`.spec.key`
// +kubebuilder:printcolumn:name="VALUE",type=string,JSONPath=`.spec.value`
// +kubebuilder:printcolumn:name="SYNCED",type="string",priority=0,JSONPath=".status.conditions[?(@.type==\"ACK.ResourceSynced\")].status"
// +kubebuilder:printcolumn:name="AGE",type="date",priority=0,JSONPath=".metadata.creationTimestamp"
type ResourceTag struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ResourceTagSpec   `json:"spec,omitempty"`
	Status            ResourceTagStatus `json:"status,omitempty"`
}

// ResourceTagList contains a list of ResourceTag.
// +kubebuilder:object:root=true
type ResourceTagList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ResourceTag `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ResourceTag{}, &ResourceTagList{})
}
