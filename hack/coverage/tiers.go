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
func loadPriority(c config, services []string) (map[string]priorityEntry, error) {
	f, err := readFile(c.priorityPath())
	if err != nil {
		return nil, err
	}
	rows, err := csv.NewReader(strings.NewReader(string(f))).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.priorityPath(), err)
	}
	if len(rows) == 0 || strings.Join(rows[0], ",") != "service,tier,category,rationale,source" {
		return nil, fmt.Errorf("%s: the header must be service,tier,category,rationale,source", c.priorityPath())
	}
	known := map[string]bool{}
	for _, s := range services {
		known[s] = true
	}
	tiers := map[string]priorityEntry{}
	for i, r := range rows[1:] {
		if len(r) != 5 {
			return nil, fmt.Errorf("%s: row %d has %d columns, want 5", c.priorityPath(), i+2, len(r))
		}
		t, err := strconv.Atoi(r[1])
		if err != nil || t < 1 || t > maxTier {
			return nil, fmt.Errorf("%s: row %d: tier %q is not 1, 2 or 3", c.priorityPath(), i+2, r[1])
		}
		if !validCategory(r[2]) {
			return nil, fmt.Errorf("%s: row %d: category %q is not one of %s", c.priorityPath(), i+2, r[2], strings.Join(categories, ", "))
		}
		if !known[r[0]] {
			return nil, fmt.Errorf("%s: row %d: %q is not a service in the CloudFormation schemas", c.priorityPath(), i+2, r[0])
		}
		if _, dup := tiers[r[0]]; dup {
			return nil, fmt.Errorf("%s: row %d: %q is listed twice", c.priorityPath(), i+2, r[0])
		}
		tiers[r[0]] = priorityEntry{tier: t, category: r[2]}
	}
	return tiers, nil
}

// The categories, in the order the reports show them.
const (
	catPlatform = "platform-foundation"
	catSecurity = "security-adjacent"
	catApp      = "application"
	catOther    = "other"
)

var categories = []string{catPlatform, catSecurity, catApp, catOther}

func validCategory(c string) bool {
	for _, x := range categories {
		if x == c {
			return true
		}
	}
	return false
}

type priorityEntry struct {
	tier     int
	category string
}

func (r *results) tierOf(service string) int {
	if t, ok := r.priority[service]; ok {
		return t.tier
	}
	return defaultTier
}

// categoryOf is the category of a service; a service not in priority.csv is other.
func (r *results) categoryOf(service string) string {
	if t, ok := r.priority[service]; ok {
		return t.category
	}
	return catOther
}

// tierRows rolls the services up by tier.
func (r *results) tierRows() []tierRow {
	rows := make([]tierRow, maxTier)
	for i := range rows {
		rows[i].tier = i + 1
		rows[i].label = tierNames[i+1]
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
	for _, x := range r.tfRows {
		rows[r.tierOf(x.service)-1].tf.add(x.tfCounts)
	}
	return rows
}

type tierRow struct {
	tier     int    // 1 to 3 for a tier row
	label    string // "Tier 1", "platform-foundation"
	services int
	svcRow
	tf tfCounts // the Terraform estimate over the group's terraform.csv rows
}

func (t tierRow) resUncov() int { return t.resTotal - t.resOfficial - t.resOursBeyond }

// categoryRows rolls the services up by category.
func (r *results) categoryRows() []tierRow {
	rows := make([]tierRow, len(categories))
	idx := map[string]int{}
	for i, c := range categories {
		rows[i].label = c
		idx[c] = i
	}
	for _, s := range r.svcs {
		t := &rows[idx[r.categoryOf(s.service)]]
		t.services++
		t.resTotal += s.resTotal
		t.resOfficial += s.resOfficial
		t.resOursBeyond += s.resOursBeyond
		t.attrTotal += s.attrTotal
		t.attrOfficial += s.attrOfficial
		t.attrOursBeyond += s.attrOursBeyond
		t.attrUncov += s.attrUncov
	}
	for _, x := range r.tfRows {
		rows[idx[r.categoryOf(x.service)]].tf.add(x.tfCounts)
	}
	return rows
}

func (r *results) categoriesCSV() []byte {
	return rollupCSV("category", r.categoryRows(), func(t tierRow) string { return t.label })
}

func (r *results) tiersCSV() []byte {
	return rollupCSV("tier", r.tierRows(), func(t tierRow) string { return itoa(t.tier) })
}

func rollupCSV(first string, in []tierRow, key func(tierRow) string) []byte {
	rows := [][]string{{first, "services", "resources_total", "resources_official", "resources_ours_beyond", "resources_uncovered",
		"attributes_total", "attributes_official", "attributes_ours_beyond", "attributes_uncovered",
		"pct_attributes_official", "pct_attributes_ours_beyond", "pct_attributes_uncovered",
		"pct_resources_official", "pct_resources_ours_beyond", "pct_resources_uncovered"}}
	for _, t := range in {
		rows = append(rows, []string{key(t), itoa(t.services), itoa(t.resTotal), itoa(t.resOfficial), itoa(t.resOursBeyond), itoa(t.resUncov()),
			itoa(t.attrTotal), itoa(t.attrOfficial), itoa(t.attrOursBeyond), itoa(t.attrUncov),
			pct1(t.attrOfficial, t.attrTotal), pct1(t.attrOursBeyond, t.attrTotal), pct1(t.attrUncov, t.attrTotal),
			pct1(t.resOfficial, t.resTotal), pct1(t.resOursBeyond, t.resTotal), pct1(t.resUncov(), t.resTotal)})
	}
	return writeCSV(rows)
}

var tierNames = map[int]string{1: "Tier 1", 2: "Tier 2", 3: "Tier 3"}

// tiersSVG is one card: for each tier, an attributes bar and a resources bar.
func tiersSVG(rows []tierRow) []byte {
	return rollupSVG(rows, func(t tierRow) string { return t.label })
}

// rollupSVG draws one pair of bars per row.
func rollupSVG(rows []tierRow, name func(tierRow) string) []byte {
	const w, h, pad, labelH, gap = 640, 22, 10, 18, 6
	const tfH = 10
	groupH := labelH + 2*h + gap + tfH + gap + 14
	height := 2*pad + len(rows)*groupH + 22 - 14 + 6 + 22
	var b strings.Builder
	var parts []string
	for _, t := range rows {
		parts = append(parts, fmt.Sprintf("%s: attributes official %s%%, ours beyond %s%%, uncovered %s%%; resources official %s%%, ours beyond %s%%, uncovered %s%%; %s",
			name(t), pct1(t.attrOfficial, t.attrTotal), pct1(t.attrOursBeyond, t.attrTotal), pct1(t.attrUncov, t.attrTotal),
			pct1(t.resOfficial, t.resTotal), pct1(t.resOursBeyond, t.resTotal), pct1(t.resUncov(), t.resTotal), tfTitle(t.tf)))
	}
	title := html.EscapeString(strings.Join(parts, ". "))
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-label="%s">`+"\n", w+2*pad, height, w+2*pad, height, title)
	fmt.Fprintf(&b, "<title>%s</title>\n", title)
	fmt.Fprintf(&b, `<rect x="0.5" y="0.5" width="%d" height="%d" rx="6" fill="#ffffff" stroke="#d0d7de"/>`+"\n", w+2*pad-1, height-1)
	y := pad
	for _, t := range rows {
		label := fmt.Sprintf("%s: %d services, %s resources, %s attributes", name(t), t.services, commas(t.resTotal), commas(t.attrTotal))
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-family="%s" font-size="12" font-weight="600" fill="%s" dominant-baseline="central">%s</text>`+"\n", pad, y+labelH/2, fontFamily, colTextDark, html.EscapeString(label))
		segments(&b, t.attrOfficial, t.attrOursBeyond, t.attrUncov, pad, y+labelH, w, h, 12)
		segments(&b, t.resOfficial, t.resOursBeyond, t.resUncov(), pad, y+labelH+h+gap, w, h, 12)
		tfBar(&b, t.tf, pad, y+labelH+2*h+2*gap, w, tfH, 0)
		y += groupH
	}
	items := []struct{ fill, text string }{{colOfficial, "ACK"}, {colOurs, "ours beyond ACK"}, {colNone, "uncovered"}, {"#ffffff", "bars: attributes, resources, Terraform"}}
	x := pad
	for _, it := range items[:3] {
		fmt.Fprintf(&b, `<rect x="%d.5" y="%d.5" width="11" height="11" rx="2" fill="%s" stroke="%s"/>`+"\n", x, y-14+7, it.fill, colBorder)
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-family="%s" font-size="12" fill="%s" dominant-baseline="central">%s</text>`+"\n", x+17, y-14+13, fontFamily, colTextDark, html.EscapeString(it.text))
		x += 17 + 7*len(it.text) + 18
	}
	fmt.Fprintf(&b, `<text x="%d" y="%d" font-family="%s" font-size="12" fill="#59636e" dominant-baseline="central">%s</text>`+"\n", x, y-14+13, fontFamily, html.EscapeString(items[3].text))
	tfLegend(&b, pad, y-14+7+22)
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

func sortedCategoryServices(r *results, cat string) []string {
	var out []string
	for _, s := range r.svcs {
		if r.categoryOf(s.service) == cat {
			out = append(out, s.service)
		}
	}
	sort.Strings(out)
	return out
}
