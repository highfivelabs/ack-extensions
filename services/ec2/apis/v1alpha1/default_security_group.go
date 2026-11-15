// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	ackec2 "github.com/aws-controllers-k8s/ec2-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DefaultSecurityGroupSpec takes over the default security group EC2 made
// with a VPC: it is found by the VPC (DescribeSecurityGroups, group-name
// default), never created and never deleted. It is Terraform's
// aws_default_security_group.
//
// The rules are exclusive, as Terraform's are: every rule the group has
// that the spec does not list is revoked, the default egress rule
// (all traffic to 0.0.0.0/0) included, and the spec's rules are
// authorized. A spec with no rules leaves the group with none, the CIS
// benchmark's recommendation for a default group. The rules take the
// shape of ACK's SecurityGroup rules (ec2.services.k8s.aws), without
// groupRef: a rule naming another group gives its groupID. The tags are
// exclusive too, except AWS's own (aws:).
//
// Deleting the object leaves the group and its rules as they are.
//
// Exactly one of vpcID and vpcRef must be set.
//
// +kubebuilder:validation:XValidation:rule="[has(self.vpcID), has(self.vpcRef)].filter(x, x).size() == 1",message="exactly one of vpcID and vpcRef must be set"
type DefaultSecurityGroupSpec struct {
	// The ID of the VPC whose default security group this is. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	VPCID *string `json:"vpcID,omitempty"`
	// An ACK VPC (ec2.services.k8s.aws) whose status.vpcID is the VPC.
	VPCRef *ackv1alpha1.AWSResourceReferenceWrapper `json:"vpcRef,omitempty"`
	// The inbound rules; none when empty.
	IngressRules []*ackec2.IPPermission `json:"ingressRules,omitempty"`
	// The outbound rules; none when empty (EC2's default allow-all egress
	// rule is revoked unless listed).
	EgressRules []*ackec2.IPPermission `json:"egressRules,omitempty"`
	// The group's tags.
	Tags []*Tag `json:"tags,omitempty"`
}

// DefaultSecurityGroupStatus defines the observed state of
// DefaultSecurityGroup.
type DefaultSecurityGroupStatus struct {
	// +kubebuilder:validation:Optional
	ACKResourceMetadata *ackv1alpha1.ResourceMetadata `json:"ackResourceMetadata"`
	// +kubebuilder:validation:Optional
	Conditions []*ackv1alpha1.Condition `json:"conditions"`
	// The ID of the VPC's default security group.
	// +kubebuilder:validation:Optional
	ID *string `json:"id,omitempty"`
	// The group's name (default).
	// +kubebuilder:validation:Optional
	GroupName *string `json:"groupName,omitempty"`
}

// DefaultSecurityGroup is the Schema for the DefaultSecurityGroups API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=aws;hfl,shortName=hfldefaultsecuritygroup
// +kubebuilder:printcolumn:name="VPC",type=string,JSONPath=`.spec.vpcID`
// +kubebuilder:printcolumn:name="ID",type=string,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="SYNCED",type="string",priority=0,JSONPath=".status.conditions[?(@.type==\"ACK.ResourceSynced\")].status"
// +kubebuilder:printcolumn:name="AGE",type="date",priority=0,JSONPath=".metadata.creationTimestamp"
type DefaultSecurityGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DefaultSecurityGroupSpec   `json:"spec,omitempty"`
	Status            DefaultSecurityGroupStatus `json:"status,omitempty"`
}

// DefaultSecurityGroupList contains a list of DefaultSecurityGroup.
// +kubebuilder:object:root=true
type DefaultSecurityGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DefaultSecurityGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DefaultSecurityGroup{}, &DefaultSecurityGroupList{})
}
