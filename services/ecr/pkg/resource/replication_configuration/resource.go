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
// aws-controllers-k8s/iam-controller pull request #184 (head 78eb350e),
// pkg/resource/role_policy_attachment/resource.go, the hand-written
// RolePolicyAttachment resource, as the skeleton of ReplicationConfiguration.
// Changes: package, kind, API group, service client, identifiers read from annotations on adoption
// (registryID, optional: there is one replication configuration per registry).

package replication_configuration

import (
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerrors "github.com/aws-controllers-k8s/runtime/pkg/errors"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	rtclient "sigs.k8s.io/controller-runtime/pkg/client"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/ecr/apis/v1alpha1"
)

var (
	_ = &ackerrors.MissingNameIdentifier
)

type resource struct {
	ko *svcapitypes.ReplicationConfiguration
}

func (r *resource) Identifiers() acktypes.AWSResourceIdentifiers {
	return &resourceIdentifiers{r.ko.Status.ACKResourceMetadata}
}

func (r *resource) IsBeingDeleted() bool {
	return !r.ko.DeletionTimestamp.IsZero()
}

func (r *resource) RuntimeObject() rtclient.Object {
	return r.ko
}

func (r *resource) MetaObject() metav1.Object {
	return r.ko.GetObjectMeta()
}

func (r *resource) Conditions() []*ackv1alpha1.Condition {
	return r.ko.Status.Conditions
}

func (r *resource) ReplaceConditions(conditions []*ackv1alpha1.Condition) {
	r.ko.Status.Conditions = conditions
}

func (r *resource) SetObjectMeta(meta metav1.ObjectMeta) {
	r.ko.ObjectMeta = meta
}

func (r *resource) SetStatus(desired acktypes.AWSResource) {
	r.ko.Status = desired.(*resource).ko.Status
}

func (r *resource) SetIdentifiers(identifier *ackv1alpha1.AWSIdentifiers) error {
	if identifier.NameOrID == "" {
		return ackerrors.MissingNameIdentifier
	}
	r.ko.Name = identifier.NameOrID
	return nil
}

func (r *resource) PopulateResourceFromAnnotation(fields map[string]string) error {
	if registry, ok := fields["registryID"]; ok {
		r.ko.Spec.RegistryID = &registry
	}
	return nil
}

func (r *resource) DeepCopy() acktypes.AWSResource {
	koCopy := r.ko.DeepCopy()
	return &resource{koCopy}
}
