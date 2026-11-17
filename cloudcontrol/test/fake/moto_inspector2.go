// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/inspector2"
	insp2types "github.com/aws/aws-sdk-go-v2/service/inspector2/types"
)

// ---- AWS::InspectorV2::Filter on moto's Inspector2 ----

// InspectorFilter is an Inspector v2 findings filter, identified by Arn.
// moto 5.2.5 implements Create/List/Update/DeleteFilter.
type InspectorFilter struct{ I *inspector2.Client }

// NewInspectorFilter makes the backend.
func NewInspectorFilter(cfg aws.Config) *InspectorFilter {
	return &InspectorFilter{I: inspector2.NewFromConfig(cfg)}
}

func inspStringFilters(v any) []insp2types.StringFilter {
	l, _ := v.([]any)
	var out []insp2types.StringFilter
	for _, x := range l {
		m, _ := x.(map[string]any)
		out = append(out, insp2types.StringFilter{
			Comparison: insp2types.StringComparison(str(m, "Comparison")),
			Value:      aws.String(str(m, "Value")),
		})
	}
	return out
}

func cfnInspStringFilters(sf []insp2types.StringFilter) []any {
	var out []any
	for _, s := range sf {
		out = append(out, map[string]any{
			"Comparison": string(s.Comparison),
			"Value":      aws.ToString(s.Value),
		})
	}
	return out
}

func inspCriteria(v any) *insp2types.FilterCriteria {
	m, _ := v.(map[string]any)
	fc := &insp2types.FilterCriteria{}
	if s := inspStringFilters(m["Severity"]); len(s) > 0 {
		fc.Severity = s
	}
	if s := inspStringFilters(m["FindingStatus"]); len(s) > 0 {
		fc.FindingStatus = s
	}
	if s := inspStringFilters(m["FindingType"]); len(s) > 0 {
		fc.FindingType = s
	}
	if s := inspStringFilters(m["ResourceType"]); len(s) > 0 {
		fc.ResourceType = s
	}
	if s := inspStringFilters(m["AwsAccountId"]); len(s) > 0 {
		fc.AwsAccountId = s
	}
	return fc
}

func cfnInspCriteria(fc *insp2types.FilterCriteria) map[string]any {
	m := map[string]any{}
	if fc == nil {
		return m
	}
	if len(fc.Severity) > 0 {
		m["Severity"] = cfnInspStringFilters(fc.Severity)
	}
	if len(fc.FindingStatus) > 0 {
		m["FindingStatus"] = cfnInspStringFilters(fc.FindingStatus)
	}
	if len(fc.FindingType) > 0 {
		m["FindingType"] = cfnInspStringFilters(fc.FindingType)
	}
	if len(fc.ResourceType) > 0 {
		m["ResourceType"] = cfnInspStringFilters(fc.ResourceType)
	}
	if len(fc.AwsAccountId) > 0 {
		m["AwsAccountId"] = cfnInspStringFilters(fc.AwsAccountId)
	}
	return m
}

func stringMap(v any) map[string]string {
	m, _ := v.(map[string]any)
	out := map[string]string{}
	for k, val := range m {
		if s, ok := val.(string); ok {
			out[k] = s
		}
	}
	return out
}

func (b *InspectorFilter) Create(ctx context.Context, m map[string]any) (string, error) {
	in := &inspector2.CreateFilterInput{
		Name:           aws.String(str(m, "Name")),
		Action:         insp2types.FilterAction(str(m, "FilterAction")),
		FilterCriteria: inspCriteria(m["FilterCriteria"]),
	}
	if d := str(m, "Description"); d != "" {
		in.Description = aws.String(d)
	}
	if t := stringMap(m["Tags"]); len(t) > 0 {
		in.Tags = t
	}
	out, err := b.I.CreateFilter(ctx, in)
	if err != nil {
		return "", InvalidRequest("%v", err)
	}
	return aws.ToString(out.Arn), nil
}

func (b *InspectorFilter) Read(ctx context.Context, arn string) (map[string]any, error) {
	out, err := b.I.ListFilters(ctx, &inspector2.ListFiltersInput{Arns: []string{arn}})
	if err != nil {
		return nil, err
	}
	if len(out.Filters) == 0 {
		return nil, NotFound("inspector filter %s", arn)
	}
	f := out.Filters[0]
	m := map[string]any{
		"Arn":            aws.ToString(f.Arn),
		"Name":           aws.ToString(f.Name),
		"FilterAction":   string(f.Action),
		"FilterCriteria": cfnInspCriteria(f.Criteria),
	}
	if f.Description != nil {
		m["Description"] = aws.ToString(f.Description)
	}
	if len(f.Tags) > 0 {
		tm := map[string]any{}
		for k, v := range f.Tags {
			tm[k] = v
		}
		m["Tags"] = tm
	}
	return m, nil
}

func (b *InspectorFilter) Update(context.Context, string, map[string]any, map[string]any) error {
	return InvalidRequest("moto 5.2.5 has no UpdateFilter")
}

func (b *InspectorFilter) Delete(ctx context.Context, arn string) error {
	if _, err := b.Read(ctx, arn); err != nil {
		return err
	}
	_, err := b.I.DeleteFilter(ctx, &inspector2.DeleteFilterInput{Arn: aws.String(arn)})
	return err
}
