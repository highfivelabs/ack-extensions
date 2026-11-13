// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package crdgen

import (
	"fmt"
	"sort"
	"strings"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/cfn"
)

// createOnlyRules adds a CEL transition rule for each create-only property
// the API server can check: the field may not change (self == oldSelf), and
// may not be added or removed after create (on its parent). Paths inside
// arrays are skipped (a transition rule there needs a keyed list) and so are
// free-form values; both are reported.
func (g *gen) createOnlyRules(spec *apiextv1.JSONSchemaProps) error {
	var ptrs []string
	for p := range g.sets.CreateOnly {
		ptrs = append(ptrs, p)
	}
	sort.Strings(ptrs)
	for _, ptr := range ptrs {
		parts := strings.Split(strings.TrimPrefix(ptr, "/properties/"), "/")
		if g.sets.ReadOnly.Covers(ptr) {
			continue // read-only and create-only: the server sets it once
		}
		skip := false
		for _, seg := range parts {
			if seg == "*" {
				skip = true
			}
		}
		if skip {
			g.rep.CELSkipped = append(g.rep.CELSkipped, ptr+" (inside an array)")
			continue
		}
		fields := g.s.FieldPath(parts)
		name := strings.Join(fields, ".")
		why := ""
		edit(spec, fields, func(parent *apiextv1.JSONSchemaProps, leaf string) {
			node, ok := parent.Properties[leaf]
			switch {
			case !ok:
				why = "not in spec"
				return
			case node.XPreserveUnknownFields != nil && *node.XPreserveUnknownFields:
				why = "free-form value"
				return
			}
			node.XValidations = append(node.XValidations, apiextv1.ValidationRule{
				Rule:    "self == oldSelf",
				Message: fmt.Sprintf("%s is create-only (CloudFormation createOnlyProperties): it cannot change after the resource is made", name),
			})
			parent.Properties[leaf] = node
			parent.XValidations = append(parent.XValidations, apiextv1.ValidationRule{
				Rule:    fmt.Sprintf("has(self.%s) == has(oldSelf.%s)", leaf, leaf),
				Message: fmt.Sprintf("%s is create-only (CloudFormation createOnlyProperties): it cannot be added or removed after the resource is made", name),
			})
			why = "ok"
		})
		switch why {
		case "ok":
		case "":
			g.rep.CELSkipped = append(g.rep.CELSkipped, ptr+" (parent not an object)")
			continue
		default:
			g.rep.CELSkipped = append(g.rep.CELSkipped, ptr+" ("+why+")")
			continue
		}
		g.rep.CELRules += 2
	}
	return nil
}

// edit walks object fields to the parent of the last one and calls fn on
// it, writing each changed copy back into its parent.
func edit(n *apiextv1.JSONSchemaProps, fields []string, fn func(parent *apiextv1.JSONSchemaProps, leaf string)) {
	if len(fields) == 1 {
		fn(n, fields[0])
		return
	}
	child, ok := n.Properties[fields[0]]
	if !ok || child.Type != "object" {
		return
	}
	edit(&child, fields[1:], fn)
	n.Properties[fields[0]] = child
}

func statusSchema() apiextv1.JSONSchemaProps {
	str := apiextv1.JSONSchemaProps{Type: "string"}
	return apiextv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]apiextv1.JSONSchemaProps{
			"ackResourceMetadata": {
				Description: "The resource's ARN (when the type has one), account and region, as on every ACK kind.",
				Type:        "object",
				Properties: map[string]apiextv1.JSONSchemaProps{
					"arn": str, "ownerAccountID": str, "region": str,
				},
				Required: []string{"ownerAccountID", "region"},
			},
			"conditions": {
				Description: "ACK's conditions: ACK.ResourceSynced, Ready, ACK.Terminal, ACK.Recoverable, ACK.Adopted.",
				Type:        "array",
				Items: &apiextv1.JSONSchemaPropsOrArray{Schema: &apiextv1.JSONSchemaProps{
					Type: "object",
					Properties: map[string]apiextv1.JSONSchemaProps{
						"type": str, "status": str, "message": str, "reason": str,
						"lastTransitionTime": {Type: "string", Format: "date-time"},
					},
					Required: []string{"status", "type"},
				}},
			},
			"cloudControl": {
				Description: "Cloud Control's view of the resource.",
				Type:        "object",
				Properties: map[string]apiextv1.JSONSchemaProps{
					"identifier": {
						Description: "Cloud Control's identifier of the resource: the primary identifier's values joined with |.",
						Type:        "string",
					},
					"request": {
						Description: "The last Cloud Control request: its token, operation, status and, when it failed, the handler's error code and message.",
						Type:        "object",
						Properties: map[string]apiextv1.JSONSchemaProps{
							"token": str, "operation": str, "status": str, "errorCode": str, "message": str,
							"eventTime": {Type: "string", Format: "date-time"},
						},
					},
					"writeOnlyDigest": {
						Description: "A digest of the write-only values last sent, which Cloud Control never returns, so a changed Secret is noticed.",
						Type:        "string",
					},
				},
			},
		},
	}
}

// Fields returns the spec field names of a generated CRD, for reports.
func Fields(s *cfn.Schema) []string {
	names, _ := cfn.Names(s.Properties)
	var out []string
	for _, f := range names.ToField {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// references adds the overlay's reference fields to spec. Each sits beside
// the property it fills: the property stops being required on its own, and
// CEL rules ask for one of the two (when the property is required) and
// refuse both, as ACK's generated validateReferenceFields does at
// reconcile time. A create-only property's reference is create-only too.
func (g *gen) references(spec *apiextv1.JSONSchemaProps, refs []cfn.Reference) {
	for _, r := range refs {
		spec.Properties[r.RefField] = referenceWrapper(r)
		g.rep.References = append(g.rep.References, r.RefField)
		required := false
		var kept []string
		for _, f := range spec.Required {
			if f == r.Field {
				required = true
				continue
			}
			kept = append(kept, f)
		}
		spec.Required = kept
		if required {
			spec.XValidations = append(spec.XValidations, apiextv1.ValidationRule{
				Rule:    fmt.Sprintf("has(self.%s) || has(self.%s)", r.Field, r.RefField),
				Message: fmt.Sprintf("one of %s or %s is required", r.Field, r.RefField),
			})
		}
		spec.XValidations = append(spec.XValidations, apiextv1.ValidationRule{
			Rule:    fmt.Sprintf("!(has(self.%s) && has(self.%s))", r.Field, r.RefField),
			Message: fmt.Sprintf("%s and %s cannot both be given", r.Field, r.RefField),
		})
		g.rep.CELRules++
		if required {
			g.rep.CELRules++
		}
		if g.sets.CreateOnly.Has(r.Property) {
			node := spec.Properties[r.RefField]
			node.XValidations = append(node.XValidations, apiextv1.ValidationRule{
				Rule:    "self == oldSelf",
				Message: fmt.Sprintf("%s is create-only, as %s is: it cannot change after the resource is made", r.RefField, r.Field),
			})
			spec.Properties[r.RefField] = node
			spec.XValidations = append(spec.XValidations, apiextv1.ValidationRule{
				Rule:    fmt.Sprintf("has(self.%s) == has(oldSelf.%s)", r.RefField, r.RefField),
				Message: fmt.Sprintf("%s is create-only, as %s is: it cannot be added or removed after the resource is made", r.RefField, r.Field),
			})
			g.rep.CELRules += 2
		}
	}
}

// referenceWrapper is ACK's AWSResourceReferenceWrapper: {from: {name,
// namespace}}.
func referenceWrapper(r cfn.Reference) apiextv1.JSONSchemaProps {
	str := apiextv1.JSONSchemaProps{Type: "string"}
	return apiextv1.JSONSchemaProps{
		Description: fmt.Sprintf("A reference to a %s (%s) whose %s fills %s: its Kubernetes name, and namespace when another (cross-namespace references need the manager's --enable-cross-namespace). The value is read once that object is synced. Give this or %s, not both.", r.Kind, r.Group, r.Path, r.Field, r.Field),
		Type:        "object",
		Properties: map[string]apiextv1.JSONSchemaProps{
			"from": {
				Description: "The referenced object.",
				Type:        "object",
				Properties:  map[string]apiextv1.JSONSchemaProps{"name": str, "namespace": str},
			},
		},
	}
}
