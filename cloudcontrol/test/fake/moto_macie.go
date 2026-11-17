// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/macie2"
	macietypes "github.com/aws/aws-sdk-go-v2/service/macie2/types"
)

// ---- AWS::Macie::Session on moto's Macie2 ----

// MacieSession is the account's Macie session singleton, identified by
// AwsAccountId. moto 5.2.5 implements EnableMacie, GetMacieSession,
// UpdateMacieSession and DisableMacie.
type MacieSession struct {
	M       *macie2.Client
	Account string
}

// NewMacieSession makes the backend.
func NewMacieSession(cfg aws.Config, account string) *MacieSession {
	return &MacieSession{M: macie2.NewFromConfig(cfg), Account: account}
}

func (b *MacieSession) Create(ctx context.Context, m map[string]any) (string, error) {
	in := &macie2.EnableMacieInput{}
	if f := str(m, "FindingPublishingFrequency"); f != "" {
		in.FindingPublishingFrequency = macietypes.FindingPublishingFrequency(f)
	}
	if s := str(m, "Status"); s != "" {
		in.Status = macietypes.MacieStatus(s)
	}
	if _, err := b.M.EnableMacie(ctx, in); err != nil {
		return "", InvalidRequest("%v", err)
	}
	return b.Account, nil
}

func (b *MacieSession) Read(ctx context.Context, id string) (map[string]any, error) {
	out, err := b.M.GetMacieSession(ctx, &macie2.GetMacieSessionInput{})
	if err != nil {
		if c := code(err); c == "ResourceNotFoundException" || c == "AccessDeniedException" {
			return nil, NotFound("macie session %s", id)
		}
		return nil, err
	}
	m := map[string]any{
		"AwsAccountId": b.Account,
		"Status":       string(out.Status),
	}
	if out.FindingPublishingFrequency != "" {
		m["FindingPublishingFrequency"] = string(out.FindingPublishingFrequency)
	}
	if out.ServiceRole != nil {
		m["ServiceRole"] = aws.ToString(out.ServiceRole)
	}
	return m, nil
}

func (b *MacieSession) Update(ctx context.Context, _ string, _, next map[string]any) error {
	in := &macie2.UpdateMacieSessionInput{}
	if f := str(next, "FindingPublishingFrequency"); f != "" {
		in.FindingPublishingFrequency = macietypes.FindingPublishingFrequency(f)
	}
	if s := str(next, "Status"); s != "" {
		in.Status = macietypes.MacieStatus(s)
	}
	if _, err := b.M.UpdateMacieSession(ctx, in); err != nil {
		return InvalidRequest("%v", err)
	}
	return nil
}

func (b *MacieSession) Delete(ctx context.Context, id string) error {
	if _, err := b.Read(ctx, id); err != nil {
		return err
	}
	_, err := b.M.DisableMacie(ctx, &macie2.DisableMacieInput{})
	return err
}

// ---- AWS::Macie::FindingsFilter on moto's Macie2 ----

// MacieFindingsFilter is a Macie findings filter, identified by Id.
// moto 5.2.5 implements Create/Get/Update/DeleteFindingsFilter.
type MacieFindingsFilter struct{ M *macie2.Client }

// NewMacieFindingsFilter makes the backend.
func NewMacieFindingsFilter(cfg aws.Config) *MacieFindingsFilter {
	return &MacieFindingsFilter{M: macie2.NewFromConfig(cfg)}
}

func macieCriteria(v any) *macietypes.FindingCriteria {
	top, _ := v.(map[string]any)
	raw, _ := top["Criterion"].(map[string]any)
	crit := map[string]macietypes.CriterionAdditionalProperties{}
	for k, cv := range raw {
		cm, _ := cv.(map[string]any)
		var p macietypes.CriterionAdditionalProperties
		p.Eq = strSlice(cm["eq"])
		p.Neq = strSlice(cm["neq"])
		p.EqExactMatch = strSlice(cm["eqExactMatch"])
		if n, ok := cm["gt"].(float64); ok {
			p.Gt = aws.Int64(int64(n))
		}
		if n, ok := cm["gte"].(float64); ok {
			p.Gte = aws.Int64(int64(n))
		}
		if n, ok := cm["lt"].(float64); ok {
			p.Lt = aws.Int64(int64(n))
		}
		if n, ok := cm["lte"].(float64); ok {
			p.Lte = aws.Int64(int64(n))
		}
		crit[k] = p
	}
	return &macietypes.FindingCriteria{Criterion: crit}
}

func cfnMacieCriteria(fc *macietypes.FindingCriteria) map[string]any {
	crit := map[string]any{}
	if fc != nil {
		for k, p := range fc.Criterion {
			cm := map[string]any{}
			if len(p.Eq) > 0 {
				cm["eq"] = anySlice(p.Eq)
			}
			if len(p.Neq) > 0 {
				cm["neq"] = anySlice(p.Neq)
			}
			if len(p.EqExactMatch) > 0 {
				cm["eqExactMatch"] = anySlice(p.EqExactMatch)
			}
			if p.Gt != nil {
				cm["gt"] = *p.Gt
			}
			if p.Gte != nil {
				cm["gte"] = *p.Gte
			}
			if p.Lt != nil {
				cm["lt"] = *p.Lt
			}
			if p.Lte != nil {
				cm["lte"] = *p.Lte
			}
			crit[k] = cm
		}
	}
	return map[string]any{"Criterion": crit}
}

func strSlice(v any) []string {
	l, _ := v.([]any)
	var out []string
	for _, x := range l {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func anySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func (b *MacieFindingsFilter) Create(ctx context.Context, m map[string]any) (string, error) {
	in := &macie2.CreateFindingsFilterInput{
		Name:            aws.String(str(m, "Name")),
		FindingCriteria: macieCriteria(m["FindingCriteria"]),
		Action:          macietypes.FindingsFilterAction(str(m, "Action")),
	}
	if in.Action == "" {
		in.Action = macietypes.FindingsFilterActionNoop
	}
	if d := str(m, "Description"); d != "" {
		in.Description = aws.String(d)
	}
	if p, ok := m["Position"].(float64); ok {
		in.Position = aws.Int32(int32(p))
	}
	if t := tagsOf(m["Tags"]); len(t) > 0 {
		in.Tags = t
	}
	out, err := b.M.CreateFindingsFilter(ctx, in)
	if err != nil {
		return "", InvalidRequest("%v", err)
	}
	return aws.ToString(out.Id), nil
}

func (b *MacieFindingsFilter) Read(ctx context.Context, id string) (map[string]any, error) {
	out, err := b.M.GetFindingsFilter(ctx, &macie2.GetFindingsFilterInput{Id: aws.String(id)})
	if err != nil {
		if code(err) == "ResourceNotFoundException" {
			return nil, NotFound("findings filter %s", id)
		}
		return nil, err
	}
	m := map[string]any{
		"Id":              aws.ToString(out.Id),
		"Arn":             aws.ToString(out.Arn),
		"Name":            aws.ToString(out.Name),
		"Action":          string(out.Action),
		"FindingCriteria": cfnMacieCriteria(out.FindingCriteria),
	}
	if out.Description != nil {
		m["Description"] = aws.ToString(out.Description)
	}
	if out.Position != nil {
		m["Position"] = int64(*out.Position)
	}
	if len(out.Tags) > 0 {
		m["Tags"] = tagList(out.Tags)
	}
	return m, nil
}

func (b *MacieFindingsFilter) Update(ctx context.Context, id string, _, next map[string]any) error {
	in := &macie2.UpdateFindingsFilterInput{
		Id:              aws.String(id),
		Name:            aws.String(str(next, "Name")),
		FindingCriteria: macieCriteria(next["FindingCriteria"]),
		Action:          macietypes.FindingsFilterAction(str(next, "Action")),
	}
	if in.Action == "" {
		in.Action = macietypes.FindingsFilterActionNoop
	}
	if d := str(next, "Description"); d != "" {
		in.Description = aws.String(d)
	}
	if p, ok := next["Position"].(float64); ok {
		in.Position = aws.Int32(int32(p))
	}
	if _, err := b.M.UpdateFindingsFilter(ctx, in); err != nil {
		return InvalidRequest("%v", err)
	}
	return nil
}

func (b *MacieFindingsFilter) Delete(ctx context.Context, id string) error {
	if _, err := b.Read(ctx, id); err != nil {
		return err
	}
	_, err := b.M.DeleteFindingsFilter(ctx, &macie2.DeleteFindingsFilterInput{Id: aws.String(id)})
	return err
}
