// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DefaultVPCSpec takes over the region's default VPC (isDefault=true): it
// is found, never created and never deleted, and takes no VPC ID or
// reference (the region, from the object's region annotation or the
// manager's, names it). It is Terraform's aws_default_vpc. A region with
// no default VPC is retried (ACK.Recoverable) until one is made
// (CreateDefaultVpc, outside Kubernetes).
//
// It manages the VPC's DNS settings, its Network Address Usage metrics
// setting and its tags (exclusive, except AWS's own aws: tags). A setting
// left out of the spec is not managed, except the two DNS settings, which
// default to true as Terraform's do (EC2's own default for a default VPC).
//
// Deleting the object leaves the VPC as it is.
type DefaultVPCSpec struct {
	// Whether the Amazon DNS server answers in the VPC.
	// +kubebuilder:default=true
	EnableDNSSupport *bool `json:"enableDNSSupport,omitempty"`
	// Whether instances launched in the VPC get public DNS hostnames.
	// +kubebuilder:default=true
	EnableDNSHostnames *bool `json:"enableDNSHostnames,omitempty"`
	// Whether Network Address Usage metrics are on for the VPC; not
	// managed when unset.
	EnableNetworkAddressUsageMetrics *bool `json:"enableNetworkAddressUsageMetrics,omitempty"`
	// The VPC's tags.
	Tags []*Tag `json:"tags,omitempty"`
}

// DefaultVPCStatus defines the observed state of DefaultVPC.
type DefaultVPCStatus struct {
	// +kubebuilder:validation:Optional
	ACKResourceMetadata *ackv1alpha1.ResourceMetadata `json:"ackResourceMetadata"`
	// +kubebuilder:validation:Optional
	Conditions []*ackv1alpha1.Condition `json:"conditions"`
	// The ID of the region's default VPC.
	// +kubebuilder:validation:Optional
	VPCID *string `json:"vpcID,omitempty"`
	// The VPC's primary IPv4 CIDR block.
	// +kubebuilder:validation:Optional
	CIDRBlock *string `json:"cidrBlock,omitempty"`
	// The VPC's DHCP options set.
	// +kubebuilder:validation:Optional
	DHCPOptionsID *string `json:"dhcpOptionsID,omitempty"`
	// The account that owns the VPC.
	// +kubebuilder:validation:Optional
	OwnerID *string `json:"ownerID,omitempty"`
}

// DefaultVPC is the Schema for the DefaultVPCs API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=aws;hfl,shortName=hfldefaultvpc
// +kubebuilder:printcolumn:name="VPC",type=string,JSONPath=`.status.vpcID`
// +kubebuilder:printcolumn:name="CIDR",type=string,JSONPath=`.status.cidrBlock`
// +kubebuilder:printcolumn:name="SYNCED",type="string",priority=0,JSONPath=".status.conditions[?(@.type==\"ACK.ResourceSynced\")].status"
// +kubebuilder:printcolumn:name="AGE",type="date",priority=0,JSONPath=".metadata.creationTimestamp"
type DefaultVPC struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DefaultVPCSpec   `json:"spec,omitempty"`
	Status            DefaultVPCStatus `json:"status,omitempty"`
}

// DefaultVPCList contains a list of DefaultVPC.
// +kubebuilder:object:root=true
type DefaultVPCList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DefaultVPC `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DefaultVPC{}, &DefaultVPCList{})
}
