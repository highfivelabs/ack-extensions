// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DBProxyTargetSpec registers one RDS DB instance or one Aurora DB cluster
// as a target of an RDS Proxy's target group (RegisterDBProxyTargets).
//
// ACK's DBProxy creates the proxy but never registers what it fronts, so
// without a target a proxy accepts no connections. Exactly one of the
// instance and cluster fields (or their references) must be set.
//
// +kubebuilder:validation:XValidation:rule="[has(self.dbInstanceIdentifier), has(self.dbInstanceRef), has(self.dbClusterIdentifier), has(self.dbClusterRef)].filter(x, x).size() == 1",message="exactly one of dbInstanceIdentifier, dbInstanceRef, dbClusterIdentifier and dbClusterRef must be set"
// +kubebuilder:validation:XValidation:rule="[has(self.dbProxyName), has(self.dbProxyRef)].filter(x, x).size() == 1",message="exactly one of dbProxyName and dbProxyRef must be set"
type DBProxyTargetSpec struct {
	// The name of the DB proxy. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	DBProxyName *string `json:"dbProxyName,omitempty"`
	// An ACK rds.services.k8s.aws DBProxy whose spec.name is the proxy.
	DBProxyRef *ackv1alpha1.AWSResourceReferenceWrapper `json:"dbProxyRef,omitempty"`
	// The proxy's target group. RDS Proxy has exactly one, "default", and
	// that is the default here. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	TargetGroupName *string `json:"targetGroupName,omitempty"`
	// The identifier of an RDS DB instance to register. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	DBInstanceIdentifier *string `json:"dbInstanceIdentifier,omitempty"`
	// An ACK rds.services.k8s.aws DBInstance whose
	// spec.dbInstanceIdentifier is the instance.
	DBInstanceRef *ackv1alpha1.AWSResourceReferenceWrapper `json:"dbInstanceRef,omitempty"`
	// The identifier of an Aurora DB cluster to register. RDS Proxy then
	// tracks the cluster's writer and readers itself. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	DBClusterIdentifier *string `json:"dbClusterIdentifier,omitempty"`
	// An ACK rds.services.k8s.aws DBCluster whose spec.dbClusterIdentifier
	// is the cluster.
	DBClusterRef *ackv1alpha1.AWSResourceReferenceWrapper `json:"dbClusterRef,omitempty"`
}

// DBProxyTargetHealth is RDS Proxy's view of whether it can reach a
// target.
type DBProxyTargetHealth struct {
	// REGISTERING, AVAILABLE or UNAVAILABLE.
	State       *string `json:"state,omitempty"`
	Reason      *string `json:"reason,omitempty"`
	Description *string `json:"description,omitempty"`
}

// DBProxyTargetObservation is one target RDS Proxy reports for this
// registration: the instance, or for a cluster the tracked cluster and
// each of its instances.
type DBProxyTargetObservation struct {
	Endpoint      *string              `json:"endpoint,omitempty"`
	Port          *int64               `json:"port,omitempty"`
	RDSResourceID *string              `json:"rdsResourceID,omitempty"`
	Role          *string              `json:"role,omitempty"`
	TargetARN     *string              `json:"targetARN,omitempty"`
	TargetHealth  *DBProxyTargetHealth `json:"targetHealth,omitempty"`
	// RDS_INSTANCE, RDS_SERVERLESS_ENDPOINT or TRACKED_CLUSTER.
	Type *string `json:"type,omitempty"`
}

// DBProxyTargetStatus defines the observed state of DBProxyTarget.
type DBProxyTargetStatus struct {
	// +kubebuilder:validation:Optional
	ACKResourceMetadata *ackv1alpha1.ResourceMetadata `json:"ackResourceMetadata"`
	// +kubebuilder:validation:Optional
	Conditions []*ackv1alpha1.Condition `json:"conditions"`
	// The targets RDS Proxy reports for this registration.
	// +kubebuilder:validation:Optional
	Targets []*DBProxyTargetObservation `json:"targets,omitempty"`
}

// DBProxyTarget is the Schema for the DBProxyTargets API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=aws;hfl,shortName=hfldbproxytarget
// +kubebuilder:printcolumn:name="PROXY",type=string,JSONPath=`.spec.dbProxyName`
// +kubebuilder:printcolumn:name="HEALTH",type=string,JSONPath=`.status.targets[0].targetHealth.state`
// +kubebuilder:printcolumn:name="SYNCED",type="string",priority=0,JSONPath=".status.conditions[?(@.type==\"ACK.ResourceSynced\")].status"
// +kubebuilder:printcolumn:name="AGE",type="date",priority=0,JSONPath=".metadata.creationTimestamp"
type DBProxyTarget struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DBProxyTargetSpec   `json:"spec,omitempty"`
	Status            DBProxyTargetStatus `json:"status,omitempty"`
}

// DBProxyTargetList contains a list of DBProxyTarget.
// +kubebuilder:object:root=true
type DBProxyTargetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DBProxyTarget `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DBProxyTarget{}, &DBProxyTargetList{})
}
