// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/csv"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
)

// Tiers rank services by how much a team migrating from Terraform needs them
// (docs/coverage/README.md, "Tiers"): 1 the foundational modules' services,
// 2 every other service a tracked module uses, 3 everything else.
const (
	defaultTier = 3
	maxTier     = 3
)

func (c config) priorityPath() string { return c.outDir + "/priority.csv" }

// loadPriority reads docs/coverage/priority.csv (hand-edited; the generator
// never writes it). A service missing from it is tier 3.
func loadPriority(c config, services []string) (map[string]int, error) {
	f, err := readFile(c.priorityPath())
	if err != nil {
		return nil, err
	}
	rows, err := csv.NewReader(strings.NewReader(string(f))).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.priorityPath(), err)
	}
	if len(rows) == 0 || strings.Join(rows[0], ",") != "service,tier,rationale,source" {
		return nil, fmt.Errorf("%s: the header must be service,tier,rationale,source", c.priorityPath())
	}
	known := map[string]bool{}
	for _, s := range services {
		known[s] = true
	}
	tiers := map[string]int{}
	for i, r := range rows[1:] {
		if len(r) != 4 {
			return nil, fmt.Errorf("%s: row %d has %d columns, want 4", c.priorityPath(), i+2, len(r))
		}
		t, err := strconv.Atoi(r[1])
		if err != nil || t < 1 || t > maxTier {
			return nil, fmt.Errorf("%s: row %d: tier %q is not 1, 2 or 3", c.priorityPath(), i+2, r[1])
		}
		if !known[r[0]] {
			return nil, fmt.Errorf("%s: row %d: %q is not a service in the CloudFormation schemas", c.priorityPath(), i+2, r[0])
		}
		if _, dup := tiers[r[0]]; dup {
			return nil, fmt.Errorf("%s: row %d: %q is listed twice", c.priorityPath(), i+2, r[0])
		}
		tiers[r[0]] = t
	}
	return tiers, nil
}

func (r *results) tierOf(service string) int {
	if t, ok := r.priority[service]; ok {
		return t
	}
	return defaultTier
}

// tierRows rolls the services up by tier.
func (r *results) tierRows() []tierRow {
	rows := make([]tierRow, maxTier)
	for i := range rows {
		rows[i].tier = i + 1
	}
	for _, s := range r.svcs {
		t := &rows[r.tierOf(s.service)-1]
		t.services++
		t.resTotal += s.resTotal
		t.resOfficial += s.resOfficial
		t.resOursBeyond += s.resOursBeyond
		t.attrTotal += s.attrTotal
		t.attrOfficial += s.attrOfficial
		t.attrOursBeyond += s.attrOursBeyond
		t.attrUncov += s.attrUncov
	}
	return rows
}

type tierRow struct {
	tier     int
	services int
	svcRow
}

func (t tierRow) resUncov() int { return t.resTotal - t.resOfficial - t.resOursBeyond }

func (r *results) tiersCSV() []byte {
	rows := [][]string{{"tier", "services", "resources_total", "resources_official", "resources_ours_beyond", "resources_uncovered",
		"attributes_total", "attributes_official", "attributes_ours_beyond", "attributes_uncovered",
		"pct_attributes_official", "pct_attributes_ours_beyond", "pct_attributes_uncovered",
		"pct_resources_official", "pct_resources_ours_beyond", "pct_resources_uncovered"}}
	for _, t := range r.tierRows() {
		rows = append(rows, []string{itoa(t.tier), itoa(t.services), itoa(t.resTotal), itoa(t.resOfficial), itoa(t.resOursBeyond), itoa(t.resUncov()),
			itoa(t.attrTotal), itoa(t.attrOfficial), itoa(t.attrOursBeyond), itoa(t.attrUncov),
			pct1(t.attrOfficial, t.attrTotal), pct1(t.attrOursBeyond, t.attrTotal), pct1(t.attrUncov, t.attrTotal),
			pct1(t.resOfficial, t.resTotal), pct1(t.resOursBeyond, t.resTotal), pct1(t.resUncov(), t.resTotal)})
	}
	return writeCSV(rows)
}

var tierNames = map[int]string{1: "Tier 1", 2: "Tier 2", 3: "Tier 3"}

// tiersSVG is one card: for each tier, an attributes bar and a resources bar.
func tiersSVG(rows []tierRow) []byte {
	const w, h, pad, labelH, gap = 640, 22, 10, 18, 6
	groupH := labelH + 2*h + gap + 14
	height := 2*pad + len(rows)*groupH + 22 - 14 + 6
	var b strings.Builder
	var parts []string
	for _, t := range rows {
		parts = append(parts, fmt.Sprintf("%s: attributes official %s%%, ours beyond %s%%, uncovered %s%%; resources official %s%%, ours beyond %s%%, uncovered %s%%",
			tierNames[t.tier], pct1(t.attrOfficial, t.attrTotal), pct1(t.attrOursBeyond, t.attrTotal), pct1(t.attrUncov, t.attrTotal),
			pct1(t.resOfficial, t.resTotal), pct1(t.resOursBeyond, t.resTotal), pct1(t.resUncov(), t.resTotal)))
	}
	title := html.EscapeString(strings.Join(parts, ". "))
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-label="%s">`+"\n", w+2*pad, height, w+2*pad, height, title)
	fmt.Fprintf(&b, "<title>%s</title>\n", title)
	fmt.Fprintf(&b, `<rect x="0.5" y="0.5" width="%d" height="%d" rx="6" fill="#ffffff" stroke="#d0d7de"/>`+"\n", w+2*pad-1, height-1)
	y := pad
	for _, t := range rows {
		label := fmt.Sprintf("%s: %d services, %s resources, %s attributes", tierNames[t.tier], t.services, commas(t.resTotal), commas(t.attrTotal))
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-family="%s" font-size="12" font-weight="600" fill="%s" dominant-baseline="central">%s</text>`+"\n", pad, y+labelH/2, fontFamily, colTextDark, html.EscapeString(label))
		segments(&b, t.attrOfficial, t.attrOursBeyond, t.attrUncov, pad, y+labelH, w, h, 12)
		segments(&b, t.resOfficial, t.resOursBeyond, t.resUncov(), pad, y+labelH+h+gap, w, h, 12)
		y += groupH
	}
	items := []struct{ fill, text string }{{colOfficial, "ACK"}, {colOurs, "ours beyond ACK"}, {colNone, "uncovered"}, {"#ffffff", "upper bar: attributes, lower bar: resources"}}
	x := pad
	for _, it := range items[:3] {
		fmt.Fprintf(&b, `<rect x="%d.5" y="%d.5" width="11" height="11" rx="2" fill="%s" stroke="%s"/>`+"\n", x, y-14+7, it.fill, colBorder)
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-family="%s" font-size="12" fill="%s" dominant-baseline="central">%s</text>`+"\n", x+17, y-14+13, fontFamily, colTextDark, html.EscapeString(it.text))
		x += 17 + 7*len(it.text) + 18
	}
	fmt.Fprintf(&b, `<text x="%d" y="%d" font-family="%s" font-size="12" fill="#59636e" dominant-baseline="central">%s</text>`+"\n", x, y-14+13, fontFamily, html.EscapeString(items[3].text))
	b.WriteString("</svg>\n")
	return []byte(b.String())
}

func sortedTierServices(r *results, tier int) []string {
	var out []string
	for _, s := range r.svcs {
		if r.tierOf(s.service) == tier {
			out = append(out, s.service)
		}
	}
	sort.Strings(out)
	return out
}
