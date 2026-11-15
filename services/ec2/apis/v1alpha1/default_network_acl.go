// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	ackec2 "github.com/aws-controllers-k8s/ec2-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DefaultNetworkACLSpec takes over the default network ACL EC2 made with a
// VPC: it is found by the VPC (DescribeNetworkAcls, default=true), never
// created and never deleted. It is Terraform's aws_default_network_acl.
//
// The entries are exclusive, as Terraform's are: every entry the ACL has
// that the spec does not list is deleted, EC2's two default allow-all
// entries (rule 100, and 101 for IPv6) included, and the spec's entries
// are created or replaced. The deny-all entries numbered 32767 are EC2's
// and are neither listed nor touched. The entries take the shape of ACK's
// NetworkACL entries (ec2.services.k8s.aws).
//
// The subnets listed (subnetIDs, or subnetRefs to ACK's Subnet) are
// associated with the default ACL, moved from whatever ACL they had. A
// subnet that is on the default ACL but not listed cannot be moved off it
// from here (EC2 moves a subnet only onto another ACL); it is reported in
// status.unlistedSubnetIDs. The tags are exclusive too, except AWS's own
// (aws:).
//
// Deleting the object leaves the ACL, its entries and its subnets as they
// are.
//
// Exactly one of vpcID and vpcRef must be set.
//
// +kubebuilder:validation:XValidation:rule="[has(self.vpcID), has(self.vpcRef)].filter(x, x).size() == 1",message="exactly one of vpcID and vpcRef must be set"
type DefaultNetworkACLSpec struct {
	// The ID of the VPC whose default network ACL this is. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	VPCID *string `json:"vpcID,omitempty"`
	// An ACK VPC (ec2.services.k8s.aws) whose status.vpcID is the VPC.
	VPCRef *ackv1alpha1.AWSResourceReferenceWrapper `json:"vpcRef,omitempty"`
	// The entries, ingress and egress (egress: true); none when empty
	// besides EC2's deny-all entries (32767).
	Entries []*ackec2.NetworkACLEntry `json:"entries,omitempty"`
	// The subnets to associate with the default ACL.
	SubnetIDs []*string `json:"subnetIDs,omitempty"`
	// ACK Subnets (ec2.services.k8s.aws) whose status.subnetID is
	// associated with the default ACL, beside subnetIDs.
	SubnetRefs []*ackv1alpha1.AWSResourceReferenceWrapper `json:"subnetRefs,omitempty"`
	// The ACL's tags.
	Tags []*Tag `json:"tags,omitempty"`
}

// DefaultNetworkACLStatus defines the observed state of DefaultNetworkACL.
type DefaultNetworkACLStatus struct {
	// +kubebuilder:validation:Optional
	ACKResourceMetadata *ackv1alpha1.ResourceMetadata `json:"ackResourceMetadata"`
	// +kubebuilder:validation:Optional
	Conditions []*ackv1alpha1.Condition `json:"conditions"`
	// The ID of the VPC's default network ACL.
	// +kubebuilder:validation:Optional
	ID *string `json:"id,omitempty"`
	// Subnets on the default ACL that the spec does not list. EC2 cannot
	// take a subnet off the default ACL except by putting it on another.
	// +kubebuilder:validation:Optional
	UnlistedSubnetIDs []*string `json:"unlistedSubnetIDs,omitempty"`
}

// DefaultNetworkACL is the Schema for the DefaultNetworkACLs API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=aws;hfl,shortName=hfldefaultnetworkacl
// +kubebuilder:printcolumn:name="VPC",type=string,JSONPath=`.spec.vpcID`
// +kubebuilder:printcolumn:name="ID",type=string,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="SYNCED",type="string",priority=0,JSONPath=".status.conditions[?(@.type==\"ACK.ResourceSynced\")].status"
// +kubebuilder:printcolumn:name="AGE",type="date",priority=0,JSONPath=".metadata.creationTimestamp"
type DefaultNetworkACL struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DefaultNetworkACLSpec   `json:"spec,omitempty"`
	Status            DefaultNetworkACLStatus `json:"status,omitempty"`
}

// DefaultNetworkACLList contains a list of DefaultNetworkACL.
// +kubebuilder:object:root=true
type DefaultNetworkACLList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DefaultNetworkACL `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DefaultNetworkACL{}, &DefaultNetworkACLList{})
}
