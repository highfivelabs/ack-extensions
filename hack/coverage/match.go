// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// mappings is hack/coverage/mappings.yaml: the hand-kept parts of the match.
type mappings struct {
	// Services maps an ACK service id (or the service in one of our API
	// groups) to the CloudFormation service name, lower case, where they
	// differ.
	Services map[string]string `json:"services"`
	// Resources maps "<ack service id>/<Kind>" to "<cfn service>/<Resource>"
	// where the kind's name does not match the CloudFormation resource's, or
	// to "outside-denominator" where CloudFormation has no such resource.
	Resources map[string]string `json:"resources"`
	// Ours is the same for our kinds, keyed "<group service>/<Kind>", with
	// a list of resources (a kind may stand for a property of several), or
	// ["outside-denominator"].
	Ours map[string][]string `json:"ours"`
	// FromUpstreamPR lists our kinds whose coverage is copied from an open
	// ACK pull request, "<group service>/<Kind>": "<repo>#<number>".
	FromUpstreamPR map[string]string `json:"fromUpstreamPR"`
	// Attributes maps, per official kind "<ack service id>/<Kind>", a CRD
	// spec path to the CloudFormation attribute path it stands for, where
	// the names differ. OursAttributes is the same for our kinds.
	Attributes     map[string]map[string]string `json:"attributes"`
	OursAttributes map[string]map[string]string `json:"oursAttributes"`
	// Notes adds a note to a resource row, keyed "<cfn service>/<Resource>".
	Notes map[string]string `json:"notes"`
}

func loadMappings(c config) (*mappings, error) {
	b, err := readFile(c.mapping)
	if err != nil {
		return nil, err
	}
	var m mappings
	if err := yaml.UnmarshalStrict(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", c.mapping, err)
	}
	return &m, nil
}

const outside = "outside-denominator"

// cov is one CRD's coverage of one CloudFormation resource.
type cov struct {
	Kind     string
	Svc      string // the ACK service id or our group's service
	Release  string // official: the controller release; ours: ""
	Resource string // service/Name, "" when outside the denominator
	CRD      *crdKind
	// Match maps a CloudFormation attribute key to the CRD path that covers it.
	Match map[string]string
	// Tiers names the matching tier of each match.
	Tiers map[string]string
	// How counts matches by rule.
	How map[string]int
	// Unmapped lists CRD spec paths that matched no CloudFormation attribute.
	Unmapped []string
	PR       string
}

// matcher holds one resource's CloudFormation attributes and one CRD's.
type matcher struct {
	cfn     []attr
	crd     []attr
	crdSegs [][]string // each CRD attr's segments after alias rewriting
	cfnHit  []int      // index of the CRD attr covering cfn[i], -1 if none
	crdUsed []bool
	how     map[string]int
	tier    []string // the tier that matched cfn[i]
}

// matchAttrs matches the CRD's leaf paths against the resource's, in
// tiers from exact to loose. A looser tier only sees what the tighter
// ones left. See docs/coverage/README.md.
func matchAttrs(res *cfnResource, crd *crdKind, alias map[string]string) (match map[string]string, tiers map[string]string, how map[string]int, unmapped []string) {
	m := &matcher{cfn: res.Attrs, crd: crd.Attrs, how: map[string]int{}}
	m.cfnHit = make([]int, len(m.cfn))
	m.tier = make([]string, len(m.cfn))
	for i := range m.cfnHit {
		m.cfnHit[i] = -1
	}
	m.crdUsed = make([]bool, len(m.crd))
	m.crdSegs = make([][]string, len(m.crd))
	rules := parseAliases(alias)
	rewritten := make([]bool, len(m.crd))
	for j, a := range m.crd {
		m.crdSegs[j], rewritten[j] = rules.rewrite(a.Segs)
	}

	// Tier 1: exact path (after alias rewriting).
	byKey := map[string]int{}
	for j := range m.crd {
		k := strings.Join(m.crdSegs[j], ".")
		if _, dup := byKey[k]; !dup {
			byKey[k] = j
		}
	}
	for i, ca := range m.cfn {
		if j, ok := byKey[ca.key()]; ok && !m.crdUsed[j] {
			name := "exact"
			if rewritten[j] {
				name = "alias"
			}
			m.hit(i, j, name)
		}
	}
	// Tier 2: CloudFormation tag list against a CRD map.
	for i, ca := range m.cfn {
		if m.cfnHit[i] >= 0 {
			continue
		}
		if j, ok := m.tagsMap(ca); ok {
			m.hitShared(i, j, "tags-map")
		}
	}
	// Tier 3: the CRD takes a free-form value above the CloudFormation path.
	for i, ca := range m.cfn {
		if m.cfnHit[i] >= 0 {
			continue
		}
		for j, c := range m.crd {
			if c.Free && len(m.crdSegs[j]) < len(ca.Segs) && hasPrefix(ca.Segs, m.crdSegs[j]) {
				m.hitShared(i, j, "free-form")
				break
			}
		}
	}
	// Tier 4: CloudFormation takes a free-form value where the CRD has structure.
	for i, ca := range m.cfn {
		if m.cfnHit[i] >= 0 || !ca.Free {
			continue
		}
		first := -1
		for j := range m.crd {
			if hasPrefixName(m.crdSegs[j], ca.Segs) {
				if first < 0 {
					first = j
				}
				m.crdUsed[j] = true
			}
		}
		if first >= 0 {
			m.cfnHit[i] = first
			m.tier[i] = "structured"
			m.how["structured"]++
		}
	}
	// Tier 4b: ACK's `name` field is the resource's name or identifier.
	m.primaryName(res)
	// Tier 4c: ACK's `description` is CloudFormation's `GroupDescription`: one
	// name ends with the other, under the same parent, unique on both sides.
	m.suffixNames()
	// Tier 5: canonical names (plurals, id/identifier, *Tags, case) at the same path.
	m.unique("name-variant", func(a attr) string { return canonKey(a.Segs) }, func(j int) string { return canonKey(m.crdSegs[j]) })
	// Tier 6: the same last k segments (a renamed or dropped wrapper above
	// them), unique among what is left on both sides; k from 4 down to 2.
	for k := 4; k >= 2; k-- {
		k := k
		m.unique(fmt.Sprintf("tail-%d", k), func(a attr) string { return tailKey(a.Segs, a.Type, k) }, func(j int) string { return tailKey(m.crdSegs[j], m.crd[j].Type, k) })
	}
	// Tier 7: the same leaf name, unique among what is left on both sides.
	m.unique("leaf-name", func(a attr) string { return tailKey(a.Segs, a.Type, 1) }, func(j int) string { return tailKey(m.crdSegs[j], m.crd[j].Type, 1) })

	match = map[string]string{}
	tiers = map[string]string{}
	for i, ca := range m.cfn {
		if j := m.cfnHit[i]; j >= 0 {
			match[ca.key()] = m.crd[j].Path
			tiers[ca.key()] = m.tier[i]
		}
	}
	for j, a := range m.crd {
		if !m.crdUsed[j] {
			unmapped = append(unmapped, a.Path)
		}
	}
	sort.Strings(unmapped)
	return match, tiers, m.how, unmapped
}

func (m *matcher) hit(i, j int, how string) {
	m.cfnHit[i] = j
	m.tier[i] = how
	m.crdUsed[j] = true
	m.how[how]++
}

// hitShared is hit where several CloudFormation attributes may share one CRD
// attribute (a free-form value standing for a subtree).
func (m *matcher) hitShared(i, j int, how string) {
	m.cfnHit[i] = j
	m.tier[i] = how
	m.crdUsed[j] = true
	m.how[how]++
}

// unique matches the left-over attributes whose key is borne by exactly one
// left-over CloudFormation attribute and exactly one left-over CRD attribute.
func (m *matcher) unique(how string, cfnKey func(attr) string, crdKey func(int) string) {
	cfnBy := map[string][]int{}
	for i, a := range m.cfn {
		if m.cfnHit[i] < 0 {
			k := cfnKey(a)
			cfnBy[k] = append(cfnBy[k], i)
		}
	}
	crdBy := map[string][]int{}
	for j := range m.crd {
		if !m.crdUsed[j] {
			k := crdKey(j)
			crdBy[k] = append(crdBy[k], j)
		}
	}
	for k, is := range cfnBy {
		js := crdBy[k]
		if k != "" && len(is) == 1 && len(js) == 1 {
			m.hit(is[0], js[0], how)
		}
	}
}

func hasPrefix(segs, prefix []string) bool {
	if len(prefix) > len(segs) {
		return false
	}
	for i := range prefix {
		if segs[i] != prefix[i] {
			return false
		}
	}
	return true
}

// hasPrefixName reports whether segs starts with the path p, comparing p's last
// segment by name only: a CloudFormation free-form value stands for a CRD list
// or map of that name and whatever is under it.
func hasPrefixName(segs, p []string) bool {
	if len(segs) < len(p) || len(p) == 0 {
		return false
	}
	if !hasPrefix(segs, p[:len(p)-1]) {
		return false
	}
	a, _ := splitMarks(segs[len(p)-1])
	b, _ := splitMarks(p[len(p)-1])
	return a == b
}

func splitMarks(s string) (name, marks string) {
	j := len(s)
	for j >= 2 && (s[j-2:j] == "[]" || s[j-2:j] == "{}") {
		j -= 2
	}
	return s[:j], s[j:]
}

// canonSeg lower-cases (already done), maps identifier to id, tags-like
// names to tags, and drops a plural s.
func canonSeg(s string) string {
	name, marks := splitMarks(s)
	name = strings.ReplaceAll(name, "identifier", "id")
	// enableFoo and fooEnabled are the same switch.
	if strings.HasPrefix(name, "tag") && len(name) >= 6 {
		name = strings.TrimPrefix(name, "tag") // tagKey, tagValue
	}
	if strings.HasPrefix(name, "enable") && len(name) >= 10 {
		name = strings.TrimPrefix(name, "enable")
	} else if strings.HasSuffix(name, "enabled") && len(name) >= 11 {
		name = strings.TrimSuffix(name, "enabled")
	}
	// fooIds and foos (names or ARNs in one API, IDs in another) are the same list.
	if strings.HasSuffix(name, "ids") && len(name) >= 8 {
		name = strings.TrimSuffix(name, "ids") + "s"
	} else if strings.HasSuffix(name, "id") && len(name) >= 6 {
		name = strings.TrimSuffix(name, "id")
	}
	if len(name) > 4 && strings.HasSuffix(name, "tags") {
		name = "tags"
	}
	switch {
	case strings.HasSuffix(name, "ies"):
		name = strings.TrimSuffix(name, "ies") + "y"
	case strings.HasSuffix(name, "ss"), strings.HasSuffix(name, "us"):
	case strings.HasSuffix(name, "s"):
		name = strings.TrimSuffix(name, "s")
	}
	return name + marks
}

func canonKey(segs []string) string {
	out := make([]string, len(segs))
	for i, s := range segs {
		out[i] = canonSeg(s)
	}
	return strings.Join(out, ".")
}

// tailKey is the last k segments, canonical, a type class (so a list of strings
// is not matched to a number) and the nesting depth. Paths shorter than k have no key.
func tailKey(segs []string, typ string, k int) string {
	if len(segs) < k {
		return ""
	}
	return canonKey(segs[len(segs)-k:]) + "|" + typeClass(typ) + "|" + fmt.Sprint(markerCount(segs))
}

// markerCount is the number of list and map levels above a path's leaf, so
// a field of a list's elements is not matched to a field of a list in a list.
func markerCount(segs []string) int {
	n := 0
	for _, s := range segs {
		_, mk := splitMarks(s)
		n += len(mk) / 2
	}
	return n
}

func typeClass(t string) string {
	// A union such as object|string (a JSON document ACK takes as a string).
	if strings.Contains(t, "|") {
		if strings.Contains(t, "string") {
			return "string"
		}
		return "object"
	}
	switch t {
	case "string", "integer", "number", "boolean":
		return t
	}
	return "object"
}

// tagsMap matches a CloudFormation tag list (Tags[].Key, Tags[].Value) to a
// CRD map of strings at the same place, how ACK models tags for services
// whose API takes a map.
func (m *matcher) tagsMap(ca attr) (int, bool) {
	n := len(ca.Segs)
	if n < 2 {
		return 0, false
	}
	last := ca.Segs[n-1]
	if last != "key" && last != "value" && last != "tagkey" && last != "tagvalue" {
		return 0, false
	}
	parent := ca.Segs[n-2]
	if !strings.HasSuffix(parent, "[]") {
		return 0, false
	}
	want := canonKey(append(append([]string(nil), ca.Segs[:n-2]...), strings.TrimSuffix(parent, "[]")+"{}"))
	for j := range m.crd {
		if canonKey(m.crdSegs[j]) == want {
			return j, true
		}
	}
	return 0, false
}

// aliasRules are the hand-kept path-prefix renames of one kind.
type aliasRules [][2][]string

func parseAliases(alias map[string]string) aliasRules {
	var r aliasRules
	keys := make([]string, 0, len(alias))
	for k := range alias {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		r = append(r, [2][]string{strings.Split(normPath(k), "."), strings.Split(normPath(alias[k]), ".")})
	}
	// The longest CRD prefix wins.
	sort.SliceStable(r, func(i, j int) bool { return len(r[i][0]) > len(r[j][0]) })
	return r
}

// rewrite replaces the first matching CRD path prefix with its CloudFormation
// prefix.
func (r aliasRules) rewrite(segs []string) ([]string, bool) {
	for _, rule := range r {
		if hasPrefix(segs, rule[0]) {
			out := append(append([]string(nil), rule[1]...), segs[len(rule[0]):]...)
			return out, true
		}
	}
	return segs, false
}

// normPath normalizes a display path the way attr.Segs does.
func normPath(p string) string {
	segs := strings.Split(p, ".")
	for i, s := range segs {
		name, marks := splitMarks(s)
		segs[i] = normName(name) + marks
	}
	return strings.Join(segs, ".")
}

// primaryName matches a CRD's top-level `name` to the one writable
// CloudFormation primary identifier (ACK names it `name` when the API's
// name is long, as in ClusterName or ReplicationInstanceIdentifier).
func (m *matcher) primaryName(res *cfnResource) {
	j := -1
	for k := range m.crd {
		if !m.crdUsed[k] && len(m.crdSegs[k]) == 1 && m.crdSegs[k][0] == "name" {
			j = k
		}
	}
	if j < 0 {
		return
	}
	cand := -1
	for i, a := range m.cfn {
		if m.cfnHit[i] >= 0 || len(a.Segs) != 1 || a.Type != "string" {
			continue
		}
		for _, p := range res.Primary {
			if normName(p) == a.Segs[0] {
				if cand >= 0 {
					return
				}
				cand = i
			}
		}
	}
	if cand >= 0 {
		m.hit(cand, j, "primary-name")
	}
}

// suffixNames matches a left-over CloudFormation attribute and a left-over CRD
// attribute with the same canonical parent path and a last name one of which
// ends with the other (policy and KeyPolicy), when each is the other's only
// candidate.
func (m *matcher) suffixNames() {
	parent := func(segs []string) string { return canonKey(segs[:len(segs)-1]) }
	last := func(segs []string) string {
		n, _ := splitMarks(segs[len(segs)-1])
		return canonSeg(n)
	}
	candOf := map[int][]int{}
	for i, ca := range m.cfn {
		if m.cfnHit[i] >= 0 {
			continue
		}
		for j := range m.crd {
			if m.crdUsed[j] || parent(ca.Segs) != parent(m.crdSegs[j]) || typeClass(ca.Type) != typeClass(m.crd[j].Type) {
				continue
			}
			x, y := last(ca.Segs), last(m.crdSegs[j])
			short, long := x, y
			if len(short) > len(long) {
				short, long = long, short
			}
			if len(short) >= 4 && short != long && strings.HasSuffix(long, short) && marksOf(ca.Segs) == marksOf(m.crdSegs[j]) {
				candOf[i] = append(candOf[i], j)
			}
		}
	}
	back := map[int][]int{}
	for i, js := range candOf {
		for _, j := range js {
			back[j] = append(back[j], i)
		}
	}
	var is []int
	for i := range candOf {
		is = append(is, i)
	}
	sort.Ints(is)
	for _, i := range is {
		js := candOf[i]
		if len(js) == 1 && len(back[js[0]]) == 1 {
			m.hit(i, js[0], "name-suffix")
		}
	}
}

func marksOf(segs []string) string {
	_, mk := splitMarks(segs[len(segs)-1])
	return mk
}
