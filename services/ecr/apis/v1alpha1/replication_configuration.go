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
// aws-controllers-k8s/ecr-controller pull request #121 (head f6c7d57e),
// apis/v1alpha1/replication_configuration.go. Changes: upstream marks the
// file generated, but it is written by hand (ack-generate does not produce
// it), so the generator's header is replaced by this note, or
// hack/generate.sh would delete it; validation markers (rules 1 to 10,
// destinations 1 to 25, filters 1 to 100, filterType PREFIX_MATCH, as
// ECR's API reference states); status.registryID; a short name and print
// columns as our other kinds have.

package v1alpha1

import (
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ReplicationRule represents a single replication rule
type ReplicationRule struct {
	// An array of objects representing the destination for a replication rule.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=25
	Destinations []*ReplicationDestination `json:"destinations"`
	// An array of objects representing the filters for a replication rule.
	// +kubebuilder:validation:MaxItems=100
	RepositoryFilters []*RepositoryFilter `json:"repositoryFilters,omitempty"`
}

// ReplicationDestination is a registry and region to replicate to.
type ReplicationDestination struct {
	// The Region to replicate to.
	Region *string `json:"region"`
	// The Amazon Web Services account ID of the Amazon ECR private registry
	// to replicate to.
	//
	// Regex Pattern: `^[0-9]{12}$`
	// +kubebuilder:validation:Pattern=`^[0-9]{12}$`
	RegistryID *string `json:"registryID"`
}

// RepositoryFilter represents a filter for repositories in replication rules
type RepositoryFilter struct {
	// The repository filter details.
	Filter *string `json:"filter"`
	// The repository filter type.
	// +kubebuilder:validation:Enum=PREFIX_MATCH
	FilterType *string `json:"filterType"`
}

// ReplicationConfigurationForRegistry represents the replication configuration for a registry
type ReplicationConfigurationForRegistry struct {
	// An array of objects representing the replication rules for a replication configuration.
	// At least one: a configuration with no rules is no replication, which
	// deleting the object gives.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=10
	Rules []*ReplicationRule `json:"rules"`
}

// ReplicationConfigurationSpec defines the desired state of ReplicationConfiguration.
//
// An object representing the replication configuration for an ECR registry.
type ReplicationConfigurationSpec struct {

	// The replication configuration for a registry.
	ReplicationConfiguration *ReplicationConfigurationForRegistry `json:"replicationConfiguration"`
	// The Amazon Web Services account ID associated with the registry to configure
	// replication for. If you do not specify a registry, the default registry is
	// assumed.
	//
	// Regex Pattern: `^[0-9]{12}$`
	// +kubebuilder:validation:Pattern=`^[0-9]{12}$`
	RegistryID *string `json:"registryID,omitempty"`
}

// ReplicationConfigurationStatus defines the observed state of ReplicationConfiguration
type ReplicationConfigurationStatus struct {
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
	// The registry DescribeRegistry reports: the account the manager acts
	// as, in its region.
	// +kubebuilder:validation:Optional
	RegistryID *string `json:"registryID,omitempty"`
}

// ReplicationConfiguration is the Schema for the ReplicationConfigurations API
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=aws;hfl,shortName=hflreplicationconfiguration
// +kubebuilder:printcolumn:name="REGISTRY-ID",type=string,priority=0,JSONPath=`.status.registryID`
// +kubebuilder:printcolumn:name="Synced",type="string",priority=0,JSONPath=".status.conditions[?(@.type==\"ACK.ResourceSynced\")].status"
// +kubebuilder:printcolumn:name="AGE",type="date",priority=0,JSONPath=".metadata.creationTimestamp"
type ReplicationConfiguration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ReplicationConfigurationSpec   `json:"spec,omitempty"`
	Status            ReplicationConfigurationStatus `json:"status,omitempty"`
}

// ReplicationConfigurationList contains a list of ReplicationConfiguration
// +kubebuilder:object:root=true
type ReplicationConfigurationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ReplicationConfiguration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ReplicationConfiguration{}, &ReplicationConfigurationList{})
}
