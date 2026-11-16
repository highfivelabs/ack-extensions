// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TrafficSourceAttachmentSpec defines the desired state of TrafficSourceAttachment.
//
// Attaches one or more traffic sources (Classic Load Balancer, Application/Network/Gateway
// Load Balancer target group, or VPC Lattice target group) to an Auto Scaling group via
// AttachTrafficSources / DescribeTrafficSources / DetachTrafficSources.
//
// +kubebuilder:validation:XValidation:rule="[has(self.autoScalingGroupName), has(self.autoScalingGroupRef)].filter(x, x).size() == 1",message="exactly one of autoScalingGroupName and autoScalingGroupRef must be set"
type TrafficSourceAttachmentSpec struct {
	// The name of the Auto Scaling group. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	AutoScalingGroupName *string `json:"autoScalingGroupName,omitempty"`
	// An autoscaling.aws.hfl.dev AutoScalingGroup whose spec.name is the group.
	AutoScalingGroupRef *ackv1alpha1.AWSResourceReferenceWrapper `json:"autoScalingGroupRef,omitempty"`
	// If you enable zonal shift with cross-zone disabled load balancers, capacity
	// could become imbalanced across Availability Zones. To skip the validation,
	// specify true.
	SkipZonalShiftValidation *bool `json:"skipZonalShiftValidation,omitempty"`
	// The unique identifiers of one or more traffic sources (up to 10).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=10
	TrafficSources []*TrafficSourceIdentifier `json:"trafficSources"`
}

// TrafficSourceAttachmentStatus defines the observed state of TrafficSourceAttachment.
type TrafficSourceAttachmentStatus struct {
	// +kubebuilder:validation:Optional
	ACKResourceMetadata *ackv1alpha1.ResourceMetadata `json:"ackResourceMetadata"`
	// +kubebuilder:validation:Optional
	Conditions []*ackv1alpha1.Condition `json:"conditions"`
	// Observed state of each attached traffic source from DescribeTrafficSources.
	// +kubebuilder:validation:Optional
	TrafficSources []*TrafficSourceState `json:"trafficSources,omitempty"`
}

// TrafficSourceAttachment is the Schema for the TrafficSourceAttachments API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=aws;hfl,shortName=hfltrafficsourceattachment
// +kubebuilder:printcolumn:name="GROUP",type=string,JSONPath=`.spec.autoScalingGroupName`
// +kubebuilder:printcolumn:name="SYNCED",type="string",priority=0,JSONPath=".status.conditions[?(@.type==\"ACK.ResourceSynced\")].status"
// +kubebuilder:printcolumn:name="AGE",type="date",priority=0,JSONPath=".metadata.creationTimestamp"
type TrafficSourceAttachment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              TrafficSourceAttachmentSpec   `json:"spec,omitempty"`
	Status            TrafficSourceAttachmentStatus `json:"status,omitempty"`
}

// TrafficSourceAttachmentList contains a list of TrafficSourceAttachment.
// +kubebuilder:object:root=true
type TrafficSourceAttachmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TrafficSourceAttachment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TrafficSourceAttachment{}, &TrafficSourceAttachmentList{})
}
