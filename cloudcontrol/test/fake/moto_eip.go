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

// ---- AWS::EC2::EIPAssociation on moto's EC2 ----

// EIPAssociation is the backend of an Elastic IP's association with an
// instance or a network interface, identified by the association ID
// (eipassoc-...), as Cloud Control gives it. moto 5.2.5 implements
// AssociateAddress, DescribeAddresses and DisassociateAddress. Every
// property is create-only (the fake server refuses a change to one before
// it reaches here), so there is no update. The allocation (AllocationId)
// or the address itself (EIP) names the Elastic IP; InstanceId or
// NetworkInterfaceId (with PrivateIpAddress) names the target.
type EIPAssociation struct{ EC2 *ec2.Client }

// NewEIPAssociation makes the backend.
func NewEIPAssociation(cfg aws.Config) *EIPAssociation {
	return &EIPAssociation{EC2: ec2.NewFromConfig(cfg)}
}

func (b *EIPAssociation) Create(ctx context.Context, m map[string]any) (string, error) {
	in := &ec2.AssociateAddressInput{}
	if v := str(m, "AllocationId"); v != "" {
		in.AllocationId = aws.String(v)
	}
	if v := str(m, "EIP"); v != "" {
		in.PublicIp = aws.String(v)
	}
	if v := str(m, "InstanceId"); v != "" {
		in.InstanceId = aws.String(v)
	}
	if v := str(m, "NetworkInterfaceId"); v != "" {
		in.NetworkInterfaceId = aws.String(v)
	}
	if v := str(m, "PrivateIpAddress"); v != "" {
		in.PrivateIpAddress = aws.String(v)
	}
	if in.AllocationId == nil && in.PublicIp == nil {
		return "", InvalidRequest("one of AllocationId or EIP is required")
	}
	if in.InstanceId == nil && in.NetworkInterfaceId == nil {
		return "", InvalidRequest("one of InstanceId or NetworkInterfaceId is required")
	}
	out, err := b.EC2.AssociateAddress(ctx, in)
	if err != nil {
		return "", InvalidRequest("%v", err)
	}
	id := aws.ToString(out.AssociationId)
	if id == "" {
		return "", InvalidRequest("AssociateAddress gave no association ID")
	}
	return id, nil
}

func (b *EIPAssociation) Read(ctx context.Context, id string) (map[string]any, error) {
	out, err := b.EC2.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{
		Filters: []ec2types.Filter{{Name: aws.String("association-id"), Values: []string{id}}},
	})
	if err != nil {
		if strings.Contains(code(err), "NotFound") {
			return nil, NotFound("%s", id)
		}
		return nil, err
	}
	for _, a := range out.Addresses {
		if aws.ToString(a.AssociationId) != id {
			continue
		}
		m := map[string]any{"Id": id}
		for k, v := range map[string]*string{
			"AllocationId":       a.AllocationId,
			"EIP":                a.PublicIp,
			"InstanceId":         a.InstanceId,
			"NetworkInterfaceId": a.NetworkInterfaceId,
			"PrivateIpAddress":   a.PrivateIpAddress,
		} {
			if s := aws.ToString(v); s != "" {
				m[k] = s
			}
		}
		return m, nil
	}
	return nil, NotFound("%s", id)
}

func (b *EIPAssociation) Update(_ context.Context, id string, _, _ map[string]any) error {
	return InvalidRequest("%s: every property of AWS::EC2::EIPAssociation is create-only", id)
}

func (b *EIPAssociation) Delete(ctx context.Context, id string) error {
	if _, err := b.Read(ctx, id); err != nil {
		return err
	}
	_, err := b.EC2.DisassociateAddress(ctx, &ec2.DisassociateAddressInput{AssociationId: aws.String(id)})
	return err
}
