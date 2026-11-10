// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/securityhub"
	shtypes "github.com/aws/aws-sdk-go-v2/service/securityhub/types"
)

// ---- AWS::SecurityHub::Hub on moto's Security Hub ----

// Hub is the Security Hub backend: one hub per account and region,
// identified by its ARN. moto 5.2.5 implements Enable/Describe/
// DisableSecurityHub; it has no UpdateSecurityHubConfiguration and no
// TagResource, its DescribeHub always answers AutoEnableControls true and
// ControlFindingGenerator SECURITY_CONTROL, and the SDK's DescribeHub has
// no tags; so this backend keeps ControlFindingGenerator,
// AutoEnableControls and Tags itself, and an update of them changes only
// what it keeps.
type Hub struct {
	SH    *securityhub.Client
	mu    sync.Mutex
	extra map[string]map[string]any // ARN -> properties moto lacks
}

// NewHub makes the backend.
func NewHub(cfg aws.Config) *Hub {
	return &Hub{SH: securityhub.NewFromConfig(cfg), extra: map[string]map[string]any{}}
}

var hubExtra = []string{"ControlFindingGenerator", "AutoEnableControls", "Tags"}

func (b *Hub) Create(ctx context.Context, m map[string]any) (string, error) {
	if _, err := b.SH.DescribeHub(ctx, &securityhub.DescribeHubInput{}); err == nil {
		return "", &HandlerError{"AlreadyExists", "Security Hub is already enabled in this account and region"}
	}
	in := &securityhub.EnableSecurityHubInput{}
	if v, ok := m["EnableDefaultStandards"].(bool); ok {
		in.EnableDefaultStandards = aws.Bool(v)
	}
	if v := str(m, "ControlFindingGenerator"); v != "" {
		in.ControlFindingGenerator = shtypes.ControlFindingGenerator(v)
	}
	if t, ok := m["Tags"].(map[string]any); ok {
		in.Tags = map[string]string{}
		for k, v := range t {
			in.Tags[k] = fmt.Sprint(v)
		}
	}
	if _, err := b.SH.EnableSecurityHub(ctx, in); err != nil {
		return "", InvalidRequest("%v", err)
	}
	out, err := b.SH.DescribeHub(ctx, &securityhub.DescribeHubInput{})
	if err != nil {
		return "", err
	}
	arn := aws.ToString(out.HubArn)
	b.mu.Lock()
	b.extra[arn] = map[string]any{}
	for _, k := range hubExtra {
		if v, ok := m[k]; ok {
			b.extra[arn][k] = v
		}
	}
	b.mu.Unlock()
	return arn, nil
}

func (b *Hub) Read(ctx context.Context, arn string) (map[string]any, error) {
	out, err := b.SH.DescribeHub(ctx, &securityhub.DescribeHubInput{HubArn: aws.String(arn)})
	if err != nil {
		if c := code(err); c == "InvalidAccessException" || c == "ResourceNotFoundException" {
			return nil, NotFound("hub %s", arn)
		}
		return nil, err
	}
	m := map[string]any{
		"ARN":                     arn,
		"SubscribedAt":            aws.ToString(out.SubscribedAt),
		"AutoEnableControls":      aws.ToBool(out.AutoEnableControls),
		"ControlFindingGenerator": string(out.ControlFindingGenerator),
	}
	b.mu.Lock()
	for k, v := range b.extra[arn] {
		m[k] = v
	}
	b.mu.Unlock()
	return m, nil
}

func (b *Hub) Update(ctx context.Context, arn string, _, next map[string]any) error {
	if _, err := b.Read(ctx, arn); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.extra[arn] = map[string]any{}
	for _, k := range hubExtra {
		if v, ok := next[k]; ok {
			b.extra[arn][k] = v
		}
	}
	return nil
}

func (b *Hub) Delete(ctx context.Context, arn string) error {
	if _, err := b.Read(ctx, arn); err != nil {
		return err
	}
	if _, err := b.SH.DisableSecurityHub(ctx, &securityhub.DisableSecurityHubInput{}); err != nil {
		return err
	}
	b.mu.Lock()
	delete(b.extra, arn)
	b.mu.Unlock()
	return nil
}

// ---- AWS::SecurityHub::OrganizationConfiguration on moto's Security Hub ----

// OrganizationConfiguration is the backend of an organization's Security
// Hub configuration, set by the delegated administrator: there is always
// one, so the resource exists from its create to its delete, which turns
// auto-enable off (AutoEnable false, AutoEnableStandards NONE). It is
// identified as <account>/<region>/securityhub-organization-configuration
// (the schema's pattern). moto 5.2.5 implements Update/
// DescribeOrganizationConfiguration for the delegated administrator only.
// moto refuses a ConfigurationType without a Status, which AWS sets
// itself, so the backend sends ENABLED with it.
type OrganizationConfiguration struct {
	SH              *securityhub.Client
	Account, Region string
	mu              sync.Mutex
	made            map[string]bool
}

// NewOrganizationConfiguration makes the backend for an account and region.
func NewOrganizationConfiguration(cfg aws.Config, account, region string) *OrganizationConfiguration {
	return &OrganizationConfiguration{SH: securityhub.NewFromConfig(cfg), Account: account, Region: region, made: map[string]bool{}}
}

func (b *OrganizationConfiguration) id() string {
	return b.Account + "/" + b.Region + "/securityhub-organization-configuration"
}

func (b *OrganizationConfiguration) put(ctx context.Context, m map[string]any) error {
	auto, _ := m["AutoEnable"].(bool)
	in := &securityhub.UpdateOrganizationConfigurationInput{AutoEnable: aws.Bool(auto)}
	if v := str(m, "AutoEnableStandards"); v != "" {
		in.AutoEnableStandards = shtypes.AutoEnableStandards(v)
	}
	if v := str(m, "ConfigurationType"); v != "" {
		in.OrganizationConfiguration = &shtypes.OrganizationConfiguration{
			ConfigurationType: shtypes.OrganizationConfigurationConfigurationType(v),
			Status:            shtypes.OrganizationConfigurationStatusEnabled,
		}
	}
	if _, err := b.SH.UpdateOrganizationConfiguration(ctx, in); err != nil {
		return InvalidRequest("%v", err)
	}
	return nil
}

func (b *OrganizationConfiguration) Create(ctx context.Context, m map[string]any) (string, error) {
	id := b.id()
	b.mu.Lock()
	made := b.made[id]
	b.mu.Unlock()
	if made {
		return "", &HandlerError{"AlreadyExists", "the organization configuration " + id + " is managed already"}
	}
	if err := b.put(ctx, m); err != nil {
		return "", err
	}
	b.mu.Lock()
	b.made[id] = true
	b.mu.Unlock()
	return id, nil
}

func (b *OrganizationConfiguration) Read(ctx context.Context, id string) (map[string]any, error) {
	b.mu.Lock()
	made := b.made[id]
	b.mu.Unlock()
	if !made {
		return nil, NotFound("organization configuration %s", id)
	}
	out, err := b.SH.DescribeOrganizationConfiguration(ctx, &securityhub.DescribeOrganizationConfigurationInput{})
	if err != nil {
		return nil, err
	}
	m := map[string]any{
		"OrganizationConfigurationIdentifier": id,
		"AutoEnable":                          aws.ToBool(out.AutoEnable),
		"AutoEnableStandards":                 string(out.AutoEnableStandards),
		"MemberAccountLimitReached":           aws.ToBool(out.MemberAccountLimitReached),
	}
	if oc := out.OrganizationConfiguration; oc != nil {
		m["ConfigurationType"] = string(oc.ConfigurationType)
		m["Status"] = string(oc.Status)
		if s := aws.ToString(oc.StatusMessage); s != "" {
			m["StatusMessage"] = s
		}
	}
	return m, nil
}

func (b *OrganizationConfiguration) Update(ctx context.Context, id string, _, next map[string]any) error {
	if _, err := b.Read(ctx, id); err != nil {
		return err
	}
	return b.put(ctx, next)
}

func (b *OrganizationConfiguration) Delete(ctx context.Context, id string) error {
	if _, err := b.Read(ctx, id); err != nil {
		return err
	}
	if err := b.put(ctx, map[string]any{"AutoEnable": false, "AutoEnableStandards": "NONE"}); err != nil {
		return err
	}
	b.mu.Lock()
	delete(b.made, id)
	b.mu.Unlock()
	return nil
}
