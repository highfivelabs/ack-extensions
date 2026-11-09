// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// explain prints how one resource was matched: every attribute and what
// covers it, then the CRD paths that matched nothing. It is how the spot
// checks in docs/coverage/README.md were done.
func explain(w io.Writer, m *model, r *results, resource string) error {
	res, ok := m.cfn[resource]
	if !ok {
		var near []string
		for k := range m.cfn {
			if strings.EqualFold(k, resource) {
				resource, res, ok = k, m.cfn[k], true
				break
			}
			if strings.HasPrefix(strings.ToLower(k), strings.ToLower(strings.SplitN(resource, "/", 2)[0])+"/") {
				near = append(near, k)
			}
		}
		if !ok {
			sort.Strings(near)
			return fmt.Errorf("no CloudFormation resource %q; in that service: %s", resource, strings.Join(near, " "))
		}
	}
	fmt.Fprintf(w, "%s (%s): %d writable attributes, %d read-only leaves removed\n", resource, res.TypeName, len(res.Attrs), res.ReadOnlyLeaves)
	for _, side := range []struct {
		name string
		covs []*cov
	}{{"official", m.official[resource]}, {"ours", m.ours[resource]}} {
		for _, cv := range side.covs {
			fmt.Fprintf(w, "%s kind %s.%s %s: %d spec paths, matched %d, how %v\n", side.name, cv.Svc, cv.Kind, cv.Release, len(cv.CRD.Attrs), len(cv.Match), cv.How)
		}
	}
	for _, a := range r.attrs {
		if a.service+"/"+a.resource == resource {
			tier := ""
			for _, cv := range append(append([]*cov(nil), m.official[resource]...), m.ours[resource]...) {
				if t, ok := cv.Tiers[a.a.key()]; ok {
					tier += " [" + t + "]"
				}
			}
			fmt.Fprintf(w, "  %-9s %-60s official=%s ours=%s%s\n", a.covered, a.a.Path, a.officialPath, a.oursPath, tier)
		}
	}
	for _, side := range []struct {
		name string
		covs []*cov
	}{{"official", m.official[resource]}, {"ours", m.ours[resource]}} {
		for _, cv := range side.covs {
			fmt.Fprintf(w, "%s %s unmapped spec paths (%d): %s\n", side.name, cv.Kind, len(cv.Unmapped), strings.Join(cv.Unmapped, " "))
		}
	}
	return nil
}
