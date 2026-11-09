// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DBRoleAssociationSpec associates one IAM role with one Aurora DB cluster
// (AddRoleToDBCluster) or one RDS DB instance (AddRoleToDBInstance), for
// one feature: Aurora's S3 import and export and Lambda calls, RDS for
// PostgreSQL's s3Import and s3Export, SQL Server's and Oracle's
// S3_INTEGRATION, and so on.
//
// ACK's DBCluster and DBInstance report associated roles in their status
// but cannot add one. Exactly one of the cluster and instance fields (or
// their references) must be set, and exactly one of roleARN and roleRef.
// An instance association needs a featureName; a cluster association may
// leave it out (Aurora MySQL names the role in a cluster parameter
// instead).
//
// +kubebuilder:validation:XValidation:rule="[has(self.dbClusterIdentifier), has(self.dbClusterRef), has(self.dbInstanceIdentifier), has(self.dbInstanceRef)].filter(x, x).size() == 1",message="exactly one of dbClusterIdentifier, dbClusterRef, dbInstanceIdentifier and dbInstanceRef must be set"
// +kubebuilder:validation:XValidation:rule="[has(self.roleARN), has(self.roleRef)].filter(x, x).size() == 1",message="exactly one of roleARN and roleRef must be set"
// +kubebuilder:validation:XValidation:rule="!(has(self.dbInstanceIdentifier) || has(self.dbInstanceRef)) || has(self.featureName)",message="featureName is required for a DB instance"
// +kubebuilder:validation:XValidation:rule="has(self.featureName) == has(oldSelf.featureName)",message="featureName is immutable once set"
type DBRoleAssociationSpec struct {
	// The identifier of an Aurora DB cluster. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	DBClusterIdentifier *string `json:"dbClusterIdentifier,omitempty"`
	// An ACK rds.services.k8s.aws DBCluster whose spec.dbClusterIdentifier
	// is the cluster.
	DBClusterRef *ackv1alpha1.AWSResourceReferenceWrapper `json:"dbClusterRef,omitempty"`
	// The identifier of an RDS DB instance. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	DBInstanceIdentifier *string `json:"dbInstanceIdentifier,omitempty"`
	// An ACK rds.services.k8s.aws DBInstance whose
	// spec.dbInstanceIdentifier is the instance.
	DBInstanceRef *ackv1alpha1.AWSResourceReferenceWrapper `json:"dbInstanceRef,omitempty"`
	// The ARN of the IAM role. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	RoleARN *string `json:"roleARN,omitempty"`
	// An ACK iam.services.k8s.aws Role whose status.ackResourceMetadata.arn
	// is the role.
	RoleRef *ackv1alpha1.AWSResourceReferenceWrapper `json:"roleRef,omitempty"`
	// The feature the role is for, as RDS names it for the engine
	// (DescribeDBEngineVersions, SupportedFeatureNames): for example
	// s3Import, s3Export, Lambda, S3_INTEGRATION. Required for a DB
	// instance. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	FeatureName *string `json:"featureName,omitempty"`
}

// DBRoleAssociationStatus defines the observed state of DBRoleAssociation.
type DBRoleAssociationStatus struct {
	// +kubebuilder:validation:Optional
	ACKResourceMetadata *ackv1alpha1.ResourceMetadata `json:"ackResourceMetadata"`
	// +kubebuilder:validation:Optional
	Conditions []*ackv1alpha1.Condition `json:"conditions"`
	// RDS's state of the association: PENDING, ACTIVE, or INVALID (RDS
	// cannot assume the role).
	// +kubebuilder:validation:Optional
	Status *string `json:"status,omitempty"`
}

// DBRoleAssociation is the Schema for the DBRoleAssociations API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=aws;hfl,shortName=hfldbroleassociation
// +kubebuilder:printcolumn:name="CLUSTER",type=string,JSONPath=`.spec.dbClusterIdentifier`
// +kubebuilder:printcolumn:name="INSTANCE",type=string,JSONPath=`.spec.dbInstanceIdentifier`
// +kubebuilder:printcolumn:name="FEATURE",type=string,JSONPath=`.spec.featureName`
// +kubebuilder:printcolumn:name="STATUS",type=string,JSONPath=`.status.status`
// +kubebuilder:printcolumn:name="SYNCED",type="string",priority=0,JSONPath=".status.conditions[?(@.type==\"ACK.ResourceSynced\")].status"
// +kubebuilder:printcolumn:name="AGE",type="date",priority=0,JSONPath=".metadata.creationTimestamp"
type DBRoleAssociation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DBRoleAssociationSpec   `json:"spec,omitempty"`
	Status            DBRoleAssociationStatus `json:"status,omitempty"`
}

// DBRoleAssociationList contains a list of DBRoleAssociation.
// +kubebuilder:object:root=true
type DBRoleAssociationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DBRoleAssociation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DBRoleAssociation{}, &DBRoleAssociationList{})
}
