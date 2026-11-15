// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// ---- AWS::EC2::SecurityGroupIngress and ...Egress on moto's EC2 ----

// SecurityGroupRule is the backend of one standalone security group rule,
// inbound (AWS::EC2::SecurityGroupIngress) or outbound (...Egress),
// identified by its rule ID (sgr-...), as Cloud Control gives it. moto
// 5.2.5 implements AuthorizeSecurityGroupIngress/Egress (returning the
// rule IDs), DescribeSecurityGroupRules, RevokeSecurityGroupIngress/Egress
// by rule ID and UpdateSecurityGroupRuleDescriptionsIngress/Egress. Every
// property but Description is create-only (the fake server refuses a
// change to one before it reaches here); the update sets the description.
// The group is named by GroupId (GroupName, EC2-Classic and the default
// VPC, is not served here); the peer by one of a CIDR (CidrIp, CidrIpv6),
// a prefix list (Source/DestinationPrefixListId) or another group
// (SourceSecurityGroupId, DestinationSecurityGroupId). moto keeps a
// rule's description on its CIDR only: a description on a rule to a group
// or a prefix list is not stored there, so it is not read back.
type SecurityGroupRule struct {
	EC2    *ec2.Client
	Egress bool
}

// NewSecurityGroupIngress makes the inbound rule's backend.
func NewSecurityGroupIngress(cfg aws.Config) *SecurityGroupRule {
	return &SecurityGroupRule{EC2: ec2.NewFromConfig(cfg)}
}

// NewSecurityGroupEgress makes the outbound rule's backend.
func NewSecurityGroupEgress(cfg aws.Config) *SecurityGroupRule {
	return &SecurityGroupRule{EC2: ec2.NewFromConfig(cfg), Egress: true}
}

// peer gives the names of the properties that name the rule's peer group
// and prefix list, which differ between the two types.
func (b *SecurityGroupRule) peer() (group, prefixList string) {
	if b.Egress {
		return "DestinationSecurityGroupId", "DestinationPrefixListId"
	}
	return "SourceSecurityGroupId", "SourcePrefixListId"
}

func port(m map[string]any, k string) *int32 {
	switch v := m[k].(type) {
	case float64:
		return aws.Int32(int32(v))
	case int:
		return aws.Int32(int32(v))
	case int64:
		return aws.Int32(int32(v))
	}
	return nil
}

// permission is the model as the one IpPermission the EC2 API takes.
func (b *SecurityGroupRule) permission(m map[string]any) (ec2types.IpPermission, error) {
	groupProp, prefixProp := b.peer()
	p := ec2types.IpPermission{IpProtocol: aws.String(str(m, "IpProtocol")), FromPort: port(m, "FromPort"), ToPort: port(m, "ToPort")}
	var desc *string
	if d := str(m, "Description"); d != "" {
		desc = aws.String(d)
	}
	peers := 0
	if v := str(m, "CidrIp"); v != "" {
		p.IpRanges = []ec2types.IpRange{{CidrIp: aws.String(v), Description: desc}}
		peers++
	}
	if v := str(m, "CidrIpv6"); v != "" {
		p.Ipv6Ranges = []ec2types.Ipv6Range{{CidrIpv6: aws.String(v), Description: desc}}
		peers++
	}
	if v := str(m, prefixProp); v != "" {
		p.PrefixListIds = []ec2types.PrefixListId{{PrefixListId: aws.String(v), Description: desc}}
		peers++
	}
	if v := str(m, groupProp); v != "" {
		pair := ec2types.UserIdGroupPair{GroupId: aws.String(v), Description: desc}
		if o := str(m, "SourceSecurityGroupOwnerId"); o != "" && !b.Egress {
			pair.UserId = aws.String(o)
		}
		p.UserIdGroupPairs = []ec2types.UserIdGroupPair{pair}
		peers++
	}
	if peers != 1 {
		return p, InvalidRequest("exactly one of CidrIp, CidrIpv6, %s or %s is required", prefixProp, groupProp)
	}
	return p, nil
}

func (b *SecurityGroupRule) Create(ctx context.Context, m map[string]any) (string, error) {
	group := str(m, "GroupId")
	if group == "" {
		return "", InvalidRequest("GroupId is required (GroupName is not served by this fake)")
	}
	p, err := b.permission(m)
	if err != nil {
		return "", err
	}
	var rules []ec2types.SecurityGroupRule
	if b.Egress {
		out, err := b.EC2.AuthorizeSecurityGroupEgress(ctx, &ec2.AuthorizeSecurityGroupEgressInput{GroupId: aws.String(group), IpPermissions: []ec2types.IpPermission{p}})
		if err != nil {
			return "", InvalidRequest("%v", err)
		}
		rules = out.SecurityGroupRules
	} else {
		out, err := b.EC2.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{GroupId: aws.String(group), IpPermissions: []ec2types.IpPermission{p}})
		if err != nil {
			return "", InvalidRequest("%v", err)
		}
		rules = out.SecurityGroupRules
	}
	if len(rules) != 1 || aws.ToString(rules[0].SecurityGroupRuleId) == "" {
		return "", InvalidRequest("the authorize call gave %d rules, want one with an ID", len(rules))
	}
	return aws.ToString(rules[0].SecurityGroupRuleId), nil
}

func (b *SecurityGroupRule) rule(ctx context.Context, id string) (ec2types.SecurityGroupRule, error) {
	out, err := b.EC2.DescribeSecurityGroupRules(ctx, &ec2.DescribeSecurityGroupRulesInput{SecurityGroupRuleIds: []string{id}})
	if err != nil {
		if strings.Contains(code(err), "NotFound") {
			return ec2types.SecurityGroupRule{}, NotFound("%s", id)
		}
		return ec2types.SecurityGroupRule{}, err
	}
	for _, r := range out.SecurityGroupRules {
		if aws.ToString(r.SecurityGroupRuleId) == id && aws.ToBool(r.IsEgress) == b.Egress {
			return r, nil
		}
	}
	return ec2types.SecurityGroupRule{}, NotFound("%s", id)
}

func (b *SecurityGroupRule) Read(ctx context.Context, id string) (map[string]any, error) {
	r, err := b.rule(ctx, id)
	if err != nil {
		return nil, err
	}
	groupProp, prefixProp := b.peer()
	m := map[string]any{"Id": id, "GroupId": aws.ToString(r.GroupId), "IpProtocol": aws.ToString(r.IpProtocol)}
	// A rule for every protocol (-1) has no ports; DescribeSecurityGroupRules
	// gives -1 for them, which the model does not hold.
	if aws.ToString(r.IpProtocol) != "-1" {
		if r.FromPort != nil {
			m["FromPort"] = float64(*r.FromPort)
		}
		if r.ToPort != nil {
			m["ToPort"] = float64(*r.ToPort)
		}
	}
	for k, v := range map[string]*string{"CidrIp": r.CidrIpv4, "CidrIpv6": r.CidrIpv6, prefixProp: r.PrefixListId, "Description": r.Description} {
		if s := aws.ToString(v); s != "" {
			m[k] = s
		}
	}
	if g := r.ReferencedGroupInfo; g != nil && aws.ToString(g.GroupId) != "" {
		m[groupProp] = aws.ToString(g.GroupId)
	}
	return m, nil
}

func (b *SecurityGroupRule) Update(ctx context.Context, id string, prev, next map[string]any) error {
	if str(prev, "Description") == str(next, "Description") {
		return nil
	}
	if _, err := b.rule(ctx, id); err != nil {
		return err
	}
	group := str(next, "GroupId")
	p, err := b.permission(next)
	if err != nil {
		return err
	}
	// The rule named by its permission, the new description on it: moto
	// matches by permission only (AWS also takes rule IDs).
	if b.Egress {
		_, err = b.EC2.UpdateSecurityGroupRuleDescriptionsEgress(ctx, &ec2.UpdateSecurityGroupRuleDescriptionsEgressInput{
			GroupId: aws.String(group), IpPermissions: []ec2types.IpPermission{p},
		})
	} else {
		_, err = b.EC2.UpdateSecurityGroupRuleDescriptionsIngress(ctx, &ec2.UpdateSecurityGroupRuleDescriptionsIngressInput{
			GroupId: aws.String(group), IpPermissions: []ec2types.IpPermission{p},
		})
	}
	if err != nil {
		return InvalidRequest("%v", err)
	}
	return nil
}

func (b *SecurityGroupRule) Delete(ctx context.Context, id string) error {
	r, err := b.rule(ctx, id)
	if err != nil {
		return err
	}
	if b.Egress {
		_, err = b.EC2.RevokeSecurityGroupEgress(ctx, &ec2.RevokeSecurityGroupEgressInput{GroupId: r.GroupId, SecurityGroupRuleIds: []string{id}})
	} else {
		_, err = b.EC2.RevokeSecurityGroupIngress(ctx, &ec2.RevokeSecurityGroupIngressInput{GroupId: r.GroupId, SecurityGroupRuleIds: []string{id}})
	}
	return err
}
