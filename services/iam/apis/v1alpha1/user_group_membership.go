// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// UserGroupMembershipSpec puts one IAM user in a list of groups
// (AddUserToGroup) and takes it out of them (RemoveUserFromGroup). It is
// Terraform's aws_iam_user_group_membership.
//
// It is not exclusive: only the listed groups are added or removed. Groups
// the user is in for any other reason (another object, the console) are
// left alone, so two writers do not undo each other. A group taken off the
// list is removed; status.groups remembers what was put.
//
// Exactly one of userName and userRef must be set, and exactly one of
// groups and groupRefs.
//
// +kubebuilder:validation:XValidation:rule="[has(self.userName), has(self.userRef)].filter(x, x).size() == 1",message="exactly one of userName and userRef must be set"
// +kubebuilder:validation:XValidation:rule="[has(self.groups), has(self.groupRefs)].filter(x, x).size() == 1",message="exactly one of groups and groupRefs must be set"
type UserGroupMembershipSpec struct {
	// The name of the IAM user. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	UserName *string `json:"userName,omitempty"`
	// An ACK iam.services.k8s.aws User whose spec.name is the user.
	UserRef *ackv1alpha1.AWSResourceReferenceWrapper `json:"userRef,omitempty"`
	// The names of the groups to put the user in.
	// +kubebuilder:validation:MinItems=1
	Groups []*string `json:"groups,omitempty"`
	// ACK iam.services.k8s.aws Groups whose spec.name are the groups.
	// +kubebuilder:validation:MinItems=1
	GroupRefs []*ackv1alpha1.AWSResourceReferenceWrapper `json:"groupRefs,omitempty"`
}

// UserGroupMembershipStatus defines the observed state of
// UserGroupMembership.
type UserGroupMembershipStatus struct {
	// +kubebuilder:validation:Optional
	ACKResourceMetadata *ackv1alpha1.ResourceMetadata `json:"ackResourceMetadata"`
	// +kubebuilder:validation:Optional
	Conditions []*ackv1alpha1.Condition `json:"conditions"`
	// The groups this object last put the user in: a group taken off
	// spec.groups is removed only if it is here.
	// +kubebuilder:validation:Optional
	Groups []*string `json:"groups,omitempty"`
}

// UserGroupMembership is the Schema for the UserGroupMemberships API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=aws;hfl,shortName=hflusergroupmembership
// +kubebuilder:printcolumn:name="USER",type=string,JSONPath=`.spec.userName`
// +kubebuilder:printcolumn:name="GROUPS",type=string,JSONPath=`.status.groups`
// +kubebuilder:printcolumn:name="SYNCED",type="string",priority=0,JSONPath=".status.conditions[?(@.type==\"ACK.ResourceSynced\")].status"
// +kubebuilder:printcolumn:name="AGE",type="date",priority=0,JSONPath=".metadata.creationTimestamp"
type UserGroupMembership struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              UserGroupMembershipSpec   `json:"spec,omitempty"`
	Status            UserGroupMembershipStatus `json:"status,omitempty"`
}

// UserGroupMembershipList contains a list of UserGroupMembership.
// +kubebuilder:object:root=true
type UserGroupMembershipList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []UserGroupMembership `json:"items"`
}

func init() {
	SchemeBuilder.Register(&UserGroupMembership{}, &UserGroupMembershipList{})
}
