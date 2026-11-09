// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package pins reads an extension repository's upstream.yaml, the
// machine-readable form of its UPSTREAM.md (pins, pull requests, patches,
// copied files), and checks that the two agree.
//
// This file is shared: each extension repository carries a verbatim copy
// in hack/upstream-yaml/pins/ (task upstream:yaml-check), and
// highfivelabs/factory reads upstream.yaml with it. Change it in factory
// (internal/pins/pins.go) and copy it; it depends only on the standard
// library and gopkg.in/yaml.v3.
package pins

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// File is upstream.yaml.
type File struct {
	Schema       int      `yaml:"schema" json:"schema"`
	Pins         []Pin    `yaml:"pins" json:"pins"`
	PullRequests []PR     `yaml:"pull_requests" json:"pull_requests"`
	Patches      []Patch  `yaml:"patches" json:"patches"`
	Copied       []Copy   `yaml:"copied" json:"copied"`
	OtherSources []Source `yaml:"other_sources,omitempty" json:"other_sources,omitempty"`
}

// Pin is one upstream this repository builds on, at one commit.
type Pin struct {
	// Name is how UPSTREAM.md names it (code-generator, rds-controller,
	// k8s-config-connector).
	Name string `yaml:"name" json:"name"`
	// Repo is owner/name on github.com.
	Repo string `yaml:"repo" json:"repo"`
	// Branch is set for a pin on a branch's commit (Config Connector's
	// master, ASO's main); Version for a pin on a release tag.
	Branch  string `yaml:"branch,omitempty" json:"branch,omitempty"`
	Version string `yaml:"version,omitempty" json:"version,omitempty"`
	// Commit is the full SHA, or a prefix where UPSTREAM.md records only
	// that; empty when UPSTREAM.md records the tag alone.
	Commit         string `yaml:"commit,omitempty" json:"commit,omitempty"`
	CommitDate     string `yaml:"commit_date,omitempty" json:"commit_date,omitempty"`
	NearestRelease string `yaml:"nearest_release,omitempty" json:"nearest_release,omitempty"`
	// NearestReleaseCommit is the commit the nearest release tag names.
	NearestReleaseCommit string `yaml:"nearest_release_commit,omitempty" json:"nearest_release_commit,omitempty"`
	PinnedOn             string `yaml:"pinned_on,omitempty" json:"pinned_on,omitempty"`
	// Patches is the directory of the patch series applied to it.
	Patches string `yaml:"patches,omitempty" json:"patches,omitempty"`
	// Via says how the pin is reached when it is not fetched directly
	// (a submodule of another pin).
	Via string `yaml:"via,omitempty" json:"via,omitempty"`
	// Use is UPSTREAM.md's "how it is used", shortened.
	Use string `yaml:"use,omitempty" json:"use,omitempty"`
}

// PR is a pull request this repository copies from or follows.
type PR struct {
	// Ref is owner/name#number.
	Ref    string `yaml:"ref" json:"ref"`
	Head   string `yaml:"head" json:"head"`
	ReadOn string `yaml:"read_on,omitempty" json:"read_on,omitempty"`
	// Role is copied (code here comes from it) or watched (followed, not
	// copied).
	Role string `yaml:"role" json:"role"`
	For  string `yaml:"for,omitempty" json:"for,omitempty"`
}

// Patch is one file of a patch series.
type Patch struct {
	File string `yaml:"file" json:"file"`
	// Pin names the pin the series applies to.
	Pin       string `yaml:"pin" json:"pin"`
	Candidate string `yaml:"upstream_candidate,omitempty" json:"upstream_candidate,omitempty"`
}

// Copy is one row of "What is copied".
type Copy struct {
	Here []string `yaml:"here" json:"here"`
	From From     `yaml:"from" json:"from"`
	// AlsoPRs are pull requests whose changes were carried into the copy
	// too ("the same file, plus PR #13517").
	AlsoPRs []string `yaml:"also_prs,omitempty" json:"also_prs,omitempty"`
}

// From is where a copy came from: a pin's commit, a pull request's head,
// or nowhere (written here).
type From struct {
	Pin         string   `yaml:"pin,omitempty" json:"pin,omitempty"`
	PR          string   `yaml:"pr,omitempty" json:"pr,omitempty"`
	Paths       []string `yaml:"paths,omitempty" json:"paths,omitempty"`
	WrittenHere bool     `yaml:"written_here,omitempty" json:"written_here,omitempty"`
}

// Source is a source consulted but not copied.
type Source struct {
	Repo    string `yaml:"repo" json:"repo"`
	Commit  string `yaml:"commit" json:"commit"`
	UsedFor string `yaml:"used_for,omitempty" json:"used_for,omitempty"`
}

// Load reads upstream.yaml strictly: unknown fields are an error.
func Load(file string) (*File, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse decodes upstream.yaml.
func Parse(data []byte) (*File, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f File
	if err := dec.Decode(&f); err != nil {
		return nil, err
	}
	return &f, nil
}

// Pin returns the named pin.
func (f *File) Pin(name string) *Pin {
	for i := range f.Pins {
		if f.Pins[i].Name == name {
			return &f.Pins[i]
		}
	}
	return nil
}

// PR returns the pull request with the given ref.
func (f *File) PR(ref string) *PR {
	for i := range f.PullRequests {
		if f.PullRequests[i].Ref == ref {
			return &f.PullRequests[i]
		}
	}
	return nil
}

// SplitPR splits owner/name#123.
func SplitPR(ref string) (slug string, n int, err error) {
	i := strings.LastIndexByte(ref, '#')
	if i < 0 {
		return "", 0, fmt.Errorf("pull request %q is not owner/name#number", ref)
	}
	n, err = strconv.Atoi(ref[i+1:])
	if err != nil || strings.Count(ref[:i], "/") != 1 {
		return "", 0, fmt.Errorf("pull request %q is not owner/name#number", ref)
	}
	return ref[:i], n, nil
}

var (
	fullSHA  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	shortSHA = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	dateRE   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// Problems checks upstream.yaml on its own and against the files of the
// repository at root (patch files and copied paths exist).
func (f *File) Problems(root string) []string {
	var p []string
	bad := func(format string, a ...any) { p = append(p, fmt.Sprintf(format, a...)) }
	if f.Schema != 1 {
		bad("upstream.yaml: schema is %d, want 1", f.Schema)
	}
	names := map[string]bool{}
	for _, pin := range f.Pins {
		if pin.Name == "" || names[pin.Name] {
			bad("pin %q: empty or listed twice", pin.Name)
		}
		names[pin.Name] = true
		if strings.Count(pin.Repo, "/") != 1 || strings.Contains(pin.Repo, "github.com") {
			bad("pin %s: repo %q is not owner/name", pin.Name, pin.Repo)
		}
		if (pin.Branch == "") == (pin.Version == "") && pin.Via == "" {
			bad("pin %s: set exactly one of branch and version", pin.Name)
		}
		if pin.Commit != "" && !shortSHA.MatchString(pin.Commit) {
			bad("pin %s: commit %q is not a SHA", pin.Name, pin.Commit)
		}
		if pin.Branch != "" && !fullSHA.MatchString(pin.Commit) {
			bad("pin %s: a branch pin needs the full commit", pin.Name)
		}
		if pin.Patches != "" {
			if st, err := os.Stat(filepath.Join(root, pin.Patches)); err != nil || !st.IsDir() {
				bad("pin %s: patches directory %s does not exist", pin.Name, pin.Patches)
			}
		}
		for _, d := range []string{pin.PinnedOn} {
			if d != "" && !dateRE.MatchString(d) {
				bad("pin %s: %q is not a date", pin.Name, d)
			}
		}
	}
	refs := map[string]bool{}
	for _, pr := range f.PullRequests {
		if _, _, err := SplitPR(pr.Ref); err != nil {
			bad("%v", err)
		}
		if refs[pr.Ref] {
			bad("pull request %s: listed twice", pr.Ref)
		}
		refs[pr.Ref] = true
		if !shortSHA.MatchString(pr.Head) {
			bad("pull request %s: head %q is not a SHA", pr.Ref, pr.Head)
		}
		if pr.Role != "copied" && pr.Role != "watched" {
			bad("pull request %s: role %q is not copied or watched", pr.Ref, pr.Role)
		}
		if pr.ReadOn != "" && !dateRE.MatchString(pr.ReadOn) {
			bad("pull request %s: read_on %q is not a date", pr.Ref, pr.ReadOn)
		}
	}
	listed := map[string]bool{}
	dirs := map[string]bool{}
	for _, pa := range f.Patches {
		listed[pa.File] = true
		if f.Pin(pa.Pin) == nil {
			bad("patch %s: pin %q is not in pins", pa.File, pa.Pin)
		} else if d := f.Pin(pa.Pin).Patches; d != "" && filepath.Dir(pa.File) != filepath.Clean(d) {
			bad("patch %s: not in its pin's patches directory %s", pa.File, d)
		}
		if _, err := os.Stat(filepath.Join(root, pa.File)); err != nil {
			bad("patch %s does not exist", pa.File)
		}
		dirs[filepath.Dir(pa.File)] = true
	}
	for _, pin := range f.Pins {
		if pin.Patches == "" {
			continue
		}
		m, _ := filepath.Glob(filepath.Join(root, pin.Patches, "*.patch"))
		for _, file := range m {
			rel, _ := filepath.Rel(root, file)
			if !listed[filepath.ToSlash(rel)] {
				bad("patch %s is not listed in upstream.yaml", filepath.ToSlash(rel))
			}
		}
	}
	for _, c := range f.Copied {
		who := strings.Join(c.Here, ", ")
		n := 0
		if c.From.Pin != "" {
			n++
			if f.Pin(c.From.Pin) == nil {
				bad("copied %s: pin %q is not in pins", who, c.From.Pin)
			}
		}
		if c.From.PR != "" {
			n++
			if f.PR(c.From.PR) == nil {
				bad("copied %s: pull request %s is not in pull_requests", who, c.From.PR)
			}
		}
		if c.From.WrittenHere {
			n++
		}
		if n != 1 {
			bad("copied %s: from needs exactly one of pin, pr and written_here", who)
		}
		if !c.From.WrittenHere && len(c.From.Paths) == 0 {
			bad("copied %s: from.paths is empty", who)
		}
		for _, ref := range c.AlsoPRs {
			if f.PR(ref) == nil {
				bad("copied %s: also_prs %s is not in pull_requests", who, ref)
			}
		}
		for _, h := range c.Here {
			if m, _ := filepath.Glob(filepath.Join(root, h)); len(m) == 0 {
				bad("copied: %s does not exist here", h)
			}
		}
	}
	for _, s := range f.OtherSources {
		if !shortSHA.MatchString(s.Commit) {
			bad("other source %s: commit %q is not a SHA", s.Repo, s.Commit)
		}
	}
	return p
}

// Table is one Markdown table of UPSTREAM.md.
type Table struct {
	Section string // the nearest "## " heading above it ("" before any)
	Header  []string
	Rows    [][]string
}

// Tables reads every pipe table of a Markdown document.
func Tables(md []byte) []Table {
	var out []Table
	section := ""
	var cur *Table
	for _, line := range strings.Split(string(md), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "## ") {
			section = strings.TrimSpace(strings.TrimPrefix(t, "## "))
		}
		if !strings.HasPrefix(t, "|") {
			cur = nil
			continue
		}
		cells := splitRow(t)
		if cur == nil {
			out = append(out, Table{Section: section, Header: cells})
			cur = &out[len(out)-1]
			continue
		}
		if isSeparator(cells) {
			continue
		}
		cur.Rows = append(cur.Rows, cells)
	}
	return out
}

// all is the header and the rows: a blank line inside a table makes the
// rest a new table whose header is a data row.
func (t Table) all() [][]string { return append([][]string{t.Header}, t.Rows...) }

func splitRow(t string) []string {
	t = strings.TrimSuffix(strings.TrimPrefix(t, "|"), "|")
	parts := strings.Split(t, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func isSeparator(cells []string) bool {
	for _, c := range cells {
		if strings.Trim(c, ":- ") != "" {
			return false
		}
	}
	return true
}

var (
	codeSpan  = regexp.MustCompile("`([^`]+)`")
	shaSpan   = regexp.MustCompile("^([0-9a-f]{7,40})(…|\\.\\.\\.)?$")
	prNumber  = regexp.MustCompile(`(?:([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+))?#(\d+)`)
	patchName = regexp.MustCompile(`\b\d{4}-[A-Za-z0-9_.-]+\.patch\b`)
	braces    = regexp.MustCompile(`\{([^{}]*)\}`)
)

// spans returns the code spans of a cell.
func spans(cell string) []string {
	var s []string
	for _, m := range codeSpan.FindAllStringSubmatch(cell, -1) {
		s = append(s, m[1])
	}
	return s
}

// SHAs are the hex code spans of 7 to 40 digits in a cell (a trailing …
// marks an abbreviation). An all-digit span is taken as a number, not a
// SHA.
func SHAs(cell string) []string {
	var out []string
	for _, s := range spans(cell) {
		m := shaSpan.FindStringSubmatch(s)
		if m == nil || strings.Trim(m[1], "0123456789") == "" {
			continue
		}
		out = append(out, m[1])
	}
	return out
}

// HerePaths expands a "Here" cell into paths: code spans that look like
// paths, with {a,b} expanded, and a bare file name taking the directory of
// the path before it ("`a/b/x.go`, `y.go`" is a/b/x.go and a/b/y.go).
func HerePaths(cell string) []string {
	var out []string
	dir := ""
	for _, s := range spans(cell) {
		if strings.ContainsAny(s, " ()") || !(strings.Contains(s, "/") || strings.Contains(s, ".")) {
			continue
		}
		for _, p := range expand(s) {
			if !strings.Contains(strings.TrimSuffix(p, "/"), "/") && dir != "" {
				p = dir + "/" + p
			}
			if i := strings.LastIndexByte(strings.TrimSuffix(p, "/"), '/'); i >= 0 {
				dir = p[:i]
			}
			out = append(out, p)
		}
	}
	return out
}

func expand(s string) []string {
	m := braces.FindStringSubmatchIndex(s)
	if m == nil {
		return []string{s}
	}
	var out []string
	for _, alt := range strings.Split(s[m[2]:m[3]], ",") {
		out = append(out, expand(s[:m[0]]+alt+s[m[1]:])...)
	}
	return out
}

// sectionHas says whether a section heading contains any of the words.
func sectionHas(section string, words ...string) bool {
	s := strings.ToLower(section)
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// Agree checks that upstream.yaml and UPSTREAM.md say the same thing:
//
//   - every commit in a table of UPSTREAM.md (full or abbreviated) is a
//     pin, a nearest release, a pull request head or another source of
//     upstream.yaml, and each of those is in UPSTREAM.md; a pin's version
//     is in the row of its commit (or, with no commit, the row named
//     after it);
//   - the pull requests of the sections about pull requests (number and
//     head) are upstream.yaml's, both ways;
//   - the patch files named in UPSTREAM.md are upstream.yaml's, both ways;
//   - the paths in the first column of the "copied" table are the
//     `here` paths of upstream.yaml, both ways.
func (f *File) Agree(md []byte) []string {
	var p []string
	bad := func(format string, a ...any) { p = append(p, fmt.Sprintf(format, a...)) }
	tables := Tables(md)

	// Commits.
	known := map[string]string{} // full or short SHA -> what it is
	add := func(sha, what string) {
		if sha != "" {
			known[sha] = what
		}
	}
	for _, pin := range f.Pins {
		add(pin.Commit, "pin "+pin.Name)
		add(pin.NearestReleaseCommit, "nearest release of "+pin.Name)
	}
	for _, pr := range f.PullRequests {
		add(pr.Head, "head of "+pr.Ref)
	}
	for _, s := range f.OtherSources {
		add(s.Commit, "source "+s.Repo)
	}
	match := func(sha string) bool {
		for k := range known {
			if strings.HasPrefix(k, sha) || strings.HasPrefix(sha, k) {
				return true
			}
		}
		return false
	}
	seen := map[string]bool{}
	for _, t := range tables {
		for _, row := range t.all() {
			for _, cell := range row {
				for _, sha := range SHAs(cell) {
					if !match(sha) {
						bad("UPSTREAM.md (%s) names commit %s, which upstream.yaml does not have", sectionOr(t.Section), sha)
					}
					for k := range known {
						if strings.HasPrefix(k, sha) || strings.HasPrefix(sha, k) {
							seen[k] = true
						}
					}
				}
			}
		}
	}
	var ks []string
	for k := range known {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	for _, k := range ks {
		if !seen[k] {
			bad("upstream.yaml's %s commit %s is not in UPSTREAM.md's tables", known[k], k)
		}
	}
	for _, pin := range f.Pins {
		if pin.Version == "" && pin.NearestRelease == "" {
			continue
		}
		row := findRow(tables, func(r []string) bool {
			if pin.Commit != "" {
				for _, c := range r {
					for _, sha := range SHAs(c) {
						if strings.HasPrefix(pin.Commit, sha) || strings.HasPrefix(sha, pin.Commit) {
							return true
						}
					}
				}
				return false
			}
			return strings.Trim(r[0], "` ") == pin.Name
		})
		if pin.Version != "" {
			if row == nil {
				bad("pin %s: no row of UPSTREAM.md names it", pin.Name)
			} else if !strings.Contains(strings.Join(row, "|"), pin.Version) {
				bad("pin %s: UPSTREAM.md's row does not say version %s", pin.Name, pin.Version)
			}
		}
		if pin.NearestRelease != "" && !strings.Contains(string(md), "`"+pin.NearestRelease+"`") {
			bad("pin %s: nearest release %s is not in UPSTREAM.md", pin.Name, pin.NearestRelease)
		}
	}

	// Pull requests: rows of sections about pull requests.
	mdPRs := map[int]string{} // number -> head (or "" when the row has none)
	for _, t := range tables {
		if !sectionHas(t.Section, "pull request", "watched upstream") {
			continue
		}
		if len(t.Header) == 2 {
			// A two-column table about one pull request: "Watched PR | #n"
			// and "PR head | `sha`" rows.
			n, head := 0, ""
			for _, row := range t.Rows {
				switch strings.Trim(row[0], "` ") {
				case "Watched PR", "PR":
					if m := prNumber.FindStringSubmatch(row[1]); m != nil {
						n, _ = strconv.Atoi(m[2])
					}
				case "PR head", "Head":
					if s := SHAs(row[1]); len(s) > 0 {
						head = s[0]
					}
				}
			}
			if n != 0 {
				mdPRs[n] = head
			}
			continue
		}
		// One pull request per row: its number in the first column, its
		// head the first commit in the others.
		for _, row := range t.Rows {
			m := prNumber.FindStringSubmatch(row[0])
			if m == nil {
				continue
			}
			n, _ := strconv.Atoi(m[2])
			head := ""
			for _, c := range row[1:] {
				if s := SHAs(c); len(s) > 0 {
					head = s[0]
					break
				}
			}
			mdPRs[n] = head
		}
	}
	ymlPRs := map[int]PR{}
	for _, pr := range f.PullRequests {
		_, n, err := SplitPR(pr.Ref)
		if err == nil {
			ymlPRs[n] = pr
		}
	}
	for n, head := range mdPRs {
		pr, ok := ymlPRs[n]
		if !ok {
			bad("UPSTREAM.md lists pull request #%d, which upstream.yaml does not", n)
			continue
		}
		if head != "" && !strings.HasPrefix(pr.Head, head) && !strings.HasPrefix(head, pr.Head) {
			bad("pull request %s: UPSTREAM.md says head %s, upstream.yaml %s", pr.Ref, head, pr.Head)
		}
	}
	for n, pr := range ymlPRs {
		if _, ok := mdPRs[n]; !ok && !strings.Contains(string(md), "#"+strconv.Itoa(n)) {
			bad("upstream.yaml lists pull request %s, which UPSTREAM.md does not", pr.Ref)
		}
	}

	// Patches.
	mdPatches := map[string]bool{}
	for _, t := range tables {
		for _, row := range t.all() {
			for _, m := range patchName.FindAllString(row[0], -1) {
				mdPatches[m] = true
			}
		}
	}
	ymlPatches := map[string]bool{}
	for _, pa := range f.Patches {
		ymlPatches[filepath.Base(pa.File)] = true
	}
	for _, n := range sortedKeys(mdPatches) {
		if !ymlPatches[n] {
			bad("UPSTREAM.md lists patch %s, which upstream.yaml does not", n)
		}
	}
	for _, n := range sortedKeys(ymlPatches) {
		if !mdPatches[n] {
			bad("upstream.yaml lists patch %s, which UPSTREAM.md's tables do not", n)
		}
	}

	// Copied files: the first column of the tables in sections about what
	// is copied.
	mdHere := map[string]bool{}
	for _, t := range tables {
		if !sectionHas(t.Section, "copied") || sectionHas(t.Section, "pull request", "not copied") || len(t.Header) < 3 {
			continue
		}
		for _, row := range t.Rows {
			for _, h := range HerePaths(row[0]) {
				mdHere[h] = true
			}
		}
	}
	ymlHere := map[string]bool{}
	for _, c := range f.Copied {
		for _, h := range c.Here {
			ymlHere[h] = true
		}
	}
	for _, h := range sortedKeys(mdHere) {
		if !ymlHere[h] {
			bad("UPSTREAM.md's copied table has %s, which upstream.yaml does not", h)
		}
	}
	for _, h := range sortedKeys(ymlHere) {
		if !mdHere[h] {
			bad("upstream.yaml's copied has %s, which UPSTREAM.md's table does not", h)
		}
	}
	return p
}

func sectionOr(s string) string {
	if s == "" {
		return "the table at the top"
	}
	return s
}

func findRow(tables []Table, ok func([]string) bool) []string {
	for _, t := range tables {
		for _, r := range t.Rows {
			if len(r) > 0 && ok(r) {
				return r
			}
		}
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Check loads root/upstream.yaml and checks it on its own, against the
// repository's files and against root/UPSTREAM.md.
func Check(root string) ([]string, error) {
	f, err := Load(filepath.Join(root, "upstream.yaml"))
	if err != nil {
		return nil, err
	}
	md, err := os.ReadFile(filepath.Join(root, "UPSTREAM.md"))
	if err != nil {
		return nil, err
	}
	return append(f.Problems(root), f.Agree(md)...), nil
}
