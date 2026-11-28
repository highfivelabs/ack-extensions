// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// The Upjet facts in the coverage report (HFL-699, HFL-700, HFL-701):
// docs/coverage/upjet.md says what the facts turned into (counted from
// cloudcontrol/zz_generated.upjet.csv, which hack/cloudcontrol-gen writes),
// docs/coverage/tested.csv is the per-kind "tested where" ledger with
// Upjet's tested flag beside it, and docs/coverage/upjet-gaps.csv ranks the
// API resources Upjet has a resource for and nothing here serves.

const upjetSlicePath = "hack/upjet/provider-upjet-aws.json"

type upjetSliceEntry struct {
	Ours struct {
		Service       string   `json:"service"`
		Resource      string   `json:"resource"`
		OfficialKinds []string `json:"officialKinds"`
		OursKinds     []string `json:"oursKinds"`
	} `json:"ours"`
	Facts struct {
		Terraform    string `json:"terraform"`
		Group        string `json:"group"`
		Kind         string `json:"kind"`
		Tested       bool   `json:"tested"`
		ExternalName struct {
			Style string `json:"style"`
		} `json:"externalName"`
		References []struct {
			Field     string `json:"field"`
			Target    string `json:"target"`
			Extractor string `json:"extractor"`
			At        string `json:"at"`
		} `json:"references"`
		Examples []struct {
			Origin             string `json:"origin"`
			ManualIntervention string `json:"manualIntervention"`
		} `json:"examples"`
	} `json:"facts"`
	TargetsOurs map[string][]string `json:"targetsOurs"`
}

type upjetSliceFile struct {
	Provider struct {
		Repo string `json:"repo"`
		Tag  string `json:"tag"`
	} `json:"provider"`
	Counts  map[string]int     `json:"counts"`
	Entries []*upjetSliceEntry `json:"entries"`
	NoUpjet []string           `json:"noUpjet"`
}

func readCSVFile(path string) ([][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return csv.NewReader(f).ReadAll()
}

// liveRow is one row of docs/live-runs.md's "Kinds" table.
type liveRow struct{ route, service, kind, test, live string }

func readLiveKinds(root string) map[string]liveRow {
	out := map[string]liveRow{}
	b, err := os.ReadFile(filepath.Join(root, "docs/live-runs.md"))
	if err != nil {
		return out
	}
	s := string(b)
	i := strings.Index(s, "\n## Kinds")
	if i < 0 {
		return out
	}
	for _, l := range strings.Split(s[i:], "\n") {
		if !strings.HasPrefix(l, "| ") || strings.HasPrefix(l, "| Route") || strings.HasPrefix(l, "| ---") {
			continue
		}
		c := strings.Split(strings.Trim(l, "|"), "|")
		if len(c) < 5 {
			continue
		}
		r := liveRow{strings.TrimSpace(c[0]), strings.TrimSpace(c[1]), strings.TrimSpace(c[2]), strings.TrimSpace(c[3]), strings.TrimSpace(strings.Join(c[4:], "|"))}
		out[r.service+"/"+strings.Fields(r.kind + " x")[0]] = r
	}
	return out
}

// ackGenRefs is every reference field in services/*/generator.yaml, by
// "<service>/<Resource>", lower-cased and without dots or underscores.
func ackGenRefs(root string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(root, "services", "*", "generator.yaml"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var g struct {
			Resources map[string]struct {
				Fields map[string]struct {
					References map[string]any `json:"references"`
				} `json:"fields"`
			} `json:"resources"`
		}
		if yaml.Unmarshal(b, &g) != nil {
			continue
		}
		svc := filepath.Base(filepath.Dir(f))
		for r, v := range g.Resources {
			for fn, fv := range v.Fields {
				if fv.References == nil {
					continue
				}
				k := svc + "/" + r
				if out[k] == nil {
					out[k] = map[string]bool{}
				}
				out[k][flat(fn)] = true
			}
		}
	}
	return out
}

func flat(s string) string {
	return strings.ToLower(strings.NewReplacer(".", "", "_", "", "[*]", "").Replace(s))
}

// upjetOutputs writes upjet.md, tested.csv and upjet-gaps.csv, or nothing
// when the repository has no slice.
func (m *model) upjetOutputs(c config, r *results, files map[string][]byte) error {
	raw, err := os.ReadFile(filepath.Join(c.root, upjetSlicePath))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var up upjetSliceFile
	if err := json.Unmarshal(raw, &up); err != nil {
		return err
	}
	facts, err := readCSVFile(filepath.Join(c.root, "cloudcontrol/zz_generated.upjet.csv"))
	if err != nil {
		return err
	}
	// Counts from the generator's facts.
	cnt := map[string]int{}
	exampleOnFake := map[string]int{} // type -> fixtures
	upjetTested := map[string]string{}
	for _, f := range facts[1:] {
		typ, check, outcome := f[0], f[2], f[4]
		cnt[check+"/"+outcome]++
		if check == "example" && outcome == "fixture" {
			exampleOnFake[typ]++
		}
		if check == "entry" && outcome != "none" {
			if upjetTested[typ] != "no" {
				upjetTested[typ] = map[bool]string{true: "yes", false: "no"}[outcome == "tested"]
			}
		}
	}
	refTypesAfter, refTypesBefore := 0, 0
	byType := map[string][2]int{} // applied, hand
	for _, f := range facts[1:] {
		if f[2] == "reference" && f[4] == "applied" {
			v := byType[f[0]]
			v[0]++
			byType[f[0]] = v
		}
	}
	reg, _ := os.ReadFile(filepath.Join(c.root, "cloudcontrol/pkg/registry/zz_generated.registry.go"))
	for _, l := range strings.Split(string(reg), "\n") {
		i := strings.Index(l, `TypeName: "`)
		if i < 0 || !strings.Contains(l, "References: ") {
			continue
		}
		typ := strings.SplitN(l[i+len(`TypeName: "`):], `"`, 2)[0]
		n := strings.Count(l, "{Property: ")
		refTypesAfter++
		if n > byType[typ][0] {
			refTypesBefore++
		}
	}

	// The per-kind ledger (HFL-701).
	live := readLiveKinds(c.root)
	var tested [][]string
	tested = append(tested, []string{"route", "service", "kind", "fake", "live", "needs_person", "upjet_resource", "upjet_tested", "upjet_examples_on_fake"})
	entriesByKey := map[string][]*upjetSliceEntry{}
	for _, e := range up.Entries {
		k := e.Ours.Service + "/" + e.Ours.Resource
		entriesByKey[k] = append(entriesByKey[k], e)
	}
	upjetFor := func(res string) (string, string) {
		var tfs []string
		t := ""
		for _, e := range entriesByKey[res] {
			tfs = append(tfs, e.Facts.Terraform)
			if !e.Facts.Tested {
				t = "no"
			} else if t == "" {
				t = "yes"
			}
		}
		if len(tfs) == 0 {
			return "", "none"
		}
		return strings.Join(tfs, " "), t
	}
	fakeCounts := map[string]int{}
	var keys []string
	for k := range m.ours {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, cv := range m.ours[k] {
			route, fake, person := "ACK", "moto", ""
			typ := ""
			if cv.CloudControl {
				route = "Cloud Control"
				typ = cv.CRD.Annotations[ccTypeNameAnnotation]
				if cv.Unproven != "" {
					fake = "schema fake only"
					person = "unproven on moto: " + cv.Unproven
				}
			}
			lv := "never"
			svc := strings.SplitN(k, "/", 2)[0]
			if row, ok := live[cv.Svc+"/"+cv.Kind]; ok && row.route == route {
				lv = row.live
				if strings.HasPrefix(row.test, "(none") {
					fake = "none"
				}
				if strings.HasPrefix(lv, "pending") {
					person = strings.TrimSpace(strings.TrimPrefix(lv, "pending:"))
				}
			} else if row, ok := live[svc+"/"+cv.Kind]; ok && row.route == route {
				lv = row.live
			}
			fakeCounts[fake]++
			res, ut := upjetFor(k)
			ex := ""
			if typ != "" {
				ex = itoa(exampleOnFake[typ])
			}
			tested = append(tested, []string{route, svc, cv.Kind, fake, lv, person, res, ut, ex})
		}
	}
	files["docs/coverage/tested.csv"] = writeCSV(tested)

	// The ACK-generated kinds of ours: Upjet's references against
	// generator.yaml's.
	gen := ackGenRefs(c.root)
	type ackRow struct{ kind, field, target, has string }
	var ackRows []ackRow
	ackSvc := map[string]string{"logs": "cloudwatchlogs", "events": "eventbridge"}
	for _, e := range up.Entries {
		for _, k := range e.Ours.OursKinds {
			if strings.HasSuffix(k, "(Cloud Control)") {
				continue
			}
			svc := e.Ours.Service
			if a := ackSvc[svc]; a != "" {
				svc = a
			}
			refs := gen[svc+"/"+k]
			for _, rf := range e.Facts.References {
				if len(e.TargetsOurs[rf.Target]) == 0 {
					continue
				}
				has := "missing"
				for f := range refs {
					if strings.HasSuffix(f, flat(rf.Field)) || strings.HasSuffix(flat(rf.Field), f) {
						has = "has"
					}
				}
				ackRows = append(ackRows, ackRow{svc + "/" + k, rf.Field, rf.Target + " (" + strings.Join(e.TargetsOurs[rf.Target], ", ") + ")", has})
			}
		}
	}
	sort.Slice(ackRows, func(i, j int) bool {
		if ackRows[i].kind != ackRows[j].kind {
			return ackRows[i].kind < ackRows[j].kind
		}
		return ackRows[i].field < ackRows[j].field
	})

	// The gaps: Upjet has a resource, nothing here serves it.
	ready := map[string][2]string{}
	if rows, err := readCSVFile(filepath.Join(c.root, "cloudcontrol/readiness.csv")); err == nil {
		for _, row := range rows[1:] {
			ready[row[1]+"/"+row[2]] = [2]string{row[4], row[7]}
		}
	}
	type gap struct {
		key, tf, kind, tested, style, manual, prov, crd, inline string
		tier, refs, served, hand                                int
		doubt                                                   bool
	}
	fnd, err := loadFoundation()
	if err != nil {
		return err
	}
	// The slice's join is factory's, frozen at the fold: a resource it
	// lists with no kind may have one now, so the gaps are also checked
	// against this run's resources.csv rows.
	servedNow := map[string]bool{}
	for _, rr := range r.res {
		if len(rr.officialKinds)+len(rr.oursKinds) > 0 {
			servedNow[rr.service+"/"+rr.resource] = true
		}
	}
	var gaps []gap
	for k, es := range entriesByKey {
		e0 := es[0]
		if len(e0.Ours.OfficialKinds)+len(e0.Ours.OursKinds) > 0 || servedNow[k] {
			continue
		}
		svc := e0.Ours.Service
		g := gap{key: k, tier: 3, tested: "yes"}
		if p, ok := m.priority[svc]; ok {
			g.tier = p.tier
		}
		var tfs, kinds []string
		for _, e := range es {
			tfs = append(tfs, e.Facts.Terraform)
			if e.Facts.Kind != "" {
				kinds = append(kinds, e.Facts.Group+"/"+e.Facts.Kind)
			}
			if !e.Facts.Tested {
				g.tested = "no"
			}
			g.style = e.Facts.ExternalName.Style
			g.refs += len(e.Facts.References)
			for _, rf := range e.Facts.References {
				if len(e.TargetsOurs[rf.Target]) > 0 {
					g.served++
				}
			}
			for _, x := range e.Facts.Examples {
				if x.Origin == "hand" {
					g.hand++
					if x.ManualIntervention != "" && g.manual == "" {
						g.manual = x.ManualIntervention
					}
				}
			}
		}
		g.tf, g.kind = strings.Join(tfs, " "), strings.Join(kinds, " ")
		rd := ready[k]
		g.prov, g.crd = rd[0], rd[1]
		g.inline = fnd.Resources[k].Inline
		// A Terraform resource that names neither our service nor sits in
		// Upjet's group of it is likely a mapping slip (aws_instance onto
		// sso/Instance): flagged, ranked after the rest of its tier.
		match := false
		for _, e := range es {
			match = match || strings.Contains(strings.ReplaceAll(e.Facts.Terraform, "_", ""), svc) || strings.HasPrefix(e.Facts.Group, svc+".")
		}
		if !match {
			g.doubt = true
		}
		gaps = append(gaps, g)
	}
	score := func(g gap) int {
		s := (4 - g.tier) * 100
		if g.inline != "" {
			// Covered as a field of another kind: not a gap in practice.
			s -= 1000
		}
		if g.doubt {
			s -= 90
		}
		if g.tested == "yes" {
			s += 20
		}
		if g.crd == "ok" && g.prov != "NON_PROVISIONABLE" && g.prov != "" {
			s += 30
		}
		if g.hand > 0 {
			s += 10
		}
		return s + g.served*2
	}
	sort.Slice(gaps, func(i, j int) bool {
		si, sj := score(gaps[i]), score(gaps[j])
		if si != sj {
			return si > sj
		}
		return gaps[i].key < gaps[j].key
	})
	gapRows := [][]string{{"rank", "service", "resource", "tier", "terraform", "upjet_kind", "upjet_tested", "external_name", "references", "references_to_served", "hand_examples", "manual_intervention", "cloudformation_provisioning", "cloudcontrol_crd", "covered_inline", "check_mapping"}}
	for i, g := range gaps {
		sr := strings.SplitN(g.key, "/", 2)
		gapRows = append(gapRows, []string{itoa(i + 1), sr[0], sr[1], itoa(g.tier), g.tf, g.kind, g.tested, g.style, itoa(g.refs), itoa(g.served), itoa(g.hand), g.manual, g.prov, g.crd, g.inline, fmt.Sprint(g.doubt)})
	}
	files["docs/coverage/upjet-gaps.csv"] = writeCSV(gapRows)

	// upjet.md
	var b bytes.Buffer
	w := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }
	w("<!-- Generated by hack/coverage/run.sh. DO NOT EDIT. -->\n\n# Upjet facts: what they turned into\n\n")
	w("[`%s`](../../%s) is factory's fold of %s %s onto our API resources (HFL-697): references, ID formats, late-init ignores, secrets, the maintainers' tested flag and examples, as data. Nothing runs a Crossplane provider. %d of our %d API resources have an Upjet resource; %d of ours have none (`noUpjet`). This page counts what the repository's generators and tests now take from it; the details are [`cloudcontrol/zz_generated.upjet.md`](../../cloudcontrol/zz_generated.upjet.md).\n\n",
		upjetSlicePath, upjetSlicePath, up.Provider.Repo, up.Provider.Tag, up.Counts["matched"], up.Counts["ours"], len(up.NoUpjet))
	w("| | Before | After |\n| --- | ---: | ---: |\n")
	w("| Cloud Control kinds with a reference field | %d | %d |\n", refTypesBefore, refTypesAfter)
	w("| Upjet references applied (reference fields) | 0 | %d |\n", cnt["reference/applied"])
	w("| Upjet references that agree with the overlay's | | %d |\n", cnt["reference/agrees"])
	w("| Upjet hand examples running on the fake (`TestUpjetExamples`) | 0 | %d |\n", cnt["example/fixture"])
	w("| Upjet hand examples the kind cannot express (listed) | | %d |\n", cnt["example/not-expressible"])
	w("| ID format checks (agree / differ / other) | 0 | %d / %d / %d |\n", cnt["id/agrees"], cnt["id/differs"], cnt["id/listed"])
	w("| Late-init checks | 0 | %d |\n", cnt["late-init/agrees"]+cnt["late-init/n/a"])
	w("| Secret checks (exposed / listed / not top-level) | 0 | %d / %d / %d |\n", cnt["secret/exposed"], cnt["secret/listed"], cnt["secret/n/a"])
	w("| Disagreements found (listed, not applied) | 0 | %d |\n", cnt["reference/disagrees"])
	w("| Upjet resources on served Cloud Control types that Upjet does not test | | %d |\n\n", cnt["entry/not-tested"])

	w("## Tested where (HFL-701)\n\n[tested.csv](tested.csv) has one row per kind of ours: the fake it runs on in `task check` (`moto`, or `schema fake only` when moto lacks the API), its live result from [`docs/live-runs.md`](../live-runs.md) (`never` without a row), what needs a person, and Upjet's own flag beside it (`upjet_tested`: whether the maintainers test the resource; `none` when Upjet has none) with the Upjet examples that run on our fake.\n\n| Fake | Kinds |\n| --- | ---: |\n")
	for _, f := range sortedStrings(fakeCounts) {
		w("| %s | %d |\n", f, fakeCounts[f])
	}
	ut := map[string]int{}
	for _, row := range tested[1:] {
		ut[row[7]]++
	}
	w("\nUpjet's flag on the same kinds: tested %d, not tested %d, no Upjet resource %d.\n\n", ut["yes"], ut["no"], ut["none"])

	w("## Our ACK-generated kinds: Upjet's references against `generator.yaml`\n\nA reference Upjet gives one of our ACK-generated kinds, to a target served here, and whether `services/<svc>/generator.yaml` has a reference on that field. `missing` is a candidate for `generator.yaml` (regenerated with `task generate`); none was added here.\n\n| Kind | Upjet field | Target | Here |\n| --- | --- | --- | --- |\n")
	missing := 0
	for _, a := range ackRows {
		w("| `%s` | `%s` | %s | %s |\n", a.kind, a.field, a.target, a.has)
		if a.has == "missing" {
			missing++
		}
	}
	w("\n%d of %d have no reference here.\n\n", missing, len(ackRows))

	w("## Gaps Upjet has a resource for (ranked)\n\nAPI resources with an Upjet resource and no kind here, ACK's or ours: %d. Ranked by tier (a resource `foundation.yaml` marks covered inline, as a field of another kind, last), then whether Cloud Control could serve it (provisionable, CRD accepted), Upjet's tested flag, a hand example, and references to kinds served here. All are in [upjet-gaps.csv](upjet-gaps.csv). The join inherits the catalog mapping's precision (a name and attribute match): check the Terraform name before starting one. The first 30:\n\n", len(gaps))
	w("| Rank | Resource | Tier | Upjet | Tested | References (to served) | Hand examples | Cloud Control |\n| ---: | --- | ---: | --- | --- | ---: | ---: | --- |\n")
	for i, g := range gaps {
		if i == 30 {
			break
		}
		cc := g.prov
		if g.inline != "" {
			cc += "; covered inline: " + firstN(g.inline, 60)
		}
		if g.doubt {
			cc += "; **check the mapping**"
		}
		if g.crd != "" && g.crd != "ok" {
			cc += ", CRD: " + strings.ReplaceAll(firstN(g.crd, 60), "|", "\\|")
		}
		w("| %d | `%s` | %d | `%s` | %s | %d (%d) | %d | %s |\n", i+1, g.key, g.tier, g.tf, g.tested, g.refs, g.served, g.hand, cc)
	}
	files["docs/coverage/upjet.md"] = b.Bytes()
	return nil
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func sortedStrings(m map[string]int) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
