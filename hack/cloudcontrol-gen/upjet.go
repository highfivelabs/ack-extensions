// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/cfn"
)

// The Upjet facts (hack/upjet, HFL-699): factory's fold of Crossplane's
// provider-upjet-aws onto this repository's API resources. The generator
// reads them as data and never runs a provider. What it takes:
//
//   - references: a top-level string property Upjet fills by reference
//     gets a reference field here when the target is served here (ACK's
//     kind first, then ours) and the path holding the value is known;
//     cloudcontrol/types.yaml's own references win, and a disagreement is
//     listed, never overridden (factory docs/design.md 5.4, the stricter
//     rule);
//   - cross-checks, listed in cloudcontrol/zz_generated.upjet.md: Upjet's
//     Sensitive overrides against our secret handling, its ID format
//     against the type's primary identifier, its late-init ignores and
//     list map keys against the reconciler's behaviour;
//   - hand-written examples (only those: generated ones carry no values)
//     that the kind can express, as fixtures the schema fake runs
//     (cloudcontrol/test/fake, TestUpjetExamples, HFL-700);
//   - the tested flag, beside our own per-kind proof (HFL-701).
//
// Every fact carries its place in the slice (file:line) and Upjet's own
// file:line.

const upjetSlice = "hack/upjet/provider-upjet-aws.json"

type upjetData struct {
	Provider struct {
		Repo   string `json:"repo"`
		Tag    string `json:"tag"`
		Commit string `json:"commit"`
	} `json:"provider"`
	Entries []*upjetEntry `json:"entries"`
	NoUpjet []string      `json:"noUpjet"`
}

type upjetEntry struct {
	Ours struct {
		Service       string   `json:"service"`
		Resource      string   `json:"resource"`
		OfficialKinds []string `json:"officialKinds"`
		OursKinds     []string `json:"oursKinds"`
	} `json:"ours"`
	Facts       upjetFacts          `json:"facts"`
	TargetsOurs map[string][]string `json:"targetsOurs"`
	line        int
}

type upjetAt struct {
	Field    string `json:"field"`
	Property string `json:"property"`
	Value    string `json:"value"`
	What     string `json:"what"`
	At       string `json:"at"`
}

type upjetFacts struct {
	Terraform    string `json:"terraform"`
	Group        string `json:"group"`
	Kind         string `json:"kind"`
	Tested       bool   `json:"tested"`
	ExternalName struct {
		Style     string `json:"style"`
		Parameter string `json:"parameter"`
		Template  string `json:"template"`
		At        string `json:"at"`
	} `json:"externalName"`
	References      []*upjetRef     `json:"references"`
	LateInitIgnored []upjetAt       `json:"lateInitIgnored"`
	SchemaOverrides []upjetAt       `json:"schemaOverrides"`
	ListMapKeys     json.RawMessage `json:"listMapKeys"`
	CustomCode      []upjetAt       `json:"customCode"`
	Examples        []*upjetExample `json:"examples"`
}

type upjetRef struct {
	Field     string `json:"field"`
	Target    string `json:"target"`
	Extractor string `json:"extractor"`
	Source    string `json:"source"`
	At        string `json:"at"`
	line      int
}

type upjetExample struct {
	ID                 string         `json:"id"`
	At                 string         `json:"at"`
	Origin             string         `json:"origin"`
	ManualIntervention string         `json:"manualIntervention"`
	Fields             []string       `json:"fields"`
	RefFields          []string       `json:"refFields"`
	Values             map[string]any `json:"values"`
	line               int
}

// readUpjet reads the slice, or nil when the repository has none.
func readUpjet(root string) (*upjetData, error) {
	b, err := os.ReadFile(filepath.Join(root, upjetSlice))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var d upjetData
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("%s: %w", upjetSlice, err)
	}
	d.placeLines(strings.Split(string(b), "\n"))
	return &d, nil
}

// placeLines finds each entry, reference and example in the file, in
// order, for provenance (the slice is written one key per line).
func (d *upjetData) placeLines(lines []string) {
	i := 0
	find := func(from int, want ...string) int {
		for j := from; j < len(lines); j++ {
			if strings.Contains(lines[j], want[0]) {
				ok := true
				for k, w := range want[1:] {
					if j+1+k >= len(lines) || !strings.Contains(lines[j+1+k], w) {
						ok = false
						break
					}
				}
				if ok {
					return j
				}
			}
		}
		return -1
	}
	q := func(k, v string) string { b, _ := json.Marshal(v); return fmt.Sprintf("%q: %s", k, b) }
	for _, e := range d.Entries {
		j := find(i, q("terraform", e.Facts.Terraform))
		if j < 0 {
			continue
		}
		e.line, i = j+1, j+1
		for _, r := range e.Facts.References {
			if k := find(j, q("field", r.Field), q("target", r.Target)); k >= 0 {
				r.line = k + 1
			}
		}
		for _, x := range e.Facts.Examples {
			if k := find(j, q("at", x.At)); k >= 0 {
				x.line = k + 1
			}
		}
	}
}

func at(line int) string { return fmt.Sprintf("%s:%d", upjetSlice, line) }

// ---- what serves a reference's target here ----

// refTarget is the overlay's knowledge of an ACK kind for references
// (cloudcontrol/types.yaml `referenceTargets`): the path that holds the
// identifier Upjet's default extractor gives (Terraform's ID), read from
// the CRD of the ACK release in docs/coverage/sources.yaml.
type refTarget struct {
	Group      string `json:"group"`
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	TrimPrefix string `json:"trimPrefix,omitempty"`
}

type ackKind struct{ group, resource string }

type servedIndex struct {
	// ack: "<cfn service>/<Kind>" to ACK's group and plural, from
	// docs/coverage/sources.yaml and hack/coverage/mappings.yaml.
	ack map[string][]ackKind
	// cc: "<service>/<Resource>" to our Cloud Control kind.
	cc map[string]*ccKind
	// idPaths: "<group>/<Kind>" to the overlay's referenceTargets.
	idPaths map[string]refTarget
	// byTF: Terraform resource to its fold entries.
	byTF map[string][]*upjetEntry
}

type ccKind struct {
	group, kind string
	schema      *cfn.Schema
}

func readACKKinds(root string) (map[string][]ackKind, error) {
	var src struct {
		ACK struct {
			Controllers []struct {
				Name string   `json:"name"`
				CRDs []string `json:"crds"`
			} `json:"controllers"`
		} `json:"ack"`
	}
	b, err := os.ReadFile(filepath.Join(root, "docs/coverage/sources.yaml"))
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, &src); err != nil {
		return nil, err
	}
	var maps struct {
		Services map[string]string `json:"services"`
	}
	if mb, err := os.ReadFile(filepath.Join(root, "hack/coverage/mappings.yaml")); err == nil {
		_ = yaml.Unmarshal(mb, &maps)
	}
	out := map[string][]ackKind{}
	for _, c := range src.ACK.Controllers {
		svc := c.Name
		if m := maps.Services[svc]; m != "" {
			svc = m
		}
		for _, f := range c.CRDs {
			base := strings.TrimSuffix(filepath.Base(f), ".yaml")
			group, plural, ok := strings.Cut(base, "_")
			if !ok {
				continue
			}
			out[svc+"/"+plural] = append(out[svc+"/"+plural], ackKind{group, plural})
		}
	}
	return out, nil
}

// arnExtractor: the reference copies the target's ARN.
func arnExtractor(x string) bool {
	return strings.Contains(x, "ARNExtractor") || strings.Contains(x, `ExtractParamPath("arn"`)
}

// idExtractor: the reference copies the target's Terraform ID.
func idExtractor(x string) bool {
	return x == "" || strings.HasSuffix(x, "resource.ExtractResourceID()")
}

var paramRE = regexp.MustCompile(`ExtractParamPath\("([^"]+)"`)

// derived is one reference the Upjet facts give a served type, or why not.
type derived struct {
	ref      cfn.Reference
	field    string // Terraform field
	target   string // Terraform resource
	slice    int    // line in the slice
	upstream string // Upjet's file:line
	outcome  string // applied, agrees, disagrees, skipped
	reason   string
}

// deriveReferences turns the Upjet references of a served type's fold
// entries into reference candidates.
func (ix *servedIndex) deriveReferences(s *cfn.Schema, entries []*upjetEntry) []*derived {
	names, _ := cfn.Names(s.Properties)
	lower := map[string]string{}
	for p := range s.Properties {
		lower[strings.ToLower(p)] = p
	}
	ro := s.Sets().ReadOnly
	var out []*derived
	for _, e := range entries {
		for _, r := range e.Facts.References {
			d := &derived{field: r.Field, target: r.Target, slice: r.line, upstream: r.At, outcome: "skipped"}
			out = append(out, d)
			prop := lower[strings.ReplaceAll(r.Field, "_", "")]
			switch {
			case strings.ContainsAny(r.Field, ".[*"):
				d.reason = "nested field: references here are top-level properties only"
				continue
			case prop == "":
				d.reason = "no top-level property of that name in the CloudFormation schema"
				continue
			}
			d.ref.Property = "/properties/" + prop
			d.ref.Field = names.ToField[prop]
			if ro.Has(d.ref.Property) {
				d.reason = "the property is read-only"
				continue
			}
			if rp, _ := s.Resolve(s.Properties[prop]); rp == nil || rp.Type.Single() != "string" {
				d.reason = "not a string property (a list of IDs, or an object)"
				continue
			}
			if len(e.TargetsOurs[r.Target]) == 0 {
				d.reason = "the target " + r.Target + " is not served here"
				continue
			}
			ix.resolveTarget(d, prop, r)
		}
	}
	return out
}

// resolveTarget picks the kind serving the target (ACK's first, then our
// Cloud Control kind) and the path of the value the extractor names.
func (ix *servedIndex) resolveTarget(d *derived, prop string, r *upjetRef) {
	isARN, isID := arnExtractor(r.Extractor), idExtractor(r.Extractor)
	wantsARN := strings.HasSuffix(prop, "Arn") || strings.HasSuffix(prop, "ARN")
	switch {
	case !isARN && !isID:
		p := r.Extractor
		if m := paramRE.FindStringSubmatch(p); m != nil {
			p = m[1]
		}
		d.reason = "the extractor copies " + p + ", neither the target's ARN nor its ID"
		return
	case wantsARN && !isARN:
		d.reason = "the property takes an ARN; Upjet's reference copies the target's ID (stricter rule: not wired)"
		return
	case isARN && strings.HasSuffix(prop, "Name"):
		d.reason = "the property takes a name; Upjet's reference copies the target's ARN (stricter rule: not wired)"
		return
	}
	var reasons []string
	for _, te := range ix.byTF[r.Target] {
		svc := te.Ours.Service
		for _, k := range te.Ours.OfficialKinds {
			cands := ix.ack[svc+"/"+cfn.KindPlural(k)]
			if len(cands) != 1 {
				reasons = append(reasons, fmt.Sprintf("ACK's %s: %d CRDs match in docs/coverage/sources.yaml", k, len(cands)))
				continue
			}
			c := cands[0]
			d.ref.Group, d.ref.Kind, d.ref.Resource = c.group, k, c.resource
			if isARN {
				d.ref.Path = "status.ackResourceMetadata.arn"
				d.outcome = "applied"
				return
			}
			if t, ok := ix.idPaths[c.group+"/"+k]; ok {
				d.ref.Path, d.ref.TrimPrefix = t.ID, t.TrimPrefix
				d.outcome = "applied"
				return
			}
			reasons = append(reasons, fmt.Sprintf("ACK's %s.%s: its ID path is not in referenceTargets (cloudcontrol/types.yaml)", k, c.group))
		}
		for _, k := range te.Ours.OursKinds {
			if !strings.HasSuffix(k, "(Cloud Control)") {
				reasons = append(reasons, "our ACK-generated "+k+": not wired")
				continue
			}
			cc := ix.cc[svc+"/"+te.Ours.Resource]
			if cc == nil {
				continue
			}
			if why := ccPath(d, prop, cc, isARN); why != "" {
				reasons = append(reasons, why)
				continue
			}
			d.outcome = "applied"
			return
		}
	}
	d.ref.Group, d.ref.Kind, d.ref.Resource = "", "", ""
	if len(reasons) == 0 {
		reasons = append(reasons, "no kind for "+r.Target+" here")
	}
	d.reason = strings.Join(reasons, "; ")
}

// ccPath fills the path for a target served by our Cloud Control kind.
func ccPath(d *derived, prop string, cc *ccKind, isARN bool) string {
	s := cc.schema
	d.ref.Group, d.ref.Kind, d.ref.Resource = cc.group, cc.kind, cfn.KindPlural(cc.kind)
	if isARN {
		for p := range s.Properties {
			if strings.EqualFold(p, "arn") {
				d.ref.Path = "status.ackResourceMetadata.arn"
				return ""
			}
		}
		return cc.kind + " (Cloud Control) has no Arn property"
	}
	ids := s.PrimaryIdentifierNames()
	if len(ids) != 1 {
		return fmt.Sprintf("%s (Cloud Control) has a compound primary identifier (%s)", cc.kind, strings.Join(ids, "|"))
	}
	id := ids[0]
	// The property must take what the identifier is: the same name, or
	// a name ending in it (RestApiId and the identifier RestApiId; ApiId
	// and Id).
	if !strings.HasSuffix(strings.ToLower(prop), strings.ToLower(id)) {
		return fmt.Sprintf("%s (Cloud Control) is identified by %s, which %s may not take", cc.kind, id, prop)
	}
	if (strings.HasSuffix(id, "Arn") || strings.HasSuffix(id, "ARN")) != (strings.HasSuffix(prop, "Arn") || strings.HasSuffix(prop, "ARN")) {
		return fmt.Sprintf("%s (Cloud Control) is identified by %s, which %s may not take", cc.kind, id, prop)
	}
	names, _ := cfn.Names(s.Properties)
	if s.Sets().ReadOnly.Has("/properties/" + id) {
		d.ref.Path = "status." + names.ToField[id]
	} else {
		d.ref.Path = "spec." + names.ToField[id]
	}
	return ""
}

// mergeReferences puts the overlay's own references first and adds the
// derived ones the schema accepts; a derived reference on a property the
// overlay already names is compared, never applied.
func mergeReferences(s *cfn.Schema, hand []cfn.Reference, ds []*derived) []cfn.Reference {
	byProp := map[string]cfn.Reference{}
	for _, h := range hand {
		byProp[h.Property] = h
	}
	merged := append([]cfn.Reference(nil), hand...)
	// Upjet's own references to one property, from more than one of its
	// resources, must agree.
	first := map[string]*derived{}
	for _, d := range ds {
		if d.outcome != "applied" {
			continue
		}
		if h, ok := byProp[d.ref.Property]; ok {
			if h.Group == d.ref.Group && h.Kind == d.ref.Kind && h.Path == d.ref.Path {
				d.outcome, d.reason = "agrees", "types.yaml names the same kind and path"
			} else {
				d.outcome = "disagrees"
				d.reason = fmt.Sprintf("types.yaml names %s.%s %s; Upjet's gives %s.%s %s (types.yaml kept)", h.Kind, h.Group, h.Path, d.ref.Kind, d.ref.Group, d.ref.Path)
			}
			continue
		}
		if f := first[d.ref.Property]; f != nil {
			if f.ref.Group == d.ref.Group && f.ref.Kind == d.ref.Kind && f.ref.Path == d.ref.Path {
				d.outcome, d.reason = "agrees", fmt.Sprintf("the same reference as %s's", f.field)
			} else {
				d.outcome = "disagrees"
				d.reason = fmt.Sprintf("Upjet's resources disagree: %s.%s and %s.%s (neither wired)", f.ref.Kind, f.ref.Group, d.ref.Kind, d.ref.Group)
				f.outcome, f.reason = "disagrees", d.reason
			}
			continue
		}
		first[d.ref.Property] = d
	}
	var props []string
	for p, d := range first {
		if d.outcome == "applied" {
			props = append(props, p)
		}
	}
	sort.Strings(props)
	for _, p := range props {
		d := first[p]
		r := d.ref
		r.Field, r.RefField = "", ""
		if _, err := s.References(append(append([]cfn.Reference(nil), merged...), r)); err != nil {
			d.outcome, d.reason = "skipped", err.Error()
			continue
		}
		merged = append(merged, r)
	}
	return merged
}
