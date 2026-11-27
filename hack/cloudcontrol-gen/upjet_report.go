// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/cfn"
)

// upjetType is what the Upjet facts gave one served type.
type upjetType struct {
	s       *cfn.Schema
	t       typeEntry
	entries []*upjetEntry
	refs    []*derived
	checks  []check
}

// check is one cross-check row (secrets, ID format, late-init, list map
// keys, examples): what was compared, the outcome and the provenance.
type check struct {
	kind, subject, outcome, detail string
	slice                          int
	upstream                       string
}

func upjetIndex(root string, files map[string][]byte, cfg config) (*upjetData, *servedIndex, error) {
	up, err := readUpjet(root)
	if err != nil || up == nil {
		return up, &servedIndex{}, err
	}
	ack, err := readACKKinds(root)
	if err != nil {
		return nil, nil, err
	}
	ix := &servedIndex{ack: ack, cc: map[string]*ccKind{}, idPaths: map[string]refTarget{}, byTF: map[string][]*upjetEntry{}}
	for _, t := range cfg.ReferenceTargets {
		if t.Group == "" || t.Kind == "" || t.ID == "" {
			return nil, nil, fmt.Errorf("referenceTargets: %+v needs group, kind and id", t)
		}
		ix.idPaths[t.Group+"/"+t.Kind] = t
	}
	for _, t := range cfg.Types {
		s, err := cfn.Parse(files[schemaFile(t.TypeName)])
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", t.TypeName, err)
		}
		kind := t.Kind
		if kind == "" {
			kind = s.Resource()
		}
		ix.cc[strings.ToLower(s.Service())+"/"+s.Resource()] = &ccKind{group: groupOf(s, cfg.GroupSuffix), kind: kind, schema: s}
	}
	for _, e := range up.Entries {
		ix.byTF[e.Facts.Terraform] = append(ix.byTF[e.Facts.Terraform], e)
	}
	return up, ix, nil
}

// forType gathers the type's fold entries and derives its references.
func (ix *servedIndex) forType(up *upjetData, t typeEntry, s *cfn.Schema) *upjetType {
	ut := &upjetType{s: s, t: t}
	if up == nil {
		return ut
	}
	key := strings.ToLower(s.Service()) + "/" + s.Resource()
	for _, e := range up.Entries {
		if e.Ours.Service+"/"+e.Ours.Resource == key {
			ut.entries = append(ut.entries, e)
		}
	}
	ut.refs = ix.deriveReferences(s, ut.entries)
	ut.crossCheck(s, t)
	return ut
}

func (ut *upjetType) merged(s *cfn.Schema, hand []cfn.Reference) []cfn.Reference {
	return mergeReferences(s, hand, ut.refs)
}

// refused leaves the derived references out: the CRD with them would be
// refused.
func (ut *upjetType) refused(err error) {
	for _, d := range ut.refs {
		if d.outcome == "applied" {
			d.outcome, d.reason = "skipped", "the CRD with Upjet's references would be refused: "+firstLine(err.Error())
		}
	}
}

func (ut *upjetType) tested() string {
	if len(ut.entries) == 0 {
		return ""
	}
	for _, e := range ut.entries {
		if !e.Facts.Tested {
			return "no"
		}
	}
	return "yes"
}

type upjetCounts struct {
	types, withEntry, notTested            int
	handRefTypes, refTypes, handRefs, refs int
	applied, agrees, disagrees, skipped    int
	checks                                 map[string]map[string]int
}

func countUpjet(gens []generated) upjetCounts {
	c := upjetCounts{checks: map[string]map[string]int{}}
	for _, g := range gens {
		c.types++
		ut := g.Upjet
		if len(ut.entries) > 0 {
			c.withEntry++
			if ut.tested() == "no" {
				c.notTested++
			}
		}
		hand := len(g.References)
		for _, d := range ut.refs {
			switch d.outcome {
			case "applied":
				c.applied++
				hand--
			case "agrees":
				c.agrees++
			case "disagrees":
				c.disagrees++
			default:
				c.skipped++
			}
		}
		c.handRefs += hand
		c.refs += len(g.References)
		if hand > 0 {
			c.handRefTypes++
		}
		if len(g.References) > 0 {
			c.refTypes++
		}
		for _, k := range ut.checks {
			if c.checks[k.kind] == nil {
				c.checks[k.kind] = map[string]int{}
			}
			c.checks[k.kind][k.outcome]++
		}
	}
	return c
}

func upstreamLink(up *upjetData, at string) string {
	if at == "" {
		return ""
	}
	return fmt.Sprintf("%s@%s %s", up.Provider.Repo, up.Provider.Tag, at)
}

// writeUpjet writes what the generator made of the Upjet facts:
// cloudcontrol/zz_generated.upjet.md, and zz_generated.upjet.csv, one row
// per fact, which the coverage report counts (hack/coverage).
func writeUpjet(root, out string, up *upjetData, gens []generated) error {
	if up == nil {
		return nil
	}
	var fixtures []upjetFixture
	for _, g := range gens {
		fixtures = append(fixtures, g.Upjet.examples(g.Upjet.s, g.Upjet.t, g, up)...)
	}
	if err := writeExampleFixtures(out, fixtures); err != nil {
		return err
	}
	var rows [][]string
	add := func(g generated, kind, subject, outcome, detail string, slice int, upstream string) {
		sl := ""
		if slice > 0 {
			sl = at(slice)
		}
		rows = append(rows, []string{g.TypeName, g.Kind + "." + g.Group, kind, subject, outcome, detail, sl, upstreamLink(up, upstream)})
	}
	for _, g := range gens {
		ut := g.Upjet
		if len(ut.entries) == 0 {
			add(g, "entry", "", "none", "no Upjet resource maps to this type", 0, "")
			continue
		}
		for _, e := range ut.entries {
			tested := "tested"
			if !e.Facts.Tested {
				tested = "not-tested"
			}
			add(g, "entry", e.Facts.Terraform, tested, e.Facts.Group+"/"+e.Facts.Kind, e.line, e.Facts.ExternalName.At)
		}
		refField := map[string]string{}
		for _, r := range g.References {
			refField[r.Property] = r.RefField
		}
		for _, d := range ut.refs {
			subj := d.field
			detail := d.reason
			if d.outcome == "applied" {
				detail = fmt.Sprintf("%s: %s.%s %s", refField[d.ref.Property], d.ref.Kind, d.ref.Group, d.ref.Path)
				if d.ref.TrimPrefix != "" {
					detail += " (trim " + d.ref.TrimPrefix + ")"
				}
			}
			if d.ref.Property != "" {
				subj = d.field + " -> " + strings.TrimPrefix(d.ref.Property, "/properties/")
			}
			add(g, "reference", subj, d.outcome, detail, d.slice, d.upstream)
		}
		for _, k := range ut.checks {
			add(g, k.kind, k.subject, k.outcome, k.detail, k.slice, k.upstream)
		}
	}
	var cb bytes.Buffer
	w := csv.NewWriter(&cb)
	_ = w.Write([]string{"type", "kind", "check", "subject", "outcome", "detail", "slice", "upstream"})
	_ = w.WriteAll(rows)
	if err := writeFile(filepath.Join(out, "zz_generated.upjet.csv"), cb.Bytes()); err != nil {
		return err
	}

	c := countUpjet(gens)
	var b bytes.Buffer
	fmt.Fprintf(&b, "<!-- %s -->\n\n# Cloud Control kinds: what the Upjet facts gave\n\n", header)
	fmt.Fprintf(&b, "`hack/cloudcontrol-gen` reads [`%s`](../%s) (%s %s, factory's fold; HFL-699) as data: nothing runs a Crossplane provider. "+
		"Each row of [`zz_generated.upjet.csv`](zz_generated.upjet.csv) is one fact with its place in the slice and Upjet's own file and line. "+
		"Where the overlay (`cloudcontrol/types.yaml`) and Upjet disagree, the overlay is kept and the disagreement listed (the stricter rule, factory `docs/design.md` 5.4).\n\n",
		upjetSlice, upjetSlice, up.Provider.Repo, up.Provider.Tag)
	fmt.Fprintf(&b, "| | Count |\n| --- | ---: |\n")
	fmt.Fprintf(&b, "| Cloud Control kinds served | %d |\n", c.types)
	fmt.Fprintf(&b, "| with an Upjet resource | %d |\n", c.withEntry)
	fmt.Fprintf(&b, "| of those, not tested by Upjet's maintainers | %d |\n", c.notTested)
	fmt.Fprintf(&b, "| kinds with a reference field, before (the overlay alone) | %d |\n", c.handRefTypes)
	fmt.Fprintf(&b, "| kinds with a reference field, after (with Upjet's) | %d |\n", c.refTypes)
	fmt.Fprintf(&b, "| reference fields, before | %d |\n", c.handRefs)
	fmt.Fprintf(&b, "| reference fields, after | %d |\n", c.refs)
	fmt.Fprintf(&b, "| Upjet references applied | %d |\n", c.applied)
	fmt.Fprintf(&b, "| Upjet references the overlay already had | %d |\n", c.agrees)
	fmt.Fprintf(&b, "| Upjet references that disagree (listed, not applied) | %d |\n", c.disagrees)
	fmt.Fprintf(&b, "| Upjet references not applied (reason in the CSV) | %d |\n", c.skipped)
	var kinds []string
	for k := range c.checks {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		var parts []string
		var outs []string
		for o := range c.checks[k] {
			outs = append(outs, o)
		}
		sort.Strings(outs)
		for _, o := range outs {
			parts = append(parts, fmt.Sprintf("%s %d", o, c.checks[k][o]))
		}
		fmt.Fprintf(&b, "| %s checks | %s |\n", k, strings.Join(parts, ", "))
	}

	fmt.Fprintf(&b, "\n## References applied\n\n| Type | Property | Reference field | Target | Path | Slice | Upjet |\n| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, g := range gens {
		refField := map[string]string{}
		for _, r := range g.References {
			refField[r.Property] = r.RefField
		}
		for _, d := range g.Upjet.refs {
			if d.outcome != "applied" {
				continue
			}
			fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | `%s.%s` | `%s` | `%s` | `%s` |\n", g.TypeName, strings.TrimPrefix(d.ref.Property, "/properties/"), refField[d.ref.Property], d.ref.Kind, d.ref.Group, d.ref.Path, at(d.slice), d.upstream)
		}
	}
	fmt.Fprintf(&b, "\n## Disagreements\n\nThe overlay is kept; each is a gap to settle by hand.\n\n| Type | Upjet field | Why | Slice |\n| --- | --- | --- | --- |\n")
	for _, g := range gens {
		for _, d := range g.Upjet.refs {
			if d.outcome == "disagrees" {
				fmt.Fprintf(&b, "| `%s` | `%s` | %s | `%s` |\n", g.TypeName, d.field, strings.ReplaceAll(d.reason, "|", "\\|"), at(d.slice))
			}
		}
		for _, k := range g.Upjet.checks {
			if k.outcome == "mismatch" || k.outcome == "disagrees" {
				fmt.Fprintf(&b, "| `%s` | `%s` (%s) | %s | `%s` |\n", g.TypeName, k.subject, k.kind, strings.ReplaceAll(k.detail, "|", "\\|"), at(k.slice))
			}
		}
	}
	reasons := map[string]int{}
	for _, g := range gens {
		for _, d := range g.Upjet.refs {
			if d.outcome == "skipped" {
				reasons[reasonClass(d.reason)]++
			}
		}
	}
	var rs []string
	for r := range reasons {
		rs = append(rs, r)
	}
	sort.Slice(rs, func(i, j int) bool {
		if reasons[rs[i]] != reasons[rs[j]] {
			return reasons[rs[i]] > reasons[rs[j]]
		}
		return rs[i] < rs[j]
	})
	fmt.Fprintf(&b, "\n## Upjet references not applied, by reason\n\n| Reason | References |\n| --- | ---: |\n")
	for _, r := range rs {
		fmt.Fprintf(&b, "| %s | %d |\n", r, reasons[r])
	}
	writeUpjetSections(&b, up, gens)
	return writeFile(filepath.Join(out, "zz_generated.upjet.md"), b.Bytes())
}

// reasonClass groups a skip reason for the summary.
func reasonClass(r string) string {
	switch {
	case strings.Contains(r, "its ID path is not in referenceTargets"):
		return "ACK's kind serves the target; its ID path is not in `referenceTargets`"
	case strings.Contains(r, "is not served here"):
		return "the target is not served here"
	case strings.HasPrefix(r, "the extractor copies"):
		return "the reference copies a field other than the target's ARN or ID"
	case strings.Contains(r, "compound primary identifier"):
		return "our Cloud Control target has a compound identifier"
	case strings.Contains(r, "is identified by"):
		return "our Cloud Control target's identifier does not fit the property"
	case strings.Contains(r, "has no Arn property"):
		return "our Cloud Control target has no Arn property"
	case strings.Contains(r, "CRDs match"):
		return "ACK's kind not found once in `docs/coverage/sources.yaml`"
	}
	if i := strings.IndexAny(r, ":("); i > 0 {
		return strings.TrimSpace(r[:i])
	}
	return r
}
