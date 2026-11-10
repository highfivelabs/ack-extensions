// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53resolver"
	r53rtypes "github.com/aws/aws-sdk-go-v2/service/route53resolver/types"
)

// F08 and F09, DNS, on moto's Route 53 and Route 53 Resolver. moto 5.2.5
// has the query logging and resolver DNSSEC calls these handlers make, and
// none of the DNS Firewall, resolver configuration, DNSSEC signing, key-
// signing key or CIDR collection calls (those types are unproven on the
// fake). Two differences from AWS the backends make up for (probed
// 2026-11-09):
//   - a query logging configuration has no ARN in moto (Route 53 has no
//     ARN in its API either; CloudFormation's handler makes one from the
//     ID), so the backend makes it the same way;
//   - moto's UpdateResolverDnssecConfig takes a VPC that does not exist,
//     where AWS refuses it; the backend looks the VPC up first, as the
//     handler does (ec2:DescribeVpcs is in its permissions).

// ---- AWS::Route53::QueryLoggingConfig ----

// QueryLoggingConfig sends a public hosted zone's DNS query logs to a
// CloudWatch Logs log group. Both properties are create-only, and the type
// has no update handler; the identifier is the configuration's ARN.
type QueryLoggingConfig struct{ R53 *route53.Client }

// NewQueryLoggingConfig makes the backend.
func NewQueryLoggingConfig(cfg aws.Config) *QueryLoggingConfig {
	return &QueryLoggingConfig{R53: route53.NewFromConfig(cfg)}
}

const queryLoggingARN = "arn:aws:route53:::queryloggingconfig/"

func (b *QueryLoggingConfig) Create(ctx context.Context, m map[string]any) (string, error) {
	out, err := b.R53.CreateQueryLoggingConfig(ctx, &route53.CreateQueryLoggingConfigInput{
		HostedZoneId:              aws.String(str(m, "HostedZoneId")),
		CloudWatchLogsLogGroupArn: aws.String(str(m, "CloudWatchLogsLogGroupArn")),
	})
	if err != nil {
		if code(err) == "QueryLoggingConfigAlreadyExists" {
			return "", &HandlerError{"AlreadyExists", err.Error()}
		}
		return "", InvalidRequest("%v", err)
	}
	return queryLoggingARN + aws.ToString(out.QueryLoggingConfig.Id), nil
}

func (b *QueryLoggingConfig) Read(ctx context.Context, arn string) (map[string]any, error) {
	id := strings.TrimPrefix(arn, queryLoggingARN)
	out, err := b.R53.GetQueryLoggingConfig(ctx, &route53.GetQueryLoggingConfigInput{Id: aws.String(id)})
	if err != nil {
		if code(err) == "NoSuchQueryLoggingConfig" {
			return nil, NotFound("%s", arn)
		}
		return nil, err
	}
	q := out.QueryLoggingConfig
	return map[string]any{
		"Arn": arn, "Id": aws.ToString(q.Id),
		"HostedZoneId":              aws.ToString(q.HostedZoneId),
		"CloudWatchLogsLogGroupArn": aws.ToString(q.CloudWatchLogsLogGroupArn),
	}, nil
}

func (b *QueryLoggingConfig) Update(context.Context, string, map[string]any, map[string]any) error {
	return &HandlerError{"NotUpdatable", "AWS::Route53::QueryLoggingConfig has no update handler"}
}

func (b *QueryLoggingConfig) Delete(ctx context.Context, arn string) error {
	_, err := b.R53.DeleteQueryLoggingConfig(ctx, &route53.DeleteQueryLoggingConfigInput{Id: aws.String(strings.TrimPrefix(arn, queryLoggingARN))})
	if code(err) == "NoSuchQueryLoggingConfig" {
		return NotFound("%s", arn)
	}
	return err
}

// ---- AWS::Route53Resolver::ResolverDNSSECConfig ----

// ResolverDNSSECConfig turns on DNSSEC validation for a VPC's Resolver. A
// create enables it, a delete disables it; a configuration whose
// validation is off reads as gone. ResourceId (the VPC) is create-only and
// the type has no update handler; the identifier is the configuration's
// ID (rdsc-...).
type ResolverDNSSECConfig struct {
	R  *route53resolver.Client
	EC *ec2.Client
}

// NewResolverDNSSECConfig makes the backend.
func NewResolverDNSSECConfig(cfg aws.Config) *ResolverDNSSECConfig {
	return &ResolverDNSSECConfig{R: route53resolver.NewFromConfig(cfg), EC: ec2.NewFromConfig(cfg)}
}

func (b *ResolverDNSSECConfig) Create(ctx context.Context, m map[string]any) (string, error) {
	vpc := str(m, "ResourceId")
	if _, err := b.EC.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{vpc}}); err != nil {
		return "", InvalidRequest("ResourceId %q: %v", vpc, err)
	}
	out, err := b.R.UpdateResolverDnssecConfig(ctx, &route53resolver.UpdateResolverDnssecConfigInput{
		ResourceId: aws.String(vpc), Validation: r53rtypes.ValidationEnable,
	})
	if err != nil {
		return "", InvalidRequest("%v", err)
	}
	return aws.ToString(out.ResolverDNSSECConfig.Id), nil
}

func (b *ResolverDNSSECConfig) find(ctx context.Context, id string) (*r53rtypes.ResolverDnssecConfig, error) {
	p := route53resolver.NewListResolverDnssecConfigsPaginator(b.R, &route53resolver.ListResolverDnssecConfigsInput{})
	for p.HasMorePages() {
		out, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for i := range out.ResolverDnssecConfigs {
			c := &out.ResolverDnssecConfigs[i]
			if aws.ToString(c.Id) != id {
				continue
			}
			switch c.ValidationStatus {
			case r53rtypes.ResolverDNSSECValidationStatusDisabled, r53rtypes.ResolverDNSSECValidationStatusDisabling:
				return nil, NotFound("%s", id)
			}
			return c, nil
		}
	}
	return nil, NotFound("%s", id)
}

func (b *ResolverDNSSECConfig) Read(ctx context.Context, id string) (map[string]any, error) {
	c, err := b.find(ctx, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"Id": id, "OwnerId": aws.ToString(c.OwnerId),
		"ResourceId": aws.ToString(c.ResourceId), "ValidationStatus": string(c.ValidationStatus),
	}, nil
}

func (b *ResolverDNSSECConfig) Update(context.Context, string, map[string]any, map[string]any) error {
	return &HandlerError{"NotUpdatable", "AWS::Route53Resolver::ResolverDNSSECConfig has no update handler"}
}

func (b *ResolverDNSSECConfig) Delete(ctx context.Context, id string) error {
	c, err := b.find(ctx, id)
	if err != nil {
		return err
	}
	_, err = b.R.UpdateResolverDnssecConfig(ctx, &route53resolver.UpdateResolverDnssecConfigInput{
		ResourceId: c.ResourceId, Validation: r53rtypes.ValidationDisable,
	})
	return err
}
