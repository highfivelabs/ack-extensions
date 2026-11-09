// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ContextName is the only name a CloudControlContext may have: there is
// one per namespace, as there is one ConfigConnectorContext.
const ContextName = "cloudcontrol"

// DefaultSystemNamespace is where the operator runs, and with it every
// environment's manager.
const DefaultSystemNamespace = "ack-extensions-system"

// ManagerName names an environment's manager: its Deployment and its
// ServiceAccount, in the operator's namespace (as Config Connector runs
// each namespace's manager in cnrm-system), so that nobody who may create
// pods in the environment's namespace can run as the environment's role.
// It is the ServiceAccount the environment's EKS Pod Identity association
// names.
func ManagerName(environment string) string { return "ack-extensions-cloudcontrol-" + environment }

// CloudControlContextSpec is how an environment (a namespace) runs Cloud
// Control: as which AWS role, in which region, and for which kinds.
type CloudControlContextSpec struct {
	// RoleARN is the environment's IAM role. The operator runs the
	// namespace's manager as the ServiceAccount
	// ack-extensions-cloudcontrol-<namespace> in its own namespace, and an
	// EKS Pod Identity association, made outside the cluster (the operator
	// holds no AWS credentials), binds that ServiceAccount to this role.
	// status.podIdentityAssociation says exactly which association.
	// The role's permissions are the generated policies of the kinds this
	// context enables (task cloudcontrol:iam).
	// +kubebuilder:validation:Pattern=`^arn:aws[a-z-]*:iam::[0-9]{12}:role/[\w+=,.@/-]+$`
	RoleARN string `json:"roleARN"`

	// Region is the AWS region the environment's resources are made in.
	// +kubebuilder:validation:Pattern=`^[a-z]{2}(-[a-z]+)+-[0-9]$`
	Region string `json:"region"`

	// Kinds are the Cloud Control kinds the namespace's manager serves, as
	// Kind.service (SAMLProvider.iam, Detector.guardduty). Empty: every
	// kind the manager serves. An object of a kind not listed is left
	// alone, and the role need not carry its permissions.
	// +optional
	// +listType=set
	Kinds []string `json:"kinds,omitempty"`

	// DeletionPolicy is what deleting an object does to its AWS resource
	// when the object does not say (ACK's services.k8s.aws/deletion-policy
	// annotation): delete (the default) or retain.
	// +optional
	// +kubebuilder:validation:Enum=delete;retain
	DeletionPolicy string `json:"deletionPolicy,omitempty"`
}

// PodIdentityAssociation is the EKS Pod Identity association an
// environment needs: this namespace and ServiceAccount, to the role.
type PodIdentityAssociation struct {
	Namespace      string `json:"namespace"`
	ServiceAccount string `json:"serviceAccount"`
	RoleARN        string `json:"roleARN"`
}

// CloudControlContextStatus is what the operator made of the context.
type CloudControlContextStatus struct {
	// ObservedGeneration is the generation last acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ServiceAccount the namespace's manager runs as.
	// +optional
	ServiceAccount string `json:"serviceAccount,omitempty"`

	// PodIdentityAssociation is the association the environment needs.
	// +optional
	PodIdentityAssociation *PodIdentityAssociation `json:"podIdentityAssociation,omitempty"`

	// Kinds are the kinds served, as Kind.service.
	// +optional
	Kinds []string `json:"kinds,omitempty"`

	// Conditions: Ready is true when the manager is rolled out.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// CloudControlContext enables Cloud Control in its namespace (an
// environment): the operator runs a manager for it, watching only that
// namespace and holding Kubernetes rights only there, as the environment's
// own ServiceAccount, bound by EKS Pod Identity to the environment's IAM
// role, serving the Cloud Control kinds the context enables. It is Config
// Connector's ConfigConnectorContext (namespaced mode) for AWS. One per
// namespace, named cloudcontrol.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=aws;cloudcontrol
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.spec.roleARN`
// +kubebuilder:printcolumn:name="Region",type=string,JSONPath=`.spec.region`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'cloudcontrol'",message="a CloudControlContext must be named cloudcontrol: one per namespace"
type CloudControlContext struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CloudControlContextSpec   `json:"spec"`
	Status CloudControlContextStatus `json:"status,omitempty"`
}

// CloudControlContextList is a list of CloudControlContexts.
//
// +kubebuilder:object:root=true
type CloudControlContextList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudControlContext `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CloudControlContext{}, &CloudControlContextList{})
}
