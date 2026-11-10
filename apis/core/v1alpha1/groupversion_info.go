// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package v1alpha1 is the API of the operator itself (not of the kinds
// its managers serve): ACKContext, which enables the ACK-generated
// managers in a namespace (an environment), as CloudControlContext
// (cloudcontrol/apis) enables Cloud Control.
//
// +kubebuilder:object:generate=true
// +groupName=core.aws.hfl.dev
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the API's group and version.
	GroupVersion = schema.GroupVersion{Group: "core.aws.hfl.dev", Version: "v1alpha1"}

	// SchemeBuilder adds the API's types to a scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the API's types to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
