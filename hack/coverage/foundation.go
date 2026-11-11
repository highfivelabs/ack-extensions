// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	_ "embed"
	"encoding/csv"
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

//go:embed foundation.yaml
var foundationYAML []byte

//go:embed module-usage.csv
var moduleUsageCSV string

// foundationFile is hack/coverage/foundation.yaml: what the generator cannot
// compute and a person (or an agent that read upstream) wrote down for
// docs/coverage/foundation.md.
type foundationFile struct {
	Researched string               `json:"researched"`
	Sources    []string             `json:"sources"`
	Services   map[string]fService  `json:"services"`
	Resources  map[string]fResource `json:"resources"`
	Queue      []fItem              `json:"queue"`
}

type fService struct {
	Rank int `json:"rank"`
	// Core lists the resources ranked with the service; when set, the rest of
	// the service's resources rank after every other service.
	Core       []string `json:"core"`
	Controller string   `json:"controller"` // ACK's controller, in words
	Ignored    []string `json:"ignored"`    // resources ACK's generator.yaml ignores
	Upstream   string   `json:"upstream"`   // open PRs and community issues
	Moto       string   `json:"moto"`       // what moto 5.2.5 implements
	NewService bool     `json:"newService"` // no manager here yet: one more unit of work
}

type fResource struct {
	Size     string   `json:"size"`     // S, M or L; overrides the estimate from attribute count
	Inline   string   `json:"inline"`   // where ACK (or ours) covers it as a field of another kind
	Upstream string   `json:"upstream"` // overrides the derived text
	Moto     string   `json:"moto"`     // overrides the service's
	Kinds    string   `json:"kinds"`    // the proposed kind(s), overrides the derived text
	Matters  []string `json:"matters"`  // the missing attributes that matter, overrides the first few
	Note     string   `json:"note"`
}

type fItem struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Resources []string `json:"resources"` // "service/Resource" keys; the item's numbers are summed over them
	Why       string   `json:"why"`
	Upstream  string   `json:"upstream"`
	Moto      string   `json:"moto"`
	Modules   string   `json:"modules"`
	Size      string   `json:"size"`
	Kinds     string   `json:"kinds"`
	Approach  string   `json:"approach"`
	Done      string   `json:"done"`
}

func loadFoundation() (*foundationFile, error) {
	var f foundationFile
	if err := yaml.UnmarshalStrict(foundationYAML, &f); err != nil {
		return nil, fmt.Errorf("hack/coverage/foundation.yaml: %w", err)
	}
	return &f, nil
}

// moduleNeed maps a CloudFormation resource key to the tracked modules whose
// Terraform creates it.
func (m *model) moduleNeed() (map[string][]string, error) {
	rows, err := csv.NewReader(strings.NewReader(moduleUsageCSV)).ReadAll()
	if err != nil {
		return nil, err
	}
	norm := map[string][]string{}
	for _, k := range m.cfnKeys {
		r := m.cfn[k]
		norm[normName(r.Service)+normName(r.Name)] = append(norm[normName(r.Service)+normName(r.Name)], k)
	}
	need := map[string]map[string]bool{}
	add := func(key, module string) {
		if need[key] == nil {
			need[key] = map[string]bool{}
		}
		need[key][module] = true
	}
	for _, r := range rows[1:] {
		module, tf := r[0], r[1]
		if t, ok := m.maps.Terraform[tf]; ok {
			for _, k := range t {
				if k == outside {
					continue
				}
				if _, ok := m.cfn[k]; !ok {
					return nil, fmt.Errorf("mappings.yaml: terraform %s maps to %s, which is not a CloudFormation resource", tf, k)
				}
				add(k, module)
			}
			continue
		}
		n := normName(strings.TrimPrefix(tf, "aws_"))
		for _, cand := range []string{n, "ec2" + n, "ec2" + strings.TrimPrefix(n, "vpc")} {
			if ks, ok := norm[cand]; ok && len(ks) == 1 {
				add(ks[0], module)
				break
			}
		}
	}
	out := map[string][]string{}
	for k, ms := range need {
		for mod := range ms {
			out[k] = append(out[k], mod)
		}
		sort.Strings(out[k])
	}
	return out, nil
}

// sizeOf estimates the size of building a resource from its attribute count.
func sizeOf(attrs int) string {
	switch {
	case attrs <= 15:
		return "S"
	case attrs <= 60:
		return "M"
	}
	return "L"
}

func sizeRank(s string) int {
	switch s {
	case "S":
		return 0
	case "M":
		return 1
	}
	return 2
}

type fRow struct {
	key, service, name string
	cat                string
	group              float64 // the service's rank, plus 1000 for the non-core resources of a service with a core
	total, off, ours   int
	none               int
	official           []string
	oursKinds          []string
	release            string
	modules            []string
	size               string
	gap                bool // something to build
	item               string
	itemRank           int // the queue position of the item that builds it; 0 if none
	res                fResource
	inline             string
}

func (m *model) foundationRows(r *results, f *foundationFile, need map[string][]string) []fRow {
	itemOf := map[string]string{}
	itemIdx := map[string]int{}
	for i, it := range f.Queue {
		itemIdx[it.ID] = i + 1
		for _, k := range it.Resources {
			if _, ok := itemOf[k]; !ok {
				itemOf[k] = it.ID
			}
		}
	}
	var rows []fRow
	for _, rr := range r.res {
		if rr.outside {
			continue
		}
		cat := r.categoryOf(rr.service)
		if cat != catPlatform && cat != catSecurity {
			continue
		}
		key := rr.service + "/" + rr.resource
		fr := fRow{key: key, service: rr.service, name: rr.resource, cat: cat, total: rr.total, off: rr.official, ours: rr.oursBeyond, none: rr.uncovered,
			official: rr.officialKinds, oursKinds: rr.oursKinds, modules: need[key], item: itemOf[key]}
		fr.res = f.Resources[key]
		fr.itemRank = itemIdx[fr.item]
		fr.inline = fr.res.Inline
		svc := f.Services[rr.service]
		fr.group = float64(svc.Rank)
		if len(svc.Core) > 0 {
			core := false
			for _, c := range svc.Core {
				if c == rr.resource {
					core = true
				}
			}
			if !core {
				fr.group += 1000
			}
		}
		if fr.group == 0 {
			fr.group = 500 // a service nobody ranked
		}
		if fr.res.Size != "" {
			fr.size = fr.res.Size
		} else {
			fr.size = sizeOf(rr.total)
		}
		fr.gap = rr.uncovered > 0 && fr.inline == ""
		for _, cv := range m.official[key] {
			fr.release = cv.Release
		}
		rows = append(rows, fr)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.cat != b.cat {
			return a.cat == catPlatform
		}
		if a.group != b.group {
			return a.group < b.group
		}
		if a.gap != b.gap {
			return a.gap
		}
		if a.gap && a.itemRank != b.itemRank {
			// Resources a queue item builds come first, in queue order.
			if a.itemRank == 0 || b.itemRank == 0 {
				return a.itemRank != 0
			}
			return a.itemRank < b.itemRank
		}
		if len(a.modules) != len(b.modules) {
			return len(a.modules) > len(b.modules)
		}
		if sizeRank(a.size) != sizeRank(b.size) {
			return sizeRank(a.size) < sizeRank(b.size)
		}
		return strings.ToLower(a.name) < strings.ToLower(b.name)
	})
	return rows
}

// bullets keeps a multi-line value inside its list item: the first line stays
// on the item, the rest become nested bullets.
func bullets(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] = "  - " + strings.TrimSpace(lines[i])
	}
	return strings.Join(lines, "\n")
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func mdEscape(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ").Replace(s)
}

func (fr fRow) missing(m *model, matters map[string][]string) string {
	if fr.total == 0 {
		return "no writable properties in CloudFormation"
	}
	if fr.none == 0 {
		return "none"
	}
	if fr.inline != "" {
		return fmt.Sprintf("%s, covered inline: %s", plural(fr.none, "attr"), fr.inline)
	}
	if fr.off == 0 && fr.ours == 0 {
		return fmt.Sprintf("whole resource (%s)", plural(fr.total, "attr"))
	}
	list := fr.res.Matters
	if len(list) == 0 {
		list = matters[fr.key]
	}
	more := ""
	if fr.none > len(list) {
		more = fmt.Sprintf(" and %d more", fr.none-len(list))
	}
	return fmt.Sprintf("%d of %d attrs: %s%s", fr.none, fr.total, "`"+strings.Join(list, "`, `")+"`", more)
}

// uncoveredAttrs lists, per resource, the uncovered attributes: required ones
// first, then shallow ones, at most six.
func (r *results) uncoveredAttrs() map[string][]string {
	type a struct {
		path  string
		req   bool
		depth int
	}
	by := map[string][]a{}
	for _, x := range r.attrs {
		if x.covered == byNone {
			k := x.service + "/" + x.resource
			by[k] = append(by[k], a{x.a.Path, x.a.Required, strings.Count(x.a.Path, ".") + strings.Count(x.a.Path, "[]")})
		}
	}
	out := map[string][]string{}
	for k, l := range by {
		sort.SliceStable(l, func(i, j int) bool {
			if l[i].req != l[j].req {
				return l[i].req
			}
			if l[i].depth != l[j].depth {
				return l[i].depth < l[j].depth
			}
			return l[i].path < l[j].path
		})
		if len(l) > 6 {
			l = l[:6]
		}
		for _, x := range l {
			out[k] = append(out[k], x.path)
		}
	}
	return out
}

func (m *model) foundationReport(r *results) ([]byte, error) {
	f, err := loadFoundation()
	if err != nil {
		return nil, err
	}
	for k := range f.Resources {
		if _, ok := m.cfn[k]; !ok {
			return nil, fmt.Errorf("foundation.yaml: resources: %s is not a CloudFormation resource", k)
		}
	}
	for s := range f.Services {
		if _, ok := r.svcIndex[s]; !ok {
			return nil, fmt.Errorf("foundation.yaml: services: %s is not a CloudFormation service", s)
		}
	}
	for _, it := range f.Queue {
		for _, k := range it.Resources {
			if _, ok := m.cfn[k]; !ok {
				return nil, fmt.Errorf("foundation.yaml: queue %s: %s is not a CloudFormation resource", it.ID, k)
			}
		}
	}
	need, err := m.moduleNeed()
	if err != nil {
		return nil, err
	}
	rows := m.foundationRows(r, f, need)
	matters := r.uncoveredAttrs()
	byKey := map[string]fRow{}
	for _, fr := range rows {
		byKey[fr.key] = fr
	}

	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	w("# Foundation gap report\n\n")
	w("<!-- Generated by hack/coverage/run.sh from the coverage data, hack/coverage/foundation.yaml (the research) and hack/coverage/module-usage.csv; do not edit. -->\n\n")
	w("The ranked work queue for the kind agents. It covers every resource in the two categories a platform team needs first, *platform-foundation* (Organizations and Control Tower, IAM and Identity Center, VPC networking and DNS, KMS, CloudTrail, RAM, EKS and ECR) and *security-adjacent* (Config, GuardDuty, Security Hub, WAF, Secrets Manager, ACM and the like), as defined on 2026-11-09. Application primitives (S3, SQS, Lambda, RDS, ...) are not here; the [coverage README](../../README.md#coverage) shows them.\n\n")
	w("**How to use it.** Take the first item in the [work queue](#work-queue) that is not built; [docs/handoff.md](../handoff.md) says how a gap kind is built and checked here. Each item names the kinds to build, what upstream already has (so you copy before you write, `CONTRIBUTING.md`), whether moto can prove it, and when it is done. The [every-resource tables](#every-resource-ranked) are the same ranking at resource level, with the numbers regenerated from `config/crds/` on each run, so an item that gets built moves down by itself.\n\n")

	// Where we stand.
	w("## Where we stand\n\n")
	w("Attributes are writable CloudFormation properties (see [README.md](README.md) for the definitions); \"ACK\" is the latest release of each ACK controller; \"ours\" is what this repository's kinds add beyond ACK.\n\n")
	w("| Category | Services | Resources | ACK | Ours beyond | None | Attributes | ACK | Ours beyond | Uncovered |\n| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, t := range r.categoryRows() {
		w("| %s | %d | %d | %d (%s%%) | %d (%s%%) | %d | %s | %s%% | %s%% | %s%% |\n", t.label, t.services, t.resTotal, t.resOfficial, pct1(t.resOfficial, t.resTotal), t.resOursBeyond, pct1(t.resOursBeyond, t.resTotal), t.resUncov(),
			commas(t.attrTotal), pct1(t.attrOfficial, t.attrTotal), pct1(t.attrOursBeyond, t.attrTotal), pct1(t.attrUncov, t.attrTotal))
	}
	w("\n")
	for _, cat := range []string{catPlatform, catSecurity} {
		w("**%s, by service** (in the order the queue takes them):\n\n", cat)
		w("| # | Service | Resources | ACK | Ours beyond | None | Attributes ACK / ours / uncovered | ACK controller (latest release) |\n| ---: | --- | ---: | ---: | ---: | ---: | --- | --- |\n")
		var svcs []svcRow
		for _, s := range r.svcs {
			if r.categoryOf(s.service) == cat {
				svcs = append(svcs, s)
			}
		}
		sort.SliceStable(svcs, func(i, j int) bool {
			ri, rj := f.Services[svcs[i].service].Rank, f.Services[svcs[j].service].Rank
			if ri == 0 {
				ri = 500
			}
			if rj == 0 {
				rj = 500
			}
			if ri != rj {
				return ri < rj
			}
			return svcs[i].service < svcs[j].service
		})
		for i, s := range svcs {
			rel := "none"
			if c := m.controllerOf(s.service); c != "" {
				rel = c
			}
			w("| %d | `%s` | %d | %d | %d | %d | %s%% / %s%% / %s%% | %s |\n", i+1, s.service, s.resTotal, s.resOfficial, s.resOursBeyond, s.resTotal-s.resOfficial-s.resOursBeyond,
				pct1(s.attrOfficial, s.attrTotal), pct1(s.attrOursBeyond, s.attrTotal), pct1(s.attrUncov, s.attrTotal), rel)
		}
		w("\n")
	}

	// Queue.
	w("## Work queue\n\n")
	w("Ranked by foundation importance first, then by how many of the tracked Terraform modules need it, then by size. Items marked **blocked** cannot be proved against moto yet. Sizes are rough: S about one build session, M two or three, L more, and a service this repository has no manager for yet adds one for the scaffolding (`services/<service>/`, the operator's mapping, the e2e harness).\n\n")
	w("| # | Item | Category | Resources | Attributes ACK / ours / none | Size | moto |\n| --- | --- | --- | ---: | --- | --- | --- |\n")
	for _, it := range f.Queue {
		off, ours, none, tot := 0, 0, 0, 0
		cat := ""
		for _, k := range it.Resources {
			if fr, ok := byKey[k]; ok {
				off += fr.off
				ours += fr.ours
				none += fr.none
				tot += fr.total
				if cat == "" {
					cat = fr.cat
				}
			}
		}
		w("| [%s](#%s) | %s | %s | %d | %d / %d / %d | %s | %s |\n", it.ID, strings.ToLower(it.ID), mdEscape(it.Title), cat, len(it.Resources), off, ours, none, it.Size, mdEscape(shortMoto(it.Moto)))
	}
	w("\n")
	for _, it := range f.Queue {
		off, ours, none, tot := 0, 0, 0, 0
		var names []string
		for _, k := range it.Resources {
			if fr, ok := byKey[k]; ok {
				off += fr.off
				ours += fr.ours
				none += fr.none
				tot += fr.total
				names = append(names, fmt.Sprintf("`%s`", k))
			} else {
				names = append(names, fmt.Sprintf("`%s` (outside the CloudFormation denominator)", k))
			}
		}
		w("### %s %s\n\n", it.ID, it.Title)
		w("- **Resources:** %s. Today ACK covers %d, ours %d, nothing covers %d of %d attributes.\n", strings.Join(names, ", "), off, ours, none, tot)
		w("- **Why first:** %s\n", strings.TrimSpace(it.Why))
		w("- **Proposed kinds:** %s\n", bullets(it.Kinds))
		w("- **Upstream:** %s\n", strings.TrimSpace(it.Upstream))
		w("- **moto (5.2.5):** %s\n", strings.TrimSpace(it.Moto))
		w("- **Modules:** %s\n", strings.TrimSpace(it.Modules))
		w("- **Size:** %s\n", it.Size)
		w("- **Approach:** %s\n", bullets(it.Approach))
		w("- **Done when:** %s\n\n", strings.TrimSpace(it.Done))
	}

	// Every resource.
	w("## Every resource, ranked\n\n")
	w("One section per service in queue order; inside a service, resources that still need something first, then by modules that need them, then by size. Columns:\n\n")
	w("- **ACK**: the released kind and how many of the resource's attributes it covers; **Ours**: our kinds and the attributes they add beyond ACK.\n")
	w("- **Missing**: `whole resource`, or the attributes nothing covers (required ones and shallow ones first, unless `foundation.yaml` names the ones that matter); `covered inline` means ACK (or ours) covers it as fields of another kind, so the number is not a gap in practice (the coverage CSVs do not credit it).\n")
	w("- **Modules**: how many of the 40 tracked terraform-aws-modules create it (their Terraform resource types, paired by name and by [`mappings.yaml`](../../hack/coverage/mappings.yaml) `terraform:`); 0 is not 'unneeded' for services no public module wraps.\n")
	w("- **moto**: whether moto 5.2.5, our only emulator, implements the API; **Size**: from `foundation.yaml`, else estimated from the attribute count (S up to 15, M up to 60, L above); **Item**: the queue item that builds it.\n\n")
	cur := ""
	for _, fr := range rows {
		sec := fr.service
		if fr.group >= 1000 {
			sec += " (other resources)"
		}
		if sec != cur {
			cur = sec
			svc := f.Services[fr.service]
			w("### %s: `%s`\n\n", fr.cat, sec)
			if fr.group < 1000 {
				if svc.Controller != "" {
					w("- **ACK controller:** %s\n", svc.Controller)
				} else if c := m.controllerOf(fr.service); c != "" {
					w("- **ACK controller:** %s\n", c)
				} else {
					w("- **ACK controller:** none released\n")
				}
				if len(svc.Ignored) > 0 {
					w("- **Ignored in ACK's `generator.yaml`:** `%s`\n", strings.Join(svc.Ignored, "`, `"))
				}
				if svc.Upstream != "" {
					w("- **Upstream:** %s\n", strings.TrimSpace(svc.Upstream))
				}
				if svc.Moto != "" {
					w("- **moto:** %s\n", strings.TrimSpace(svc.Moto))
				}
				if svc.NewService {
					w("- **New service:** this repository has no manager for `%s` yet.\n", fr.service)
				}
			}
			w("\n| Resource | ACK | Ours | Missing | Upstream | Modules | moto | Size | Proposed | Item |\n| --- | --- | --- | --- | --- | ---: | --- | --- | --- | --- |\n")
		}
		svc := f.Services[fr.service]
		ack := "none"
		if len(fr.official) > 0 {
			ack = fmt.Sprintf("`%s` %s: %d/%d", strings.Join(fr.official, "`, `"), fr.release, fr.off, fr.total)
		}
		oursCol := "-"
		if len(fr.oursKinds) > 0 {
			oursCol = fmt.Sprintf("`%s`: +%d", strings.Join(fr.oursKinds, "`, `"), fr.ours)
		}
		up := fr.res.Upstream
		if up == "" {
			up = derivedUpstream(m, fr, svc)
		}
		mo := fr.res.Moto
		if mo == "" {
			mo = "service-level only: " + shortMoto(svc.Moto)
			if strings.TrimSpace(svc.Moto) == "" {
				mo = "not researched"
			}
		}
		kinds := fr.res.Kinds
		if kinds == "" {
			kinds = derivedKinds(m, fr, svc)
		}
		sz := fr.size
		if !fr.gap {
			sz = "-"
		}
		it := fr.item
		if it != "" {
			it = "[" + it + "](#" + strings.ToLower(it) + ")"
		}
		w("| `%s` | %s | %s | %s | %s | %d | %s | %s | %s | %s |\n", fr.name, mdEscape(ack), mdEscape(oursCol), mdEscape(fr.missing(m, matters)), mdEscape(up), len(fr.modules), mdEscape(mo), sz, mdEscape(kinds), it)
	}
	w("\n## Sources and pins\n\n")
	w("Researched %s, read-only.\n\n", f.Researched)
	for _, s := range f.Sources {
		w("- %s\n", strings.TrimSpace(s))
	}
	w("\n")
	return []byte(b.String()), nil
}

// controllerOf describes the latest release of the ACK controller for a
// CloudFormation service, from sources.yaml.
func (m *model) controllerOf(service string) string {
	for _, c := range m.src.ACK.Controllers {
		if m.cfnService(c.Name) == service {
			return fmt.Sprintf("`%s-controller` %s", c.Name, c.Release)
		}
	}
	return ""
}

func shortMoto(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, ".;"); i > 0 && i < 90 {
		return s[:i]
	}
	if len(s) > 90 {
		return s[:90] + "..."
	}
	return s
}

func derivedUpstream(m *model, fr fRow, svc fService) string {
	ignored := false
	for _, n := range svc.Ignored {
		if normName(n) == normName(fr.name) {
			ignored = true
		}
	}
	switch {
	case len(fr.official) > 0 && fr.none == 0:
		return "released"
	case len(fr.official) > 0:
		return "kind released; the rest is not in it"
	case ignored:
		return "ignored in ACK's generator.yaml"
	case m.controllerOf(fr.service) != "":
		return "controller exists; resource not in it"
	}
	return "no released controller"
}

func derivedKinds(m *model, fr fRow, svc fService) string {
	switch {
	case fr.total == 0:
		return "-"
	case fr.inline != "":
		return "none (inline)"
	case fr.none == 0:
		return "none (covered)"
	case len(fr.official) > 0:
		return "the missing fields: upstream PR or our copy (plan section 9, question 1)"
	case m.controllerOf(fr.service) != "":
		return fmt.Sprintf("`%s.aws.hfl.dev/%s`, generated", fr.service, fr.name)
	}
	return fmt.Sprintf("`%s.aws.hfl.dev/%s`, in a new service manager", fr.service, fr.name)
}
