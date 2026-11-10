// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package v1alpha1 is the API of the Cloud Control route itself (not of
// the kinds it serves): CloudControlContext, which enables Cloud Control in
// a namespace.
//
// +kubebuilder:object:generate=true
// +groupName=cloudcontrol.aws.hfl.dev
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the API's group and version.
	GroupVersion = schema.GroupVersion{Group: "cloudcontrol.aws.hfl.dev", Version: "v1alpha1"}

	// SchemeBuilder adds the API's types to a scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the API's types to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
