// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VolumeAttachmentSpec attaches one EBS volume to one EC2 instance, as a
// device (AttachVolume), and detaches it on delete (DetachVolume). It is
// Terraform's aws_volume_attachment.
//
// Exactly one of volumeID and volumeRef must be set. Every field is
// immutable: an attachment is identified by its volume, instance and
// device.
//
// +kubebuilder:validation:XValidation:rule="[has(self.volumeID), has(self.volumeRef)].filter(x, x).size() == 1",message="exactly one of volumeID and volumeRef must be set"
type VolumeAttachmentSpec struct {
	// The ID of the EBS volume. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	VolumeID *string `json:"volumeID,omitempty"`
	// An ec2.aws.hfl.dev Volume whose status.volumeID is the volume.
	VolumeRef *ackv1alpha1.AWSResourceReferenceWrapper `json:"volumeRef,omitempty"`
	// The ID of the EC2 instance, in the volume's Availability Zone.
	// Immutable.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	InstanceID *string `json:"instanceID"`
	// The device name the instance sees, for example /dev/sdf or
	// /dev/xvdf. Immutable.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	DeviceName *string `json:"deviceName"`
}

// VolumeAttachmentStatus defines the observed state of VolumeAttachment.
type VolumeAttachmentStatus struct {
	// +kubebuilder:validation:Optional
	ACKResourceMetadata *ackv1alpha1.ResourceMetadata `json:"ackResourceMetadata"`
	// +kubebuilder:validation:Optional
	Conditions []*ackv1alpha1.Condition `json:"conditions"`
	// EC2's state of the attachment: attaching, attached, detaching or busy.
	// +kubebuilder:validation:Optional
	State *string `json:"state,omitempty"`
	// When the volume was attached.
	// +kubebuilder:validation:Optional
	AttachTime *metav1.Time `json:"attachTime,omitempty"`
	// Whether EC2 deletes the volume when the instance terminates.
	// +kubebuilder:validation:Optional
	DeleteOnTermination *bool `json:"deleteOnTermination,omitempty"`
}

// VolumeAttachment is the Schema for the VolumeAttachments API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=aws;hfl,shortName=hflvolumeattachment
// +kubebuilder:printcolumn:name="VOLUME",type=string,JSONPath=`.spec.volumeID`
// +kubebuilder:printcolumn:name="INSTANCE",type=string,JSONPath=`.spec.instanceID`
// +kubebuilder:printcolumn:name="DEVICE",type=string,JSONPath=`.spec.deviceName`
// +kubebuilder:printcolumn:name="STATE",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="SYNCED",type="string",priority=0,JSONPath=".status.conditions[?(@.type==\"ACK.ResourceSynced\")].status"
// +kubebuilder:printcolumn:name="AGE",type="date",priority=0,JSONPath=".metadata.creationTimestamp"
type VolumeAttachment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              VolumeAttachmentSpec   `json:"spec,omitempty"`
	Status            VolumeAttachmentStatus `json:"status,omitempty"`
}

// VolumeAttachmentList contains a list of VolumeAttachment.
// +kubebuilder:object:root=true
type VolumeAttachmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VolumeAttachment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VolumeAttachment{}, &VolumeAttachmentList{})
}
