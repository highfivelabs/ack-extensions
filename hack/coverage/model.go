// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

func readFile(p string) ([]byte, error) { return os.ReadFile(p) }

const oursGroupSuffix = ".aws.hfl.dev"

// model is everything the outputs are written from.
type model struct {
	src  *sources
	maps *mappings
	// cfn resources by "service/Name", and the sorted keys and services.
	cfn      map[string]*cfnResource
	cfnKeys  []string
	services []string
	// covs by resource key.
	official map[string][]*cov
	ours     map[string][]*cov
	// outsideOfficial and outsideOurs are kinds with no CloudFormation
	// resource.
	outsideOfficial []*cov
	outsideOurs     []*cov
	// ackServicesAbsent lists ACK service ids with no CloudFormation service.
	ackServicesAbsent []string
	unmappedOfficial  int // CRD spec paths that matched no CloudFormation attribute
	unmappedOurs      int
	howOfficial       map[string]int
	howOurs           map[string]int
	cfnReadOnly       int
	cfnCut            int
}

func build(c config) (*model, error) {
	src, err := loadSources(c)
	if err != nil {
		return nil, err
	}
	maps, err := loadMappings(c)
	if err != nil {
		return nil, err
	}
	m := &model{src: src, maps: maps, cfn: map[string]*cfnResource{},
		official: map[string][]*cov{}, ours: map[string][]*cov{},
		howOfficial: map[string]int{}, howOurs: map[string]int{}}

	zb, err := cfnZip(c, src)
	if err != nil {
		return nil, err
	}
	names, files, err := cfnSchemas(zb)
	if err != nil {
		return nil, err
	}
	svcSet := map[string]bool{}
	for _, n := range names {
		r, err := parseCFN(files[n])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		if !strings.HasPrefix(r.TypeName, "AWS::") {
			continue
		}
		k := r.Service + "/" + r.Name
		m.cfn[k] = r
		m.cfnReadOnly += r.ReadOnlyLeaves
		m.cfnCut += r.Cut
		svcSet[r.Service] = true
	}
	for k := range m.cfn {
		m.cfnKeys = append(m.cfnKeys, k)
	}
	sort.Slice(m.cfnKeys, func(i, j int) bool {
		li, lj := strings.ToLower(m.cfnKeys[i]), strings.ToLower(m.cfnKeys[j])
		if li != lj {
			return li < lj
		}
		return m.cfnKeys[i] < m.cfnKeys[j]
	})
	for s := range svcSet {
		m.services = append(m.services, s)
	}
	sort.Strings(m.services)

	// Official. Fetch the pinned CRDs first, in parallel; they are cached.
	if err := prefetch(c, src); err != nil {
		return nil, err
	}
	absent := map[string]bool{}
	for _, ctl := range src.ACK.Controllers {
		for _, p := range ctl.CRDs {
			b, err := crdBytes(c, ctl, p)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", ctl.Repo, p, err)
			}
			crd, err := parseCRD(b)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", ctl.Repo, p, err)
			}
			cv := &cov{Kind: crd.Kind, Svc: ctl.Name, Release: ctl.Release, CRD: crd}
			if !m.resolve(cv, maps.Resources, ctl.Name) {
				if !svcSet[m.cfnService(ctl.Name)] {
					absent[ctl.Name] = true
				}
				m.outsideOfficial = append(m.outsideOfficial, cv)
				continue
			}
			m.addMatch(cv, maps.Attributes[ctl.Name+"/"+crd.Kind], m.official, m.howOfficial, &m.unmappedOfficial)
		}
	}
	for s := range absent {
		m.ackServicesAbsent = append(m.ackServicesAbsent, s)
	}
	sort.Strings(m.ackServicesAbsent)

	// Ours.
	files2, err := filepath.Glob(filepath.Join(c.root, "config", "crds", "bases", "*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files2)
	for _, f := range files2 {
		b, err := readFile(f)
		if err != nil {
			return nil, err
		}
		crd, err := parseCRD(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		svc := strings.TrimSuffix(crd.Group, oursGroupSuffix)
		key := svc + "/" + crd.Kind
		pr := maps.FromUpstreamPR[key]
		targets := maps.Ours[key]
		if len(targets) == 0 {
			cv := &cov{Kind: crd.Kind, Svc: svc, CRD: crd, PR: pr}
			if m.resolve(cv, nil, svc) {
				targets = []string{cv.Resource}
			} else {
				targets = []string{outside}
			}
		}
		for _, t := range targets {
			cv := &cov{Kind: crd.Kind, Svc: svc, CRD: crd, PR: pr, Resource: t}
			if t == outside {
				cv.Resource = ""
				m.outsideOurs = append(m.outsideOurs, cv)
				continue
			}
			if _, ok := m.cfn[t]; !ok {
				return nil, fmt.Errorf("mappings.yaml: ours %s maps to %s, which is not a CloudFormation resource", key, t)
			}
			m.addMatch(cv, maps.OursAttributes[key], m.ours, m.howOurs, &m.unmappedOurs)
		}
	}
	return m, nil
}

func (m *model) cfnService(svc string) string {
	if s, ok := m.maps.Services[svc]; ok {
		return s
	}
	return svc
}

// resolve finds the CloudFormation resource of a kind. It returns false if
// there is none (the kind is outside the denominator).
func (m *model) resolve(cv *cov, explicit map[string]string, svc string) bool {
	if t, ok := explicit[svc+"/"+cv.Kind]; ok {
		if t == outside {
			return false
		}
		if _, ok := m.cfn[t]; !ok {
			panic(fmt.Sprintf("mappings.yaml: %s/%s maps to %s, which is not a CloudFormation resource", svc, cv.Kind, t))
		}
		cv.Resource = t
		return true
	}
	cs := m.cfnService(svc)
	want := normName(cv.Kind)
	for _, k := range m.cfnKeys {
		r := m.cfn[k]
		if r.Service == cs && normName(r.Name) == want {
			cv.Resource = k
			return true
		}
	}
	return false
}

func (m *model) addMatch(cv *cov, alias map[string]string, into map[string][]*cov, how map[string]int, unmapped *int) {
	res := m.cfn[cv.Resource]
	cv.Match, cv.Tiers, cv.How, cv.Unmapped = matchAttrs(res, cv.CRD, alias)
	for k, v := range cv.How {
		how[k] += v
	}
	*unmapped += len(cv.Unmapped)
	into[cv.Resource] = append(into[cv.Resource], cv)
}

// prefetch downloads every pinned CRD into the cache with a few workers.
func prefetch(c config, src *sources) error {
	type job struct {
		ctl controller
		p   string
	}
	jobs := make(chan job)
	errs := make(chan error, 1)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if _, err := crdBytes(c, j.ctl, j.p); err != nil {
					select {
					case errs <- fmt.Errorf("%s %s: %w", j.ctl.Repo, j.p, err):
					default:
					}
				}
			}
		}()
	}
	for _, ctl := range src.ACK.Controllers {
		for _, p := range ctl.CRDs {
			jobs <- job{ctl, p}
		}
	}
	close(jobs)
	wg.Wait()
	select {
	case err := <-errs:
		return err
	default:
		return nil
	}
}
