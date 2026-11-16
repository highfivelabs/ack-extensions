// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
)

// resourceLister is the part of client-go's discovery client servedTypes
// uses.
type resourceLister interface {
	ServerResourcesForGroupVersion(groupVersion string) (*metav1.APIResourceList, error)
}

// servedTypes keeps the types whose CRD the API server serves. Each kind
// served costs about 0.36 MiB at rest (docs/cloudcontrol.md section 11,
// RECCC: 533 MB for all 1,345), and a controller for a kind with no CRD has
// nothing to watch; in a cluster that installs only the Cloud Control CRDs
// it uses, the manager is then sized to those.
//
// With every kind asked for (no --cloudcontrol-kinds) the types without a
// CRD are left out and named in the log. Kinds asked for by name must all
// be installed: one that is not is an error, so the manager does not start
// serving less than it was told to without saying so.
func servedTypes(d resourceLister, types []*registry.Type, explicit bool) (served []*registry.Type, missing []string, err error) {
	have := map[string]map[string]bool{}
	for _, t := range types {
		gv := t.GVK().GroupVersion().String()
		if _, done := have[gv]; done {
			continue
		}
		kinds := map[string]bool{}
		list, err := d.ServerResourcesForGroupVersion(gv)
		switch {
		case apierrors.IsNotFound(err):
			// No CRD of this group installed.
		case err != nil:
			return nil, nil, fmt.Errorf("discovery of %s: %w", gv, err)
		default:
			for _, r := range list.APIResources {
				if !strings.Contains(r.Name, "/") {
					kinds[r.Kind] = true
				}
			}
		}
		have[gv] = kinds
	}
	for _, t := range types {
		if have[t.GVK().GroupVersion().String()][t.Kind] {
			served = append(served, t)
		} else {
			missing = append(missing, t.Ref())
		}
	}
	sort.Strings(missing)
	if explicit && len(missing) > 0 {
		return nil, missing, fmt.Errorf("no CRD installed for %s", strings.Join(missing, ", "))
	}
	return served, missing, nil
}
