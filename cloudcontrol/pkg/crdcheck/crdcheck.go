// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package crdcheck validates a CustomResourceDefinition the way the API
// server does when it is created (structural schema, CEL compilation and
// cost), without an API server, so the generator can tell which
// CloudFormation types make a CRD Kubernetes accepts.
package crdcheck

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation"
)

// Validate returns the API server's objections, or nil.
func Validate(crd *apiextv1.CustomResourceDefinition) error {
	v1 := crd.DeepCopy()
	apiextv1.SetObjectDefaults_CustomResourceDefinition(v1)
	var internal apiextensions.CustomResourceDefinition
	if err := apiextv1.Convert_v1_CustomResourceDefinition_To_apiextensions_CustomResourceDefinition(v1, &internal, nil); err != nil {
		return err
	}
	errs := validation.ValidateCustomResourceDefinition(context.Background(), &internal)
	if len(errs) == 0 {
		return nil
	}
	var msgs []string
	for _, e := range errs {
		msgs = append(msgs, e.Error())
	}
	return fmt.Errorf("%s", strings.Join(msgs, "; "))
}
