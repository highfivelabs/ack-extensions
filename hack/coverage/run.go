// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	markerStart = "<!-- coverage:start -->"
	markerEnd   = "<!-- coverage:end -->"
	// attributesSplitBytes is the size above which attributes.csv is split by
	// service into attributes/<service>.csv.
	attributesSplitBytes = 25 << 20
)

// run builds every output and writes them, or with check compares them to
// the committed files.
func run(c config, check bool) error {
	m, err := build(c)
	if err != nil {
		return err
	}
	r := m.compute()
	files, err := outputs(c, m, r)
	if err != nil {
		return err
	}
	if check {
		return checkFiles(c, files)
	}
	return writeFiles(c, files)
}

// outputs maps a path relative to the repository root to its content.
func outputs(c config, m *model, r *results) (map[string][]byte, error) {
	files := map[string][]byte{}
	dir := "docs/coverage/"
	files[dir+"services.csv"] = r.servicesCSV()
	files[dir+"resources.csv"] = r.resourcesCSV()
	if all := r.attributesCSV(nil); len(all) <= attributesSplitBytes {
		files[dir+"attributes.csv"] = all
	} else {
		for _, s := range m.services {
			s := s
			files[dir+"attributes/"+s+".csv"] = r.attributesCSV(func(x string) bool { return x == s })
		}
	}
	for i := range r.svcs {
		s := r.svcs[i]
		files[dir+"bars/"+s.service+".svg"] = barSVG(barTitle(s.service, s), s)
	}
	files[dir+"bars/_total.svg"] = totalSVG(r.total, "ACK")
	files[dir+"bars/_tiers.svg"] = tiersSVG(r.tierRows())
	files[dir+"tiers.csv"] = r.tiersCSV()
	files[dir+"README.md"] = docsReadme(m, r)

	readme, err := os.ReadFile(filepath.Join(c.root, "README.md"))
	if err != nil {
		return nil, err
	}
	out, err := replaceSection(readme, rootSection(r))
	if err != nil {
		return nil, err
	}
	files["README.md"] = out
	return files, nil
}

func replaceSection(readme []byte, section string) ([]byte, error) {
	s := string(readme)
	i, j := strings.Index(s, markerStart), strings.Index(s, markerEnd)
	if i < 0 || j < i {
		return nil, fmt.Errorf("README.md needs the markers %s and %s", markerStart, markerEnd)
	}
	return []byte(s[:i] + markerStart + "\n" + section + s[j:]), nil
}

// generated reports whether a path under docs/coverage is a generated file
// (the committed inputs and sources.yaml are not).
func generatedDir(rel string) bool {
	return strings.HasPrefix(rel, "docs/coverage/bars/") || strings.HasPrefix(rel, "docs/coverage/attributes/")
}

func writeFiles(c config, files map[string][]byte) error {
	// Remove stale generated files.
	for _, sub := range []string{"bars", "attributes"} {
		entries, _ := os.ReadDir(filepath.Join(c.outDir, sub))
		for _, e := range entries {
			rel := "docs/coverage/" + sub + "/" + e.Name()
			if _, ok := files[rel]; !ok {
				if err := os.Remove(filepath.Join(c.root, rel)); err != nil {
					return err
				}
			}
		}
	}
	if _, split := files["docs/coverage/attributes.csv"]; split {
		_ = os.Remove(filepath.Join(c.outDir, "attributes.csv"))
	}
	var rels []string
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		p := filepath.Join(c.root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, files[rel]) {
			continue
		}
		if err := os.WriteFile(p, files[rel], 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("wrote %d files under docs/coverage/ and the Coverage section of README.md\n", len(files)-1)
	return nil
}

func checkFiles(c config, files map[string][]byte) error {
	var stale []string
	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join(c.root, rel))
		if err != nil || !bytes.Equal(got, want) {
			stale = append(stale, rel)
		}
	}
	for _, sub := range []string{"bars", "attributes"} {
		entries, _ := os.ReadDir(filepath.Join(c.outDir, sub))
		for _, e := range entries {
			rel := "docs/coverage/" + sub + "/" + e.Name()
			if _, ok := files[rel]; !ok {
				stale = append(stale, rel+" (should not exist)")
			}
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		return fmt.Errorf("committed coverage outputs differ from a regeneration from the pinned inputs:\n  %s\nrun hack/coverage/run.sh and commit the result", strings.Join(stale, "\n  "))
	}
	fmt.Println("ok: docs/coverage/ and the README's Coverage section match a regeneration from the pinned inputs")
	return nil
}
