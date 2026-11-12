// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package slot_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/highfivelabs/ack-extensions/pkg/slot"
)

var (
	readsTags = regexp.MustCompile(`(^|[^.\w])ko\.Spec\.Tags(, err)? = `)
	groupRe   = regexp.MustCompile(`Group:\s*"([^"]+)"`)
	kindRe    = regexp.MustCompile(`Kind:\s*"([^"]+)"`)
)

// TestTaggedKinds keeps slot.TaggedKinds equal to the ACK-generated kinds
// whose read (sdkFind or a hook) fills spec.tags from AWS: a new kind
// that reads its tags joins the guard, and one listed that stops reading
// them leaves it.
func TestTaggedKinds(t *testing.T) {
	root := filepath.Join("..", "..", "services")
	dirs, err := filepath.Glob(filepath.Join(root, "*", "pkg", "resource", "*"))
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, d := range dirs {
		// The read is sdkFind in sdk.go (generated), or a hook in hooks.go
		// (hand-written, called from sdkFind).
		reads := false
		if b, err := os.ReadFile(filepath.Join(d, "sdk.go")); err == nil {
			s := string(b)
			if i := strings.Index(s, "func (rm *resourceManager) sdkFind("); i >= 0 {
				body := s[i:]
				if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
					body = body[:j+1]
				}
				reads = readsTags.MatchString(body)
			}
		}
		if b, err := os.ReadFile(filepath.Join(d, "hooks.go")); err == nil && readsTags.Match(b) {
			reads = true
		}
		if !reads {
			continue
		}
		desc, err := os.ReadFile(filepath.Join(d, "descriptor.go"))
		if err != nil {
			t.Fatal(err)
		}
		g, k := groupRe.FindSubmatch(desc), kindRe.FindSubmatch(desc)
		if g == nil || k == nil {
			t.Fatalf("%s: no GroupKind in descriptor.go", d)
		}
		found = append(found, string(k[1])+"."+string(g[1]))
	}
	sort.Strings(found)
	var listed []string
	for k := range slot.TaggedKinds {
		listed = append(listed, k)
	}
	sort.Strings(listed)
	if strings.Join(found, ",") != strings.Join(listed, ",") {
		t.Errorf("slot.TaggedKinds = %v\nthe kinds whose read fills spec.tags = %v", listed, found)
	}
}
