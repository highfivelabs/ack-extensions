// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/cfn"
)

// The cross-checks of a served type against its Upjet facts (HFL-699):
// each is a row of zz_generated.upjet.csv; a disagreement is listed in
// zz_generated.upjet.md, never acted on by the generator.

// topProp is the top-level property a Terraform field names, or "".
func topProp(s *cfn.Schema, field string) string {
	if strings.ContainsAny(field, ".[*") {
		return ""
	}
	want := strings.ReplaceAll(field, "_", "")
	for p := range s.Properties {
		if strings.EqualFold(p, want) {
			return p
		}
	}
	return ""
}

// secretish: a field Upjet treats as a secret, by its Sensitive override,
// or an override on a field whose name says it holds one.
func secretish(o upjetAt) bool {
	if o.Property == "Sensitive" {
		return o.Value == "true"
	}
	f := strings.ToLower(o.Field)
	return strings.Contains(f, "password") || strings.Contains(f, "auth_token") || strings.Contains(f, "secret")
}

func (ut *upjetType) crossCheck(s *cfn.Schema, t typeEntry) {
	sets := s.Sets()
	secrets := map[string]bool{}
	for _, p := range t.Secrets {
		secrets[p] = true
	}
	for _, e := range ut.entries {
		f := e.Facts
		line := e.line
		// Secrets: what Upjet hides against what we expose.
		for _, o := range f.SchemaOverrides {
			if !secretish(o) {
				continue
			}
			p := topProp(s, o.Field)
			c := check{kind: "secret", subject: o.Field, slice: line, upstream: o.At}
			ptr := cfn.Pointer(p)
			switch {
			case p == "":
				c.outcome, c.detail = "n/a", "no top-level property of that name"
			case secrets[ptr]:
				c.outcome, c.detail = "agrees", p+" is a Secret reference here"
			case sets.ReadOnly.Has(ptr):
				c.outcome, c.detail = "exposed", p+" is read-only: in the status in plain text, where Upjet keeps it secret"
			case sets.WriteOnly.Has(ptr):
				c.outcome, c.detail = "exposed", p+" is write-only and a plain spec field: opt it in with `secrets` in types.yaml if it is one"
			default:
				c.outcome, c.detail = "exposed", p+" is a plain spec field, where Upjet keeps it secret (CloudFormation does not mark it write-only)"
			}
			ut.checks = append(ut.checks, c)
		}
		for _, cc := range f.CustomCode {
			if cc.What == "connection details" {
				ut.checks = append(ut.checks, check{kind: "secret", subject: "connection details", outcome: "listed", detail: "Upjet publishes connection details (a Secret) for this resource; this kind publishes none (ACK's FieldExport can copy a status field)", slice: line, upstream: cc.At})
			}
		}
		// ID format: who chooses the identifier.
		ids := s.PrimaryIdentifierNames()
		userNamed := len(ids) > 0
		for _, id := range ids {
			userNamed = userNamed && !sets.ReadOnly.Has(cfn.Pointer(id))
		}
		ours := "assigned by AWS (" + strings.Join(ids, "|") + ")"
		if userNamed {
			ours = "written by the user (" + strings.Join(ids, "|") + ")"
		}
		en := f.ExternalName
		c := check{kind: "id", subject: en.Style, slice: line, upstream: en.At}
		switch en.Style {
		case "name", "parameter":
			theirs := "a name the user writes"
			if en.Parameter != "" {
				theirs += " (" + en.Parameter + ")"
			}
			if userNamed {
				c.outcome, c.detail = "agrees", "both "+theirs+"; ours "+ours
			} else {
				c.outcome, c.detail = "differs", "Upjet: "+theirs+"; ours "+ours+": adoption takes the AWS-assigned identifier"
			}
		case "provider", "identifier":
			if userNamed {
				c.outcome, c.detail = "differs", "Upjet: assigned by AWS; ours "+ours
			} else {
				c.outcome, c.detail = "agrees", "both assigned by AWS; ours "+ours
			}
		default:
			c.outcome = "listed"
			c.detail = "Upjet: " + en.Style
			if en.Template != "" {
				c.detail += " " + en.Template
			}
			c.detail += "; ours " + ours
		}
		ut.checks = append(ut.checks, c)
		// Late-init ignores: fields Upjet never fills from AWS.
		for _, li := range f.LateInitIgnored {
			p := topProp(s, li.Field)
			c := check{kind: "late-init", subject: li.Field, slice: line, upstream: li.At}
			if p == "" {
				c.outcome, c.detail = "n/a", "no top-level property of that name"
			} else {
				c.outcome, c.detail = "agrees", p+": the reconciler late-initializes nothing (LateInitialize is a no-op) and compares only the fields the spec sets (docs/cloudcontrol.md section 3)"
			}
			ut.checks = append(ut.checks, c)
		}
		// List map keys: lists Upjet merges as maps.
		if len(f.ListMapKeys) > 0 && string(f.ListMapKeys) != "null" && string(f.ListMapKeys) != "[]" {
			ut.checks = append(ut.checks, check{kind: "list-map", subject: string(f.ListMapKeys), outcome: "listed", detail: "arrays are replaced whole here, compared as multisets when unordered", slice: line})
		}
	}
}

// writeUpjetSections writes the cross-check and example sections.
func writeUpjetSections(b *bytes.Buffer, up *upjetData, gens []generated) {
	fmt.Fprintf(b, "\n## Secrets: fields Upjet keeps secret\n\n`exposed` means this kind shows the field in plain text where Upjet treats it as a secret.\n\n| Type | Field | Outcome | Here | Upjet |\n| --- | --- | --- | --- | --- |\n")
	for _, g := range gens {
		for _, c := range g.Upjet.checks {
			if c.kind == "secret" {
				fmt.Fprintf(b, "| `%s` | `%s` | %s | %s | `%s` |\n", g.TypeName, c.subject, c.outcome, c.detail, c.upstream)
			}
		}
	}
	fmt.Fprintf(b, "\n## Identifier: who chooses it\n\nUpjet's external name against the type's primary identifier. `differs` is not a defect here (a Cloud Control kind is identified as CloudFormation identifies the type), but adoption and imports take a different value than Terraform's.\n\n| Type | Upjet | Here |\n| --- | --- | --- |\n")
	for _, g := range gens {
		for _, c := range g.Upjet.checks {
			if c.kind == "id" && c.outcome == "differs" {
				fmt.Fprintf(b, "| `%s` | %s | %s |\n", g.TypeName, c.subject, c.detail)
			}
		}
	}
	fmt.Fprintf(b, "\n## Late-init ignores\n\n| Type | Field | Here |\n| --- | --- | --- |\n")
	for _, g := range gens {
		for _, c := range g.Upjet.checks {
			if c.kind == "late-init" {
				fmt.Fprintf(b, "| `%s` | `%s` | %s |\n", g.TypeName, c.subject, c.outcome)
			}
		}
	}
	n := 0
	for _, g := range gens {
		for _, c := range g.Upjet.checks {
			if c.kind == "list-map" {
				n++
			}
		}
	}
	fmt.Fprintf(b, "\n## List map keys\n\n%d of the served types' Upjet resources merge a list as a map (the slice has none for this provider release).\n", n)
	writeExampleSection(b, gens)
}

// sortedKeys is a map's keys in order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
