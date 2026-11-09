// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Coverage of one attribute.
const (
	byNone     = "none"
	byOfficial = "official"
	byOurs     = "ours"
	byBoth     = "both"
)

type attrRow struct {
	service, resource string
	a                 attr
	covered           string
	officialPath      string
	oursPath          string
}

type resRow struct {
	service, resource string
	total             int
	officialKinds     []string
	official          int
	oursKinds         []string
	oursBeyond        int
	uncovered         int
	fromPR            bool
	oursBeyondPR      int // of oursBeyond, covered only by kinds copied from an unreleased upstream PR
	notes             []string
	outside           bool
}

type svcRow struct {
	oursBeyondPR                                       int
	service                                            string
	resTotal, resOfficial, resOursBeyond               int
	attrTotal, attrOfficial, attrOursBeyond, attrUncov int
}

func (s svcRow) pct(n int) float64 {
	if s.attrTotal == 0 {
		return 0
	}
	return float64(n) * 100 / float64(s.attrTotal)
}

// pct1 formats a percentage with one decimal, rounding half up on the
// integer count so the same inputs always give the same text.
func pct1(n, total int) string {
	if total == 0 {
		return "0.0"
	}
	tenths := (n*1000*2 + total) / (total * 2) // round half up
	return fmt.Sprintf("%d.%d", tenths/10, tenths%10)
}

type results struct {
	attrs    []attrRow
	res      []resRow
	svcs     []svcRow
	total    svcRow
	outside  []resRow
	svcIndex map[string]*svcRow
	priority map[string]int // service to tier, from priority.csv
}

func (m *model) compute() *results {
	r := &results{svcIndex: map[string]*svcRow{}, priority: m.priority}
	for _, s := range m.services {
		r.svcs = append(r.svcs, svcRow{service: s})
	}
	for i := range r.svcs {
		r.svcIndex[r.svcs[i].service] = &r.svcs[i]
	}
	for _, key := range m.cfnKeys {
		res := m.cfn[key]
		offs, ours := m.official[key], m.ours[key]
		rr := resRow{service: res.Service, resource: res.Name, total: len(res.Attrs)}
		for _, cv := range offs {
			rr.officialKinds = append(rr.officialKinds, cv.Kind)
		}
		for _, cv := range ours {
			rr.oursKinds = append(rr.oursKinds, cv.Kind)
			if cv.PR != "" {
				rr.fromPR = true
			}
		}
		sort.Strings(rr.officialKinds)
		sort.Strings(rr.oursKinds)
		for _, ca := range res.Attrs {
			k := ca.key()
			op, up := paths(offs, k), paths(ours, k)
			ar := attrRow{service: res.Service, resource: res.Name, a: ca, officialPath: op, oursPath: up}
			switch {
			case op != "" && up != "":
				ar.covered = byBoth
				rr.official++
			case op != "":
				ar.covered = byOfficial
				rr.official++
			case up != "":
				ar.covered = byOurs
				rr.oursBeyond++
				if allPR(ours, k) {
					rr.oursBeyondPR++
				}
			default:
				ar.covered = byNone
				rr.uncovered++
			}
			r.attrs = append(r.attrs, ar)
		}
		// Notes.
		for _, cv := range ours {
			if _, folded := m.maps.Ours[cv.Svc+"/"+cv.Kind]; folded && !sameName(cv.Kind, res.Name) {
				rr.notes = append(rr.notes, fmt.Sprintf("ours %s covers a property of this resource", cv.Kind))
			}
			if cv.PR != "" {
				rr.notes = append(rr.notes, fmt.Sprintf("ours %s copied from %s", cv.Kind, cv.PR))
			}
		}
		if n := unmappedCount(offs); n > 0 {
			rr.notes = append(rr.notes, fmt.Sprintf("official_unmapped=%d", n))
		}
		if n := unmappedCount(ours); n > 0 {
			rr.notes = append(rr.notes, fmt.Sprintf("ours_unmapped=%d", n))
		}
		if n, ok := m.maps.Notes[key]; ok {
			rr.notes = append(rr.notes, n)
		}
		r.res = append(r.res, rr)

		sv := r.svcIndex[res.Service]
		sv.resTotal++
		sv.attrTotal += rr.total
		sv.attrOfficial += rr.official
		sv.attrOursBeyond += rr.oursBeyond
		sv.oursBeyondPR += rr.oursBeyondPR
		sv.attrUncov += rr.uncovered
		switch {
		case len(offs) > 0:
			sv.resOfficial++
		case len(ours) > 0:
			sv.resOursBeyond++
		}
	}
	// Outside the denominator.
	for _, list := range []struct {
		covs  []*cov
		ours  bool
		label string
	}{{m.outsideOfficial, false, "official"}, {m.outsideOurs, true, "ours"}} {
		for _, cv := range list.covs {
			rr := resRow{service: m.cfnService(cv.Svc), resource: cv.Kind, outside: true}
			if list.ours {
				rr.oursKinds = []string{cv.Kind}
				rr.fromPR = cv.PR != ""
			} else {
				rr.officialKinds = []string{cv.Kind}
			}
			rr.notes = []string{outside, fmt.Sprintf("%s_crd_attributes=%d", list.label, len(cv.CRD.Attrs))}
			r.outside = append(r.outside, rr)
		}
	}
	for _, sv := range r.svcs {
		r.total.resTotal += sv.resTotal
		r.total.resOfficial += sv.resOfficial
		r.total.resOursBeyond += sv.resOursBeyond
		r.total.attrTotal += sv.attrTotal
		r.total.attrOfficial += sv.attrOfficial
		r.total.attrOursBeyond += sv.attrOursBeyond
		r.total.oursBeyondPR += sv.oursBeyondPR
		r.total.attrUncov += sv.attrUncov
	}
	r.total.service = "_total"
	// Rows of resources.csv: the denominator's rows and the outside rows, by service then resource.
	all := append(append([]resRow(nil), r.res...), r.outside...)
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].service != all[j].service {
			return all[i].service < all[j].service
		}
		return strings.ToLower(all[i].resource) < strings.ToLower(all[j].resource)
	})
	r.res = all
	return r
}

// allPR reports whether every ours kind covering the attribute is copied from
// an unreleased upstream pull request.
func allPR(covs []*cov, key string) bool {
	any := false
	for _, cv := range covs {
		if _, ok := cv.Match[key]; ok {
			if cv.PR == "" {
				return false
			}
			any = true
		}
	}
	return any
}

func sameName(kind, resource string) bool { return normName(kind) == normName(resource) }

func unmappedCount(covs []*cov) int {
	n := 0
	for _, c := range covs {
		n += len(c.Unmapped)
	}
	return n
}

// paths joins the CRD paths covering a CloudFormation attribute key, as
// spec.<path>, prefixed by the kind when more than one kind covers.
func paths(covs []*cov, key string) string {
	var out []string
	for _, cv := range covs {
		if p, ok := cv.Match[key]; ok {
			if len(covs) > 1 {
				out = append(out, cv.Kind+":spec."+p)
			} else {
				out = append(out, "spec."+p)
			}
		}
	}
	sort.Strings(out)
	return strings.Join(out, ";")
}

func writeCSV(rows [][]string) []byte {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.WriteAll(rows)
	return b.Bytes()
}

func (r *results) servicesCSV() []byte {
	rows := [][]string{{"service", "resources_total", "resources_official", "resources_ours_beyond", "attributes_total", "attributes_official", "attributes_ours_beyond", "attributes_uncovered", "pct_official", "pct_ours_beyond", "pct_uncovered"}}
	for _, s := range r.svcs {
		rows = append(rows, []string{s.service, itoa(s.resTotal), itoa(s.resOfficial), itoa(s.resOursBeyond), itoa(s.attrTotal), itoa(s.attrOfficial), itoa(s.attrOursBeyond), itoa(s.attrUncov),
			pct1(s.attrOfficial, s.attrTotal), pct1(s.attrOursBeyond, s.attrTotal), pct1(s.attrUncov, s.attrTotal)})
	}
	return writeCSV(rows)
}

func (r *results) resourcesCSV() []byte {
	rows := [][]string{{"service", "resource", "attributes_total", "official_kinds", "attributes_official", "ours_kinds", "attributes_ours_beyond", "attributes_uncovered", "ours_from_upstream_pr", "notes"}}
	for _, x := range r.res {
		rows = append(rows, []string{x.service, x.resource, itoa(x.total), strings.Join(x.officialKinds, ";"), itoa(x.official), strings.Join(x.oursKinds, ";"), itoa(x.oursBeyond), itoa(x.uncovered),
			strconv.FormatBool(x.fromPR), strings.Join(x.notes, ";")})
	}
	return writeCSV(rows)
}

// attributesCSV returns the attribute rows of the given services.
func (r *results) attributesCSV(filter func(service string) bool) []byte {
	rows := [][]string{{"service", "resource", "attribute", "type", "required", "covered_by", "official_path", "ours_path"}}
	for _, a := range r.attrs {
		if filter != nil && !filter(a.service) {
			continue
		}
		rows = append(rows, []string{a.service, a.resource, a.a.Path, a.a.Type, strconv.FormatBool(a.a.Required), a.covered, a.officialPath, a.oursPath})
	}
	return writeCSV(rows)
}

func itoa(n int) string { return strconv.Itoa(n) }
