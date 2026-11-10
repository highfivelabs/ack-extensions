// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ContextName is the only name an ACKContext may have: there is one per
// namespace, as there is one CloudControlContext.
const ContextName = "ack"

// DefaultSystemNamespace is where the operator runs, and with it every
// environment's managers.
const DefaultSystemNamespace = "ack-extensions-system"

// ManagerName names the manager of one service for one environment: its
// Deployment and its ServiceAccount, in the operator's namespace, so that
// nobody who may create pods in the environment's namespace can run as the
// environment's role. It is the ServiceAccount the environment's EKS Pod
// Identity association for that service names.
func ManagerName(service, environment string) string {
	return "ack-extensions-" + service + "-" + environment
}

// ACKContextSpec is how an environment (a namespace) runs the
// ACK-generated managers: as which AWS role, in which region, and which
// controllers with which kinds.
type ACKContextSpec struct {
	// RoleARN is the environment's IAM role for its ACK managers. The
	// operator runs each enabled controller's manager as its own
	// ServiceAccount, ack-extensions-<service>-<namespace>, in the
	// operator's namespace; one EKS Pod Identity association per manager,
	// made outside the cluster (the operator holds no AWS credentials),
	// binds that ServiceAccount to this role.
	// status.podIdentityAssociations says exactly which associations.
	// The role's permissions are the generated policies of the kinds this
	// context enables (task ack:iam).
	// +kubebuilder:validation:Pattern=`^arn:aws[a-z-]*:iam::[0-9]{12}:role/[\w+=,.@/-]+$`
	RoleARN string `json:"roleARN"`

	// Region is the AWS region the environment's resources are made in.
	// +kubebuilder:validation:Pattern=`^[a-z]{2}(-[a-z]+)+-[0-9]$`
	Region string `json:"region"`

	// Controllers are the services whose managers run for this namespace,
	// one manager each.
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=service
	Controllers []ACKController `json:"controllers"`

	// DeletionPolicy is what deleting an object does to its AWS resource
	// when the object does not say (ACK's services.k8s.aws/deletion-policy
	// annotation): delete (the default) or retain.
	// +optional
	// +kubebuilder:validation:Enum=delete;retain
	DeletionPolicy string `json:"deletionPolicy,omitempty"`
}

// ACKController enables one service's manager in the namespace.
type ACKController struct {
	// Service is the service, as in the API group <service>.aws.hfl.dev:
	// iam, rds, scheduler. organizations is refused: it keeps an
	// organization-scoped install of its own (docs/plan.md, section 7.5).
	// +kubebuilder:validation:Pattern=`^[a-z0-9]+$`
	Service string `json:"service"`

	// Kinds are the kinds of the service the manager serves (OptionGroup,
	// DBProxyTarget). Empty: every kind of the service. An object of a
	// kind not listed is left alone, and the role need not carry its
	// permissions.
	// +optional
	// +listType=set
	Kinds []string `json:"kinds,omitempty"`
}

// PodIdentityAssociation is an EKS Pod Identity association the
// environment needs: this namespace and ServiceAccount, to the role.
type PodIdentityAssociation struct {
	Namespace      string `json:"namespace"`
	ServiceAccount string `json:"serviceAccount"`
	RoleARN        string `json:"roleARN"`
}

// ACKManager is a manager the operator runs for the context.
type ACKManager struct {
	// Service is the controller's service.
	Service string `json:"service"`
	// ServiceAccount is the manager's, namespace/name.
	ServiceAccount string `json:"serviceAccount"`
	// Kinds are the kinds it serves.
	// +optional
	Kinds []string `json:"kinds,omitempty"`
}

// ACKRefusal is a controller the operator does not run for the context,
// and why.
type ACKRefusal struct {
	Service string `json:"service"`
	// Reason: UnknownService, UnknownKinds, OrganizationScoped or
	// NamespaceClaimed.
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// ACKContextStatus is what the operator made of the context.
type ACKContextStatus struct {
	// ObservedGeneration is the generation last acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Managers are the managers that run for the namespace.
	// +optional
	Managers []ACKManager `json:"managers,omitempty"`

	// PodIdentityAssociations are the associations the environment needs,
	// one per manager.
	// +optional
	PodIdentityAssociations []PodIdentityAssociation `json:"podIdentityAssociations,omitempty"`

	// Refused are the controllers the operator does not run, and why.
	// +optional
	Refused []ACKRefusal `json:"refused,omitempty"`

	// Conditions: Ready is true when every enabled controller's manager is
	// rolled out and none is refused.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ACKContext enables ACK-generated managers in its namespace (an
// environment): for each controller it lists, the operator runs one
// manager, in the operator's namespace, watching only this namespace and
// holding Kubernetes rights only here, as its own ServiceAccount bound by
// EKS Pod Identity to the environment's IAM role. It is CloudControlContext
// for the ACK-generated kinds, and Config Connector's
// ConfigConnectorContext (namespaced mode) for AWS. One per namespace,
// named ack.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=aws
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.spec.roleARN`
// +kubebuilder:printcolumn:name="Region",type=string,JSONPath=`.spec.region`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'ack'",message="an ACKContext must be named ack: one per namespace"
type ACKContext struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ACKContextSpec   `json:"spec"`
	Status ACKContextStatus `json:"status,omitempty"`
}

// ACKContextList is a list of ACKContexts.
//
// +kubebuilder:object:root=true
type ACKContextList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ACKContext `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ACKContext{}, &ACKContextList{})
}
