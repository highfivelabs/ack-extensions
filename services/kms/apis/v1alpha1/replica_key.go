// Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//     http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0
//
// Modified by High Five Labs for ack-extensions: copied from
// aws-controllers-k8s/kms-controller pull request #112 (head eb4057e4),
// apis/v1alpha1/replica_key.go. Changes: the generator's header is replaced
// by this note (the kind is written by hand here); the field documentation
// is shortened; Tag and KeyMetadata are ACK kms-controller v1.7.0's own
// types, as the pull request's are; keyID and keyRef are exactly one, and
// keyID is immutable; a short name and print columns as our other kinds
// have.

package v1alpha1

import (
	ackkmsapitypes "github.com/aws-controllers-k8s/kms-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ReplicaKeySpec defines the desired state of ReplicaKey: a replica, in
// another Region, of a multi-Region primary KMS key (ReplicateKey).
//
// +kubebuilder:validation:XValidation:rule="[has(self.keyID), has(self.keyRef)].filter(x, x).size() == 1",message="exactly one of keyID and keyRef must be set"
type ReplicaKeySpec struct {
	// Skips ("bypasses") the key policy lockout safety check. The default value
	// is false. Setting it to true increases the risk that the KMS key becomes
	// unmanageable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	BypassPolicyLockoutSafetyCheck *bool `json:"bypassPolicyLockoutSafetyCheck,omitempty"`
	// A description of the replica key. The description is not a shared
	// property of multi-Region keys; KMS does not synchronize it.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	Description *string `json:"description,omitempty"`
	// The key ID or key ARN of the multi-Region primary key to replicate,
	// for example mrk-1234abcd12ab34cd56ef1234567890ab. The primary key is in
	// the Region this object's manager acts in.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	KeyID *string `json:"keyID,omitempty"`
	// An ACK kms.services.k8s.aws Key whose status.keyID is the primary key.
	KeyRef *ackv1alpha1.AWSResourceReferenceWrapper `json:"keyRef,omitempty"`
	// The replica's key policy. Without one, KMS attaches the default key
	// policy. The key policy is not a shared property of multi-Region keys.
	//
	// Regex Pattern: `^[\u0009\u000A\u000D -ÿ]+$`
	Policy *string `json:"policy,omitempty"`
	// The Region of the replica key, such as us-east-1. It must differ from
	// the primary key's Region and from its other replicas'.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	// +kubebuilder:validation:Required
	ReplicaRegion *string `json:"replicaRegion"`
	// The replica's tags. Tags are not a shared property of multi-Region
	// keys.
	Tags []*ackkmsapitypes.Tag `json:"tags,omitempty"`
}

// ReplicaKeyStatus defines the observed state of ReplicaKey
type ReplicaKeyStatus struct {
	// All CRs managed by ACK have a common `Status.ACKResourceMetadata` member
	// that is used to contain resource sync state, account ownership,
	// constructed ARN for the resource
	// +kubebuilder:validation:Optional
	ACKResourceMetadata *ackv1alpha1.ResourceMetadata `json:"ackResourceMetadata"`
	// All CRs managed by ACK have a common `Status.Conditions` member that
	// contains a collection of `ackv1alpha1.Condition` objects that describe
	// the various terminal states of the CR and its backend AWS service API
	// resource
	// +kubebuilder:validation:Optional
	Conditions []*ackv1alpha1.Condition `json:"conditions"`
	// Displays details about the new replica key, read in its own Region,
	// including its Amazon Resource Name (key ARN) and Key states of KMS keys
	// (https://docs.aws.amazon.com/kms/latest/developerguide/key-state.html).
	// +kubebuilder:validation:Optional
	ReplicaKeyMetadata *ackkmsapitypes.KeyMetadata `json:"replicaKeyMetadata,omitempty"`
}

// ReplicaKey is the Schema for the ReplicaKeys API
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=aws;hfl,shortName=hflreplicakey
// +kubebuilder:printcolumn:name="REPLICA-REGION",type=string,JSONPath=`.spec.replicaRegion`
// +kubebuilder:printcolumn:name="STATE",type=string,JSONPath=`.status.replicaKeyMetadata.keyState`
// +kubebuilder:printcolumn:name="SYNCED",type="string",priority=0,JSONPath=".status.conditions[?(@.type==\"ACK.ResourceSynced\")].status"
// +kubebuilder:printcolumn:name="AGE",type="date",priority=0,JSONPath=".metadata.creationTimestamp"
type ReplicaKey struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ReplicaKeySpec   `json:"spec,omitempty"`
	Status            ReplicaKeyStatus `json:"status,omitempty"`
}

// ReplicaKeyList contains a list of ReplicaKey
// +kubebuilder:object:root=true
type ReplicaKeyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ReplicaKey `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ReplicaKey{}, &ReplicaKeyList{})
}
