// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AccountPasswordPolicySpec sets the AWS account's password policy for IAM
// users (UpdateAccountPasswordPolicy). It is Terraform's
// aws_iam_account_password_policy. An account has one policy, so one
// object per account. A field left out keeps IAM's default.
type AccountPasswordPolicySpec struct {
	// Whether users may change their own password.
	AllowUsersToChangePassword *bool `json:"allowUsersToChangePassword,omitempty"`
	// Whether a user whose password expires must ask an administrator to
	// reset it.
	HardExpiry *bool `json:"hardExpiry,omitempty"`
	// Days a password is valid: 1 to 1095; 0 for no expiry.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1095
	MaxPasswordAge *int64 `json:"maxPasswordAge,omitempty"`
	// The shortest password allowed: 6 to 128.
	// +kubebuilder:validation:Minimum=6
	// +kubebuilder:validation:Maximum=128
	MinimumPasswordLength *int64 `json:"minimumPasswordLength,omitempty"`
	// How many previous passwords a user may not reuse: 1 to 24; 0 for
	// none.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=24
	PasswordReusePrevention    *int64 `json:"passwordReusePrevention,omitempty"`
	RequireLowercaseCharacters *bool  `json:"requireLowercaseCharacters,omitempty"`
	RequireNumbers             *bool  `json:"requireNumbers,omitempty"`
	RequireSymbols             *bool  `json:"requireSymbols,omitempty"`
	RequireUppercaseCharacters *bool  `json:"requireUppercaseCharacters,omitempty"`
}

// AccountPasswordPolicyStatus defines the observed state of
// AccountPasswordPolicy.
type AccountPasswordPolicyStatus struct {
	// +kubebuilder:validation:Optional
	ACKResourceMetadata *ackv1alpha1.ResourceMetadata `json:"ackResourceMetadata"`
	// +kubebuilder:validation:Optional
	Conditions []*ackv1alpha1.Condition `json:"conditions"`
	// Whether passwords expire (a maxPasswordAge is set).
	// +kubebuilder:validation:Optional
	ExpirePasswords *bool `json:"expirePasswords,omitempty"`
}

// AccountPasswordPolicy is the Schema for the AccountPasswordPolicies API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=aws;hfl,shortName=hflaccountpasswordpolicy
// +kubebuilder:printcolumn:name="MINLENGTH",type=integer,JSONPath=`.spec.minimumPasswordLength`
// +kubebuilder:printcolumn:name="MAXAGE",type=integer,JSONPath=`.spec.maxPasswordAge`
// +kubebuilder:printcolumn:name="SYNCED",type="string",priority=0,JSONPath=".status.conditions[?(@.type==\"ACK.ResourceSynced\")].status"
// +kubebuilder:printcolumn:name="AGE",type="date",priority=0,JSONPath=".metadata.creationTimestamp"
type AccountPasswordPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AccountPasswordPolicySpec   `json:"spec,omitempty"`
	Status            AccountPasswordPolicyStatus `json:"status,omitempty"`
}

// AccountPasswordPolicyList contains a list of AccountPasswordPolicy.
// +kubebuilder:object:root=true
type AccountPasswordPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AccountPasswordPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AccountPasswordPolicy{}, &AccountPasswordPolicyList{})
}
