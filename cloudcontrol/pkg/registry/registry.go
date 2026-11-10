// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package registry lists the CloudFormation types this module serves as
// Kubernetes kinds through Cloud Control. The list and the embedded schemas
// are generated (zz_generated.registry.go, schemas/) by
// hack/cloudcontrol-gen from cloudcontrol/types.yaml; this file only loads
// them.
package registry

import (
	"embed"
	"fmt"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/cfn"
)

// GroupSuffix is the suffix of every Cloud Control kind's API group,
// <service>.cloudcontrol.aws.hfl.dev (the builder, 2026-11-09): separate
// from the ACK-generated <service>.aws.hfl.dev, and matched by its own
// Bundt Provider, hfl.aws-cloudcontrol.
const GroupSuffix = "cloudcontrol.aws.hfl.dev"

//go:embed schemas/*.json
var schemaFS embed.FS

// Type is one kind and the CloudFormation type behind it.
type Type struct {
	Group, Version, Kind, Plural string
	TypeName                     string
	SchemaFile                   string
	// Secrets are the write-only properties served as Secret references
	// (property pointers), opted in per field in cloudcontrol/types.yaml.
	Secrets []string
	// Unproven, when set, says why the kind has no end-to-end test on the
	// fake (cloudcontrol/types.yaml); empty: it has one.
	Unproven string

	once    sync.Once
	schema  *cfn.Schema
	secrets [][]string
	err     error
}

// GVK is the kind's group, version and kind.
func (t *Type) GVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: t.Group, Version: t.Version, Kind: t.Kind}
}

// Schema is the parsed registry schema (embedded at generation).
func (t *Type) Schema() *cfn.Schema {
	t.once.Do(func() {
		b, err := schemaFS.ReadFile(t.SchemaFile)
		if err != nil {
			t.err = err
			return
		}
		if t.schema, t.err = cfn.Parse(b); t.err != nil {
			return
		}
		t.secrets, t.err = t.schema.SecretPaths(t.Secrets)
	})
	if t.err != nil {
		panic(fmt.Sprintf("registry: %s: %v", t.TypeName, t.err))
	}
	return t.schema
}

// SecretPaths are Secrets as CloudFormation property paths
// (cfn.Schema.SecretPaths).
func (t *Type) SecretPaths() [][]string {
	t.Schema()
	return t.secrets
}

// Ref is how a context names the kind: Kind.service (SAMLProvider.iam).
func (t *Type) Ref() string {
	return t.Kind + "." + strings.TrimSuffix(t.Group, "."+GroupSuffix)
}

// ByRef finds a type by Ref.
func ByRef(ref string) *Type {
	for _, t := range Types {
		if t.Ref() == ref {
			return t
		}
	}
	return nil
}

// ByTypeName finds a type by its CloudFormation name.
func ByTypeName(name string) *Type {
	for _, t := range Types {
		if t.TypeName == name {
			return t
		}
	}
	return nil
}
