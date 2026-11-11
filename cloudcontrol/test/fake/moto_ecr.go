// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
)

// ---- AWS::ECR::RegistryPolicy on moto's ECR ----

// RegistryPolicy is the backend of the registry's permissions policy, one
// per account and Region, identified by the registry (account) ID. moto
// 5.2.5 implements Put/Get/DeleteRegistryPolicy. A create while the
// registry has a policy fails AlreadyExists, as the handler of a
// singleton does (unverified on real AWS).
type RegistryPolicy struct{ ECR *ecr.Client }

// NewRegistryPolicy makes the backend.
func NewRegistryPolicy(cfg aws.Config) *RegistryPolicy {
	return &RegistryPolicy{ECR: ecr.NewFromConfig(cfg)}
}

func (b *RegistryPolicy) put(ctx context.Context, m map[string]any) (string, error) {
	pol, err := policyString(m["PolicyText"])
	if err != nil {
		return "", err
	}
	out, err := b.ECR.PutRegistryPolicy(ctx, &ecr.PutRegistryPolicyInput{PolicyText: aws.String(pol)})
	if err != nil {
		return "", InvalidRequest("%v", err)
	}
	return aws.ToString(out.RegistryId), nil
}

func (b *RegistryPolicy) Create(ctx context.Context, m map[string]any) (string, error) {
	if out, err := b.ECR.GetRegistryPolicy(ctx, &ecr.GetRegistryPolicyInput{}); err == nil && out.PolicyText != nil {
		return "", &HandlerError{"AlreadyExists", "registry " + aws.ToString(out.RegistryId) + " has a policy"}
	}
	return b.put(ctx, m)
}

func (b *RegistryPolicy) Read(ctx context.Context, id string) (map[string]any, error) {
	out, err := b.ECR.GetRegistryPolicy(ctx, &ecr.GetRegistryPolicyInput{})
	if err != nil {
		if code(err) == "RegistryPolicyNotFoundException" {
			return nil, NotFound("registry %s has no policy", id)
		}
		return nil, err
	}
	if aws.ToString(out.RegistryId) != id {
		return nil, NotFound("registry %s", id)
	}
	var pol any = aws.ToString(out.PolicyText)
	var obj map[string]any
	if json.Unmarshal([]byte(aws.ToString(out.PolicyText)), &obj) == nil {
		pol = obj
	}
	return map[string]any{"RegistryId": id, "PolicyText": pol}, nil
}

func (b *RegistryPolicy) Update(ctx context.Context, _ string, _, next map[string]any) error {
	_, err := b.put(ctx, next)
	return err
}

func (b *RegistryPolicy) Delete(ctx context.Context, id string) error {
	if _, err := b.Read(ctx, id); err != nil {
		return err
	}
	_, err := b.ECR.DeleteRegistryPolicy(ctx, &ecr.DeleteRegistryPolicyInput{})
	return err
}

// ---- AWS::ECR::RegistryScanningConfiguration on moto's ECR ----

// RegistryScanningConfiguration is the backend of the registry's scanning
// configuration, one per account and Region, identified by the registry
// ID. ECR has no call to delete it: there is always a configuration (BASIC,
// no rules, by default), so this backend keeps whether the resource
// exists, and its delete puts the default back (what the handler is taken
// to do; unverified on real AWS). moto 5.2.5 implements
// Put/GetRegistryScanningConfiguration.
type RegistryScanningConfiguration struct {
	ECR *ecr.Client

	mu     sync.Mutex
	exists map[string]bool
}

// NewRegistryScanningConfiguration makes the backend.
func NewRegistryScanningConfiguration(cfg aws.Config) *RegistryScanningConfiguration {
	return &RegistryScanningConfiguration{ECR: ecr.NewFromConfig(cfg), exists: map[string]bool{}}
}

func (b *RegistryScanningConfiguration) put(ctx context.Context, m map[string]any) (string, error) {
	// Rules always sent, empty for none: moto 5.2.5 fails (500) without them.
	in := &ecr.PutRegistryScanningConfigurationInput{ScanType: ecrtypes.ScanType(str(m, "ScanType")), Rules: []ecrtypes.RegistryScanningRule{}}
	rules, _ := m["Rules"].([]any)
	for _, r := range rules {
		rm, _ := r.(map[string]any)
		rule := ecrtypes.RegistryScanningRule{ScanFrequency: ecrtypes.ScanFrequency(str(rm, "ScanFrequency"))}
		fs, _ := rm["RepositoryFilters"].([]any)
		for _, f := range fs {
			fm, _ := f.(map[string]any)
			rule.RepositoryFilters = append(rule.RepositoryFilters, ecrtypes.ScanningRepositoryFilter{
				Filter: aws.String(str(fm, "Filter")), FilterType: ecrtypes.ScanningRepositoryFilterType(str(fm, "FilterType")),
			})
		}
		in.Rules = append(in.Rules, rule)
	}
	out, err := b.ECR.PutRegistryScanningConfiguration(ctx, in)
	if err != nil {
		return "", InvalidRequest("%v", err)
	}
	_ = out
	got, err := b.ECR.GetRegistryScanningConfiguration(ctx, &ecr.GetRegistryScanningConfigurationInput{})
	if err != nil {
		return "", err
	}
	return aws.ToString(got.RegistryId), nil
}

func (b *RegistryScanningConfiguration) Create(ctx context.Context, m map[string]any) (string, error) {
	got, err := b.ECR.GetRegistryScanningConfiguration(ctx, &ecr.GetRegistryScanningConfigurationInput{})
	if err != nil {
		return "", err
	}
	id := aws.ToString(got.RegistryId)
	b.mu.Lock()
	had := b.exists[id]
	b.mu.Unlock()
	if had {
		return "", &HandlerError{"AlreadyExists", "registry " + id + " has a scanning configuration resource"}
	}
	if id, err = b.put(ctx, m); err != nil {
		return "", err
	}
	b.mu.Lock()
	b.exists[id] = true
	b.mu.Unlock()
	return id, nil
}

func (b *RegistryScanningConfiguration) Read(ctx context.Context, id string) (map[string]any, error) {
	b.mu.Lock()
	ok := b.exists[id]
	b.mu.Unlock()
	if !ok {
		return nil, NotFound("registry %s has no scanning configuration resource", id)
	}
	got, err := b.ECR.GetRegistryScanningConfiguration(ctx, &ecr.GetRegistryScanningConfigurationInput{})
	if err != nil {
		return nil, err
	}
	m := map[string]any{"RegistryId": id}
	rules := []any{}
	if c := got.ScanningConfiguration; c != nil {
		m["ScanType"] = string(c.ScanType)
		for _, r := range c.Rules {
			fs := []any{}
			for _, f := range r.RepositoryFilters {
				fs = append(fs, map[string]any{"Filter": aws.ToString(f.Filter), "FilterType": string(f.FilterType)})
			}
			rules = append(rules, map[string]any{"ScanFrequency": string(r.ScanFrequency), "RepositoryFilters": fs})
		}
	}
	m["Rules"] = rules
	return m, nil
}

func (b *RegistryScanningConfiguration) Update(ctx context.Context, _ string, _, next map[string]any) error {
	_, err := b.put(ctx, next)
	return err
}

func (b *RegistryScanningConfiguration) Delete(ctx context.Context, id string) error {
	if _, err := b.Read(ctx, id); err != nil {
		return err
	}
	if _, err := b.put(ctx, map[string]any{"ScanType": "BASIC"}); err != nil {
		return err
	}
	b.mu.Lock()
	delete(b.exists, id)
	b.mu.Unlock()
	return nil
}
