// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	ackec2 "github.com/aws-controllers-k8s/ec2-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DefaultRouteTableSpec takes over the main route table EC2 made with a
// VPC: it is found by the VPC (DescribeRouteTables, association.main=true),
// never created and never deleted. It is Terraform's
// aws_default_route_table.
//
// The routes are exclusive, as Terraform's are: every route the table has
// that the spec does not list is deleted, and the spec's are created. The
// VPC's local route (origin CreateRouteTable) and routes a virtual private
// gateway propagates (origin EnableVgwRoutePropagation) are EC2's and are
// neither listed nor touched. The routes take the shape of ACK's
// RouteTable routes (ec2.services.k8s.aws), references included
// (gatewayRef to ACK's InternetGateway, natGatewayRef, transitGatewayRef,
// vpcEndpointRef, vpcPeeringConnectionRef).
//
// The virtual private gateways listed in propagatingVGWs propagate their
// routes to the table and no other gateway does (exclusive). The tags are
// exclusive too, except AWS's own (aws:).
//
// Deleting the object leaves the table, its routes and propagations as
// they are.
//
// Exactly one of vpcID and vpcRef must be set.
//
// +kubebuilder:validation:XValidation:rule="[has(self.vpcID), has(self.vpcRef)].filter(x, x).size() == 1",message="exactly one of vpcID and vpcRef must be set"
type DefaultRouteTableSpec struct {
	// The ID of the VPC whose main route table this is. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	VPCID *string `json:"vpcID,omitempty"`
	// An ACK VPC (ec2.services.k8s.aws) whose status.vpcID is the VPC.
	VPCRef *ackv1alpha1.AWSResourceReferenceWrapper `json:"vpcRef,omitempty"`
	// The routes; none when empty besides the local route.
	Routes []*ackec2.CreateRouteInput `json:"routes,omitempty"`
	// The IDs of the virtual private gateways that propagate routes to the
	// table.
	PropagatingVGWs []*string `json:"propagatingVGWs,omitempty"`
	// The table's tags.
	Tags []*Tag `json:"tags,omitempty"`
}

// DefaultRouteTableStatus defines the observed state of DefaultRouteTable.
type DefaultRouteTableStatus struct {
	// +kubebuilder:validation:Optional
	ACKResourceMetadata *ackv1alpha1.ResourceMetadata `json:"ackResourceMetadata"`
	// +kubebuilder:validation:Optional
	Conditions []*ackv1alpha1.Condition `json:"conditions"`
	// The ID of the VPC's main route table.
	// +kubebuilder:validation:Optional
	ID *string `json:"id,omitempty"`
}

// DefaultRouteTable is the Schema for the DefaultRouteTables API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=aws;hfl,shortName=hfldefaultroutetable
// +kubebuilder:printcolumn:name="VPC",type=string,JSONPath=`.spec.vpcID`
// +kubebuilder:printcolumn:name="ID",type=string,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="SYNCED",type="string",priority=0,JSONPath=".status.conditions[?(@.type==\"ACK.ResourceSynced\")].status"
// +kubebuilder:printcolumn:name="AGE",type="date",priority=0,JSONPath=".metadata.creationTimestamp"
type DefaultRouteTable struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DefaultRouteTableSpec   `json:"spec,omitempty"`
	Status            DefaultRouteTableStatus `json:"status,omitempty"`
}

// DefaultRouteTableList contains a list of DefaultRouteTable.
// +kubebuilder:object:root=true
type DefaultRouteTableList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DefaultRouteTable `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DefaultRouteTable{}, &DefaultRouteTableList{})
}
