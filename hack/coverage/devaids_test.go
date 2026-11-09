// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"sort"
	"testing"
)

// TestPotential is a dev aid: COVPOTENTIAL=1 go test ./hack/coverage -run Potential -v
func TestPotential(t *testing.T) {
	if os.Getenv("COVPOTENTIAL") == "" {
		t.Skip()
	}
	root := "../.."
	c := config{root: root, cache: root + "/.cache/coverage", outDir: root + "/docs/coverage", mapping: root + "/hack/coverage/mappings.yaml"}
	m, err := build(c)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		k    string
		u, v int
	}
	var rows []row
	for k, cvs := range m.official {
		for _, cv := range cvs {
			u := len(m.cfn[k].Attrs) - len(cv.Match)
			v := len(cv.Unmapped)
			p := u
			if v < p {
				p = v
			}
			rows = append(rows, row{k + "<-" + cv.Kind, u, v})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		pa, pb := min(a.u, a.v), min(b.u, b.v)
		return pa > pb
	})
	for _, r := range rows[:40] {
		fmt.Printf("%-60s cfn-unmatched %4d crd-unmapped %4d\n", r.k, r.u, r.v)
	}
}

// TestAudit is a dev aid: COVAUDIT=1 go test ./hack/coverage -run Audit -v prints an
// even sample of the matches each loose tier made, for a human to judge.
func TestAudit(t *testing.T) {
	if os.Getenv("COVAUDIT") == "" {
		t.Skip()
	}
	root := "../.."
	c := config{root: root, cache: root + "/.cache/coverage", outDir: root + "/docs/coverage", mapping: root + "/hack/coverage/mappings.yaml"}
	m, err := build(c)
	if err != nil {
		t.Fatal(err)
	}
	byTier := map[string][]string{}
	for k, cvs := range m.official {
		for _, cv := range cvs {
			for ck, tier := range cv.Tiers {
				if tier == "exact" {
					continue
				}
				var cfnPath string
				for _, a := range m.cfn[k].Attrs {
					if a.key() == ck {
						cfnPath = a.Path
					}
				}
				byTier[tier] = append(byTier[tier], fmt.Sprintf("%s: %s  <->  %s", k, cfnPath, cv.Match[ck]))
			}
		}
	}
	var tiers []string
	for tname := range byTier {
		tiers = append(tiers, tname)
	}
	sort.Strings(tiers)
	for _, tname := range tiers {
		l := byTier[tname]
		sort.Strings(l)
		fmt.Printf("### %s: %d\n", tname, len(l))
		step := len(l) / 25
		if step < 1 {
			step = 1
		}
		for i := 0; i < len(l); i += step {
			fmt.Println("  ", l[i])
		}
	}
}
