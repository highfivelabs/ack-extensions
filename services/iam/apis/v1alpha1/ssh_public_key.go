// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SSHPublicKeySpec uploads an SSH public key for an IAM user
// (UploadSSHPublicKey), for AWS CodeCommit, and sets it Active or Inactive
// (UpdateSSHPublicKey). It is Terraform's aws_iam_user_ssh_key.
//
// +kubebuilder:validation:XValidation:rule="[has(self.userName), has(self.userRef)].filter(x, x).size() == 1",message="exactly one of userName and userRef must be set"
type SSHPublicKeySpec struct {
	// The name of the IAM user. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	UserName *string `json:"userName,omitempty"`
	// An ACK iam.services.k8s.aws User whose spec.name is the user.
	UserRef *ackv1alpha1.AWSResourceReferenceWrapper `json:"userRef,omitempty"`
	// The public key, in ssh-rsa or PEM form. Immutable: another key is
	// another object.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	SSHPublicKeyBody *string `json:"sshPublicKeyBody"`
	// Active (the default) or Inactive.
	// +kubebuilder:validation:Enum=Active;Inactive
	Status *string `json:"status,omitempty"`
}

// SSHPublicKeyStatus defines the observed state of SSHPublicKey.
type SSHPublicKeyStatus struct {
	// +kubebuilder:validation:Optional
	ACKResourceMetadata *ackv1alpha1.ResourceMetadata `json:"ackResourceMetadata"`
	// +kubebuilder:validation:Optional
	Conditions []*ackv1alpha1.Condition `json:"conditions"`
	// The key's ID, which CodeCommit and SSH configuration name it by.
	// +kubebuilder:validation:Optional
	SSHPublicKeyID *string `json:"sshPublicKeyID,omitempty"`
	// The key's MD5 fingerprint.
	// +kubebuilder:validation:Optional
	Fingerprint *string `json:"fingerprint,omitempty"`
	// When the key was uploaded.
	// +kubebuilder:validation:Optional
	UploadDate *metav1.Time `json:"uploadDate,omitempty"`
}

// SSHPublicKey is the Schema for the SSHPublicKeys API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=aws;hfl,shortName=hflsshpublickey
// +kubebuilder:printcolumn:name="USER",type=string,JSONPath=`.spec.userName`
// +kubebuilder:printcolumn:name="ID",type=string,JSONPath=`.status.sshPublicKeyID`
// +kubebuilder:printcolumn:name="STATUS",type=string,JSONPath=`.spec.status`
// +kubebuilder:printcolumn:name="SYNCED",type="string",priority=0,JSONPath=".status.conditions[?(@.type==\"ACK.ResourceSynced\")].status"
// +kubebuilder:printcolumn:name="AGE",type="date",priority=0,JSONPath=".metadata.creationTimestamp"
type SSHPublicKey struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SSHPublicKeySpec   `json:"spec,omitempty"`
	Status            SSHPublicKeyStatus `json:"status,omitempty"`
}

// SSHPublicKeyList contains a list of SSHPublicKey.
// +kubebuilder:object:root=true
type SSHPublicKeyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SSHPublicKey `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SSHPublicKey{}, &SSHPublicKeyList{})
}
