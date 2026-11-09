// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WebACLAssociationSpec associates a regional web ACL with one regional
// resource (AssociateWebACL): an Application Load Balancer, an API Gateway
// REST API stage, an AppSync GraphQL API, a Cognito user pool, an App
// Runner service or a Verified Access instance. It is Terraform's
// aws_wafv2_web_acl_association. A CloudFront distribution names its web
// ACL in its own configuration instead.
//
// Exactly one of webACLARN and webACLRef must be set. Both fields are
// immutable, as in Terraform: a resource has one web ACL, and another
// association is another object.
//
// +kubebuilder:validation:XValidation:rule="[has(self.webACLARN), has(self.webACLRef)].filter(x, x).size() == 1",message="exactly one of webACLARN and webACLRef must be set"
type WebACLAssociationSpec struct {
	// The ARN of the resource the web ACL protects. Immutable.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	ResourceARN *string `json:"resourceARN"`
	// The ARN of the web ACL, of scope REGIONAL. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	WebACLARN *string `json:"webACLARN,omitempty"`
	// An ACK wafv2.services.k8s.aws WebACL whose
	// status.ackResourceMetadata.arn is the web ACL.
	WebACLRef *ackv1alpha1.AWSResourceReferenceWrapper `json:"webACLRef,omitempty"`
}

// WebACLAssociationStatus defines the observed state of WebACLAssociation.
type WebACLAssociationStatus struct {
	// +kubebuilder:validation:Optional
	ACKResourceMetadata *ackv1alpha1.ResourceMetadata `json:"ackResourceMetadata"`
	// +kubebuilder:validation:Optional
	Conditions []*ackv1alpha1.Condition `json:"conditions"`
	// The name of the web ACL the resource is associated with.
	// +kubebuilder:validation:Optional
	WebACLName *string `json:"webACLName,omitempty"`
}

// WebACLAssociation is the Schema for the WebACLAssociations API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=aws;hfl,shortName=hflwebaclassociation
// +kubebuilder:printcolumn:name="RESOURCE",type=string,JSONPath=`.spec.resourceARN`
// +kubebuilder:printcolumn:name="WEBACL",type=string,JSONPath=`.status.webACLName`
// +kubebuilder:printcolumn:name="SYNCED",type="string",priority=0,JSONPath=".status.conditions[?(@.type==\"ACK.ResourceSynced\")].status"
// +kubebuilder:printcolumn:name="AGE",type="date",priority=0,JSONPath=".metadata.creationTimestamp"
type WebACLAssociation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              WebACLAssociationSpec   `json:"spec,omitempty"`
	Status            WebACLAssociationStatus `json:"status,omitempty"`
}

// WebACLAssociationList contains a list of WebACLAssociation.
// +kubebuilder:object:root=true
type WebACLAssociationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WebACLAssociation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WebACLAssociation{}, &WebACLAssociationList{})
}
