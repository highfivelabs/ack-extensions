// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/cfn"
)

// ---- Any type: the schema fake ----

// SchemaStore is a backend for any type, made from its registry schema
// alone: it keeps each resource's model in memory and fills what AWS would
// (docs/testing.md, "The schema fake"). It is what a type marked unproven in
// cloudcontrol/types.yaml runs on, because moto has no backend for it.
//
// What it shows: the kind's CRD is served and accepted, the spec reaches
// Cloud Control as the type's CloudFormation model (names, nesting, Secret
// references), the identifier and read-only properties come back into the
// status, a resync of an unchanged object makes no update, and the delete
// path ends. The server around it adds Cloud Control's own rules (required,
// read-only, create-only and write-only properties, the asynchronous
// requests). What it cannot show is anything the service itself does:
// validation of values, defaults, side effects, eventual consistency. A
// type on it stays "unproven on fake" in the coverage data.
//
// What it fills, unverified on real AWS:
//
//   - each top-level read-only property of type string, integer, number
//     or boolean: the first value of its enum if it has one; else an
//     ARN-shaped string (padded or cut to the schema's lengths) for a name ending in Arn, a
//     "<resource>-<n>" for any other string, n for a number, true for a
//     boolean;
//   - a writable primary identifier the model leaves out (CloudFormation
//     names it), the same way;
//   - the identifier: the primary identifier's values joined with "|", as
//     Cloud Control writes a compound one.
type SchemaStore struct {
	Schema          *cfn.Schema
	Account, Region string

	mu    sync.Mutex
	items map[string]map[string]any // identifier -> model
	n     int
}

// NewSchemaStore makes the schema fake for one type.
func NewSchemaStore(s *cfn.Schema, account, region string) *SchemaStore {
	return &SchemaStore{Schema: s, Account: account, Region: region, items: map[string]map[string]any{}}
}

// Model is what the store holds for an identifier, for the tests' checks.
func (b *SchemaStore) Model(id string) (map[string]any, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	m, ok := b.items[id]
	if !ok {
		return nil, false
	}
	return copyModel(m), true
}

// topLevel is the name of a pointer to a top-level property, or "".
func topLevel(ptr string) string {
	name := strings.TrimPrefix(ptr, "/properties/")
	if name == ptr || strings.Contains(name, "/") {
		return ""
	}
	return name
}

func (b *SchemaStore) value(name string, n int) any {
	p := b.Schema.Properties[name]
	if p == nil {
		return nil
	}
	p, _ = b.Schema.Resolve(p)
	if len(p.Enum) > 0 {
		// The CRD's status keeps the schema's enum: a value outside it
		// is refused by the API server.
		return p.Enum[0]
	}
	switch p.Type.Single() {
	case "string", "":
		res := strings.ToLower(b.Schema.Resource())
		v := fmt.Sprintf("%s-%d", res, n)
		if strings.HasSuffix(name, "Arn") || strings.HasSuffix(name, "ARN") {
			v = fmt.Sprintf("arn:aws:%s:%s:%s:%s/%s-%d", strings.ToLower(b.Schema.Service()), b.Region, b.Account, res, res, n)
		}
		// The CRD keeps the schema's lengths too.
		if p.MinLength != nil {
			for int64(len(v)) < *p.MinLength {
				v += "0"
			}
		}
		if p.MaxLength != nil && int64(len(v)) > *p.MaxLength {
			v = v[len(v)-int(*p.MaxLength):]
		}
		return v
	case "integer", "number":
		return float64(n)
	case "boolean":
		return true
	}
	return nil
}

func (b *SchemaStore) Create(_ context.Context, model map[string]any) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.n++
	m := copyModel(model)
	for _, ptr := range b.Schema.ReadOnlyProperties {
		if name := topLevel(ptr); name != "" {
			if v := b.value(name, b.n); v != nil {
				m[name] = v
			}
		}
	}
	var parts []string
	for _, ptr := range b.Schema.PrimaryIdentifier {
		name := topLevel(ptr)
		if name == "" {
			return "", InvalidRequest("primary identifier %s is not a top-level property", ptr)
		}
		if _, ok := m[name]; !ok {
			if v := b.value(name, b.n); v != nil {
				m[name] = v
			}
		}
		v, ok := m[name]
		if !ok {
			return "", InvalidRequest("primary identifier %s has no value", ptr)
		}
		parts = append(parts, fmt.Sprint(v))
	}
	id := strings.Join(parts, "|")
	if _, ok := b.items[id]; ok {
		return "", &HandlerError{"AlreadyExists", fmt.Sprintf("%s %s already exists", b.Schema.TypeName, id)}
	}
	b.items[id] = m
	return id, nil
}

func (b *SchemaStore) Read(_ context.Context, id string) (map[string]any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	m, ok := b.items[id]
	if !ok {
		return nil, NotFound("%s %s not found", b.Schema.TypeName, id)
	}
	return copyModel(m), nil
}

func (b *SchemaStore) Update(_ context.Context, id string, _, next map[string]any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	cur, ok := b.items[id]
	if !ok {
		return NotFound("%s %s not found", b.Schema.TypeName, id)
	}
	m := copyModel(next)
	// Read-only values are the store's, whatever the patch left.
	for _, ptr := range b.Schema.ReadOnlyProperties {
		if name := topLevel(ptr); name != "" {
			if v, ok := cur[name]; ok {
				m[name] = v
			}
		}
	}
	b.items[id] = m
	return nil
}

func (b *SchemaStore) Delete(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.items[id]; !ok {
		return NotFound("%s %s not found", b.Schema.TypeName, id)
	}
	delete(b.items, id)
	return nil
}
