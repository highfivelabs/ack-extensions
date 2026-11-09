// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AccountAliasSpec sets the AWS account's alias (CreateAccountAlias), the
// name in its sign-in URL. It is Terraform's aws_iam_account_alias. An
// account has one alias: a new one replaces the old, so one object per
// account.
type AccountAliasSpec struct {
	// The alias: 3 to 63 lowercase letters, digits and hyphens, not
	// starting or ending with a hyphen. IAM also refuses two hyphens in a
	// row, which this pattern cannot say. Immutable.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	AccountAlias *string `json:"accountAlias"`
}

// AccountAliasStatus defines the observed state of AccountAlias.
type AccountAliasStatus struct {
	// +kubebuilder:validation:Optional
	ACKResourceMetadata *ackv1alpha1.ResourceMetadata `json:"ackResourceMetadata"`
	// +kubebuilder:validation:Optional
	Conditions []*ackv1alpha1.Condition `json:"conditions"`
}

// AccountAlias is the Schema for the AccountAliases API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=aws;hfl,shortName=hflaccountalias
// +kubebuilder:printcolumn:name="ALIAS",type=string,JSONPath=`.spec.accountAlias`
// +kubebuilder:printcolumn:name="SYNCED",type="string",priority=0,JSONPath=".status.conditions[?(@.type==\"ACK.ResourceSynced\")].status"
// +kubebuilder:printcolumn:name="AGE",type="date",priority=0,JSONPath=".metadata.creationTimestamp"
type AccountAlias struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AccountAliasSpec   `json:"spec,omitempty"`
	Status            AccountAliasStatus `json:"status,omitempty"`
}

// AccountAliasList contains a list of AccountAlias.
// +kubebuilder:object:root=true
type AccountAliasList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AccountAlias `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AccountAlias{}, &AccountAliasList{})
}
