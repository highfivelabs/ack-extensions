// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package ackmirrors holds minimal mirrors of ACK's own elasticache and sns
// kinds (elasticache.services.k8s.aws/v1alpha1 and sns.services.k8s.aws/v1alpha1)
// reduced to the fields that elasticache.aws.hfl.dev CacheCluster and
// ReplicationGroup resource references read.
//
// Living outside apis/ keeps controller-gen from generating CRDs for them;
// services/elasticache/apis/v1alpha1 aliases the elasticache types for the
// generated references.go, and cmd/controller/handwritten.go registers their
// upstream GroupVersions in the scheme.
package ackmirrors

import (
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// CommonStatus mirrors the ACK status fields read by reference resolution.
type CommonStatus struct {
	ACKResourceMetadata *ackv1alpha1.ResourceMetadata `json:"ackResourceMetadata,omitempty"`
	Conditions          []*ackv1alpha1.Condition      `json:"conditions"`
}

// CacheParameterGroup mirrors ACK's elasticache CacheParameterGroup.
type CacheParameterGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CacheParameterGroupSpec `json:"spec,omitempty"`
	Status            CommonStatus            `json:"status,omitempty"`
}

// CacheParameterGroupSpec holds the field referenced by CacheCluster/ReplicationGroup.
type CacheParameterGroupSpec struct {
	CacheParameterGroupName *string `json:"cacheParameterGroupName,omitempty"`
}

// DeepCopyObject implements runtime.Object.
func (in *CacheParameterGroup) DeepCopyObject() runtime.Object {
	out := *in
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	return &out
}

// CacheSubnetGroup mirrors ACK's elasticache CacheSubnetGroup.
type CacheSubnetGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CacheSubnetGroupSpec `json:"spec,omitempty"`
	Status            CommonStatus         `json:"status,omitempty"`
}

// CacheSubnetGroupSpec holds the field referenced by CacheCluster/ReplicationGroup.
type CacheSubnetGroupSpec struct {
	CacheSubnetGroupName *string `json:"cacheSubnetGroupName,omitempty"`
}

// DeepCopyObject implements runtime.Object.
func (in *CacheSubnetGroup) DeepCopyObject() runtime.Object {
	out := *in
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	return &out
}

// Snapshot mirrors ACK's elasticache Snapshot.
type Snapshot struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SnapshotSpec `json:"spec,omitempty"`
	Status            CommonStatus `json:"status,omitempty"`
}

// SnapshotSpec holds the field referenced by CacheCluster/ReplicationGroup.
type SnapshotSpec struct {
	SnapshotName *string `json:"snapshotName,omitempty"`
}

// DeepCopyObject implements runtime.Object.
func (in *Snapshot) DeepCopyObject() runtime.Object {
	out := *in
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	return &out
}

// UserGroup mirrors ACK's elasticache UserGroup.
type UserGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              UserGroupSpec `json:"spec,omitempty"`
	Status            CommonStatus  `json:"status,omitempty"`
}

// UserGroupSpec holds the field referenced by ReplicationGroup.
type UserGroupSpec struct {
	UserGroupID *string `json:"userGroupID,omitempty"`
}

// DeepCopyObject implements runtime.Object.
func (in *UserGroup) DeepCopyObject() runtime.Object {
	out := *in
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	return &out
}

// Topic mirrors ACK's sns Topic.
type Topic struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Status            CommonStatus `json:"status,omitempty"`
}

// DeepCopyObject implements runtime.Object.
func (in *Topic) DeepCopyObject() runtime.Object {
	out := *in
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	return &out
}

// AddToScheme registers the mirrored ACK types under their upstream
// services.k8s.aws GroupVersions.
func AddToScheme(s *runtime.Scheme) error {
	s.AddKnownTypes(
		schema.GroupVersion{Group: "elasticache.services.k8s.aws", Version: "v1alpha1"},
		&CacheParameterGroup{},
		&CacheSubnetGroup{},
		&Snapshot{},
		&UserGroup{},
	)
	s.AddKnownTypes(
		schema.GroupVersion{Group: "sns.services.k8s.aws", Version: "v1alpha1"},
		&Topic{},
	)
	return nil
}
