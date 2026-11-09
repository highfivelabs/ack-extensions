// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PermissionSpec is one statement of a Lambda function's resource-based
// policy (AddPermission): it lets a service or account invoke the
// function, as S3 notifications, an ALB target group, EventBridge rules
// and SNS subscriptions need. The fields after functionName and qualifier
// are those of ACK's own AddPermissionInput (lambda-controller v1.19.0,
// Alias.spec.permissions). Lambda cannot change a statement, so every
// field is immutable.
//
// +kubebuilder:validation:XValidation:rule="[has(self.functionName), has(self.functionRef)].filter(x, x).size() == 1",message="exactly one of functionName and functionRef must be set"
type PermissionSpec struct {
	// The function's name, ARN or partial ARN. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	FunctionName *string `json:"functionName,omitempty"`
	// An ACK lambda.services.k8s.aws Function whose spec.name is the
	// function.
	FunctionRef *ackv1alpha1.AWSResourceReferenceWrapper `json:"functionRef,omitempty"`
	// A version or alias, to add the statement to that version's or
	// alias's policy instead of the function's. Do not name an alias that
	// an ACK Alias manages: ACK's Alias owns that alias's whole policy and
	// removes statements it does not list (use its spec.permissions).
	// Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	Qualifier *string `json:"qualifier,omitempty"`
	// The statement's identifier, unique in the policy. Immutable.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9-_]+$`
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	StatementID *string `json:"statementID"`
	// The action the principal may use: lambda:InvokeFunction or
	// lambda:GetFunction, for example. Immutable.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	Action *string `json:"action"`
	// The service (s3.amazonaws.com, elasticloadbalancing.amazonaws.com,
	// events.amazonaws.com, sns.amazonaws.com) or account that may invoke
	// the function. Immutable.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	Principal *string `json:"principal"`
	// For a service principal: the ARN of the resource that may invoke the
	// function (the bucket, target group, rule or topic). Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	SourceARN *string `json:"sourceARN,omitempty"`
	// For a service principal: the account that owns the resource.
	// Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	SourceAccount *string `json:"sourceAccount,omitempty"`
	// For an Alexa Smart Home function: the event source token. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	EventSourceToken *string `json:"eventSourceToken,omitempty"`
	// The organization whose accounts may invoke the function. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	PrincipalOrgID *string `json:"principalOrgID,omitempty"`
	// For lambda:InvokeFunctionUrl: the function URL's auth type.
	// Immutable.
	// +kubebuilder:validation:Enum=NONE;AWS_IAM
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	FunctionURLAuthType *string `json:"functionURLAuthType,omitempty"`
}

// PermissionStatus defines the observed state of Permission.
type PermissionStatus struct {
	// +kubebuilder:validation:Optional
	ACKResourceMetadata *ackv1alpha1.ResourceMetadata `json:"ackResourceMetadata"`
	// +kubebuilder:validation:Optional
	Conditions []*ackv1alpha1.Condition `json:"conditions"`
	// The statement as Lambda stores it in the policy (GetPolicy), JSON.
	// +kubebuilder:validation:Optional
	Statement *string `json:"statement,omitempty"`
}

// Permission is the Schema for the Permissions API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=aws;hfl,shortName=hfllambdapermission
// +kubebuilder:printcolumn:name="FUNCTION",type=string,JSONPath=`.spec.functionName`
// +kubebuilder:printcolumn:name="PRINCIPAL",type=string,JSONPath=`.spec.principal`
// +kubebuilder:printcolumn:name="SYNCED",type="string",priority=0,JSONPath=".status.conditions[?(@.type==\"ACK.ResourceSynced\")].status"
// +kubebuilder:printcolumn:name="AGE",type="date",priority=0,JSONPath=".metadata.creationTimestamp"
type Permission struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PermissionSpec   `json:"spec,omitempty"`
	Status            PermissionStatus `json:"status,omitempty"`
}

// PermissionList contains a list of Permission.
// +kubebuilder:object:root=true
type PermissionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Permission `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Permission{}, &PermissionList{})
}
