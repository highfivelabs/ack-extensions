// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/guardduty"
	gdtypes "github.com/aws/aws-sdk-go-v2/service/guardduty/types"
)

// ---- AWS::GuardDuty::Filter on moto's GuardDuty ----

// Filter is the GuardDuty findings filter backend, identified as
// <detector>|<name>. moto 5.2.5 implements Create/Get/Update/DeleteFilter
// with the name, action, description, criteria and rank; it has no tags for
// filters (and no TagResource), so this backend keeps Tags itself, and it
// overwrites a filter of the same name where GuardDuty refuses one, so the
// create checks first.
type Filter struct {
	GD   *guardduty.Client
	mu   sync.Mutex
	tags map[string]any // identifier -> Tags
}

// NewFilter makes the backend.
func NewFilter(cfg aws.Config) *Filter {
	return &Filter{GD: guardduty.NewFromConfig(cfg), tags: map[string]any{}}
}

func filterID(id string) (detector, name string, err error) {
	d, n, ok := strings.Cut(id, "|")
	if !ok {
		return "", "", NotFound("filter %s", id)
	}
	return d, n, nil
}

// findingCriteria is CloudFormation's FindingCriteria as the SDK's: the
// condition keys (Eq, Gt, GreaterThan, ...) are the SDK's field names. The
// SDK pinned here has no Matches and NotMatches, which the schema has, so
// those are dropped and not proven.
func findingCriteria(v any) (*gdtypes.FindingCriteria, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	out := &gdtypes.FindingCriteria{}
	if err := json.Unmarshal(b, out); err != nil {
		return nil, InvalidRequest("FindingCriteria: %v", err)
	}
	return out, nil
}

// cfnFindingCriteria is the SDK's FindingCriteria as CloudFormation's,
// without the conditions it does not set.
func cfnFindingCriteria(fc *gdtypes.FindingCriteria) map[string]any {
	if fc == nil {
		return nil
	}
	crit := map[string]any{}
	for field, c := range fc.Criterion {
		m := map[string]any{}
		for k, l := range map[string][]string{"Eq": c.Eq, "Neq": c.Neq, "Equals": c.Equals, "NotEquals": c.NotEquals} {
			if l != nil {
				var a []any
				for _, s := range l {
					a = append(a, s)
				}
				m[k] = a
			}
		}
		for k, p := range map[string]*int32{"Gt": c.Gt, "Gte": c.Gte, "Lt": c.Lt, "Lte": c.Lte} {
			if p != nil {
				m[k] = int64(*p)
			}
		}
		for k, p := range map[string]*int64{"GreaterThan": c.GreaterThan, "GreaterThanOrEqual": c.GreaterThanOrEqual, "LessThan": c.LessThan, "LessThanOrEqual": c.LessThanOrEqual} {
			if p != nil {
				m[k] = *p
			}
		}
		crit[field] = m
	}
	return map[string]any{"Criterion": crit}
}

func (b *Filter) Create(ctx context.Context, m map[string]any) (string, error) {
	det, name := str(m, "DetectorId"), str(m, "Name")
	id := det + "|" + name
	if _, err := b.Read(ctx, id); err == nil {
		return "", &HandlerError{"AlreadyExists", "filter " + name + " exists in detector " + det}
	}
	fc, err := findingCriteria(m["FindingCriteria"])
	if err != nil {
		return "", err
	}
	in := &guardduty.CreateFilterInput{DetectorId: aws.String(det), Name: aws.String(name), FindingCriteria: fc}
	if a := str(m, "Action"); a != "" {
		in.Action = gdtypes.FilterAction(a)
	}
	if d := str(m, "Description"); d != "" {
		in.Description = aws.String(d)
	}
	if r, ok := m["Rank"].(float64); ok {
		in.Rank = aws.Int32(int32(r))
	}
	if _, err := b.GD.CreateFilter(ctx, in); err != nil {
		return "", InvalidRequest("%v", err)
	}
	b.mu.Lock()
	if t, ok := m["Tags"]; ok {
		b.tags[id] = t
	}
	b.mu.Unlock()
	return id, nil
}

func (b *Filter) Read(ctx context.Context, id string) (map[string]any, error) {
	det, name, err := filterID(id)
	if err != nil {
		return nil, err
	}
	out, err := b.GD.GetFilter(ctx, &guardduty.GetFilterInput{DetectorId: aws.String(det), FilterName: aws.String(name)})
	if err != nil {
		if c := code(err); c == "BadRequestException" || c == "InvalidInputException" {
			return nil, NotFound("filter %s", id)
		}
		return nil, err
	}
	m := map[string]any{
		"DetectorId":      det,
		"Name":            aws.ToString(out.Name),
		"Action":          string(out.Action),
		"FindingCriteria": cfnFindingCriteria(out.FindingCriteria),
	}
	if out.Description != nil {
		m["Description"] = aws.ToString(out.Description)
	}
	if out.Rank != nil {
		m["Rank"] = int64(*out.Rank)
	}
	b.mu.Lock()
	if t, ok := b.tags[id]; ok {
		m["Tags"] = t
	}
	b.mu.Unlock()
	return m, nil
}

func (b *Filter) Update(ctx context.Context, id string, _, next map[string]any) error {
	det, name, err := filterID(id)
	if err != nil {
		return err
	}
	fc, err := findingCriteria(next["FindingCriteria"])
	if err != nil {
		return err
	}
	in := &guardduty.UpdateFilterInput{DetectorId: aws.String(det), FilterName: aws.String(name), FindingCriteria: fc, Description: aws.String(str(next, "Description"))}
	if a := str(next, "Action"); a != "" {
		in.Action = gdtypes.FilterAction(a)
	}
	if r, ok := next["Rank"].(float64); ok {
		in.Rank = aws.Int32(int32(r))
	}
	if _, err := b.GD.UpdateFilter(ctx, in); err != nil {
		return InvalidRequest("%v", err)
	}
	b.mu.Lock()
	if t, ok := next["Tags"]; ok {
		b.tags[id] = t
	} else {
		delete(b.tags, id)
	}
	b.mu.Unlock()
	return nil
}

func (b *Filter) Delete(ctx context.Context, id string) error {
	if _, err := b.Read(ctx, id); err != nil {
		return err
	}
	det, name, _ := filterID(id)
	if _, err := b.GD.DeleteFilter(ctx, &guardduty.DeleteFilterInput{DetectorId: aws.String(det), FilterName: aws.String(name)}); err != nil {
		return err
	}
	b.mu.Lock()
	delete(b.tags, id)
	b.mu.Unlock()
	return nil
}
