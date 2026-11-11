// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// ---- AWS::EC2::NetworkInterface on moto's EC2 ----

// NetworkInterface is the backend of an elastic network interface in a
// subnet, identified by its ID. moto 5.2.5 implements Create, Describe,
// ModifyNetworkInterfaceAttribute and DeleteNetworkInterface. This backend
// serves the subnet, the primary private address (create-only), the
// description, the security groups, the source/destination check and the
// tags; the read gives the VPC and the primary address as read-only
// values. Prefixes, IPv6, connection tracking and public DNS name options
// are not served here (unproven on the fake).
type NetworkInterface struct{ EC2 *ec2.Client }

// NewNetworkInterface makes the backend.
func NewNetworkInterface(cfg aws.Config) *NetworkInterface {
	return &NetworkInterface{EC2: ec2.NewFromConfig(cfg)}
}

func (b *NetworkInterface) Create(ctx context.Context, m map[string]any) (string, error) {
	in := &ec2.CreateNetworkInterfaceInput{SubnetId: aws.String(str(m, "SubnetId")), Groups: strList(m["GroupSet"])}
	if d := str(m, "Description"); d != "" {
		in.Description = aws.String(d)
	}
	if ip := str(m, "PrivateIpAddress"); ip != "" {
		in.PrivateIpAddress = aws.String(ip)
	}
	if tags := tagsOf(m["Tags"]); len(tags) > 0 {
		in.TagSpecifications = []ec2types.TagSpecification{{ResourceType: ec2types.ResourceTypeNetworkInterface, Tags: ec2Tags(tags)}}
	}
	out, err := b.EC2.CreateNetworkInterface(ctx, in)
	if err != nil {
		return "", InvalidRequest("%v", err)
	}
	id := aws.ToString(out.NetworkInterface.NetworkInterfaceId)
	if v, ok := m["SourceDestCheck"].(bool); ok && !v {
		if _, err := b.EC2.ModifyNetworkInterfaceAttribute(ctx, &ec2.ModifyNetworkInterfaceAttributeInput{
			NetworkInterfaceId: aws.String(id), SourceDestCheck: &ec2types.AttributeBooleanValue{Value: aws.Bool(false)},
		}); err != nil {
			return "", err
		}
	}
	return id, nil
}

func (b *NetworkInterface) Read(ctx context.Context, id string) (map[string]any, error) {
	out, err := b.EC2.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{NetworkInterfaceIds: []string{id}})
	if err != nil {
		if strings.Contains(code(err), "NotFound") {
			return nil, NotFound("%s", id)
		}
		return nil, err
	}
	for _, n := range out.NetworkInterfaces {
		if aws.ToString(n.NetworkInterfaceId) != id {
			continue
		}
		var groups []any
		var gs []string
		for _, g := range n.Groups {
			gs = append(gs, aws.ToString(g.GroupId))
		}
		sort.Strings(gs)
		for _, g := range gs {
			groups = append(groups, g)
		}
		m := map[string]any{
			"Id":                      id,
			"SubnetId":                aws.ToString(n.SubnetId),
			"VpcId":                   aws.ToString(n.VpcId),
			"PrimaryPrivateIpAddress": aws.ToString(n.PrivateIpAddress),
			"PrivateIpAddress":        aws.ToString(n.PrivateIpAddress),
			"SourceDestCheck":         aws.ToBool(n.SourceDestCheck),
		}
		if len(groups) > 0 {
			m["GroupSet"] = groups
		}
		if d := aws.ToString(n.Description); d != "" {
			m["Description"] = d
		}
		tags := map[string]string{}
		for _, tg := range n.TagSet {
			tags[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
		}
		if len(tags) > 0 {
			m["Tags"] = tagList(tags)
		}
		return m, nil
	}
	return nil, NotFound("%s", id)
}

func (b *NetworkInterface) Update(ctx context.Context, id string, prev, next map[string]any) error {
	if str(prev, "Description") != str(next, "Description") {
		if _, err := b.EC2.ModifyNetworkInterfaceAttribute(ctx, &ec2.ModifyNetworkInterfaceAttributeInput{
			NetworkInterfaceId: aws.String(id), Description: &ec2types.AttributeValue{Value: aws.String(str(next, "Description"))},
		}); err != nil {
			return InvalidRequest("%v", err)
		}
	}
	pg, ng := strList(prev["GroupSet"]), strList(next["GroupSet"])
	sort.Strings(pg)
	sort.Strings(ng)
	if strings.Join(pg, ",") != strings.Join(ng, ",") && len(ng) > 0 {
		if _, err := b.EC2.ModifyNetworkInterfaceAttribute(ctx, &ec2.ModifyNetworkInterfaceAttributeInput{
			NetworkInterfaceId: aws.String(id), Groups: ng,
		}); err != nil {
			return InvalidRequest("%v", err)
		}
	}
	if pv, nv := prev["SourceDestCheck"], next["SourceDestCheck"]; nv != nil && pv != nv {
		v, _ := nv.(bool)
		if _, err := b.EC2.ModifyNetworkInterfaceAttribute(ctx, &ec2.ModifyNetworkInterfaceAttributeInput{
			NetworkInterfaceId: aws.String(id), SourceDestCheck: &ec2types.AttributeBooleanValue{Value: aws.Bool(v)},
		}); err != nil {
			return InvalidRequest("%v", err)
		}
	}
	return ec2SyncTags(ctx, b.EC2, id, prev, next)
}

func (b *NetworkInterface) Delete(ctx context.Context, id string) error {
	if _, err := b.Read(ctx, id); err != nil {
		return err
	}
	_, err := b.EC2.DeleteNetworkInterface(ctx, &ec2.DeleteNetworkInterfaceInput{NetworkInterfaceId: aws.String(id)})
	return err
}
