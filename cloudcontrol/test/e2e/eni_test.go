// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// F06, EC2: a network interface in a subnet with a description, a security
// group and a tag; its description, groups and source/destination check
// changed; drift made in EC2 put back; the subnet create-only; deleted.
func TestNetworkInterface(t *testing.T) {
	e := start(t)
	c := ec2.NewFromConfig(e.moto)
	const typ = "AWS::EC2::NetworkInterface"
	vpc, err := c.CreateVpc(e.ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.40.0.0/16")})
	if err != nil {
		t.Fatal(err)
	}
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	sub, err := c.CreateSubnet(e.ctx, &ec2.CreateSubnetInput{VpcId: &vpcID, CidrBlock: aws.String("10.40.1.0/24")})
	if err != nil {
		t.Fatal(err)
	}
	subnet := aws.ToString(sub.Subnet.SubnetId)
	sg := func(name string) string {
		out, err := c.CreateSecurityGroup(e.ctx, &ec2.CreateSecurityGroupInput{GroupName: aws.String(name), Description: aws.String(name), VpcId: &vpcID})
		if err != nil {
			t.Fatal(err)
		}
		return aws.ToString(out.GroupId)
	}
	sg1, sg2 := sg("appliance"), sg("appliance-v2")

	u := objOf(typ, "appliance-eni", map[string]any{
		"subnetId":    subnet,
		"description": "appliance interface",
		"groupSet":    []any{sg1},
		"tags":        []any{map[string]any{"key": "team", "value": "network"}},
	})
	if err := e.c.Create(e.ctx, u); err != nil {
		t.Fatal(err)
	}
	got := e.waitSynced(t, u)
	id, _, _ := unstructured.NestedString(got.Object, "status", "id")
	if v, _, _ := unstructured.NestedString(got.Object, "status", "vpcId"); v != vpcID {
		t.Errorf("status.vpcId %q, want %q", v, vpcID)
	}
	eni := func() (ec2types.NetworkInterface, error) {
		out, err := c.DescribeNetworkInterfaces(e.ctx, &ec2.DescribeNetworkInterfacesInput{NetworkInterfaceIds: []string{id}})
		if err != nil {
			return ec2types.NetworkInterface{}, err
		}
		if len(out.NetworkInterfaces) != 1 {
			return ec2types.NetworkInterface{}, fmt.Errorf("%d interfaces", len(out.NetworkInterfaces))
		}
		return out.NetworkInterfaces[0], nil
	}
	describe := func(n ec2types.NetworkInterface) string {
		var gs []string
		for _, g := range n.Groups {
			gs = append(gs, aws.ToString(g.GroupId))
		}
		tags := ""
		for _, tg := range n.TagSet {
			if aws.ToString(tg.Key) == "team" {
				tags = aws.ToString(tg.Value)
			}
		}
		return fmt.Sprintf("%s|%s|%v|%s", aws.ToString(n.Description), strings.Join(gs, ","), aws.ToBool(n.SourceDestCheck), tags)
	}
	n, err := eni()
	if err != nil || describe(n) != fmt.Sprintf("appliance interface|%s|true|network", sg1) || aws.ToString(n.SubnetId) != subnet {
		t.Fatalf("moto's interface: %v %s", err, describe(n))
	}

	// Resyncs of the unchanged interface make no update.
	time.Sleep(12 * time.Second)
	if k := len(e.calls("UpdateResource", typ)); k != 0 {
		t.Errorf("%d update(s) of an unchanged interface: %v", k, patchPaths(t, e.calls("UpdateResource", typ)))
	}

	if err := e.update(t, u, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, "appliance interface v2", "spec", "description")
		_ = unstructured.SetNestedSlice(o.Object, []any{sg2}, "spec", "groupSet")
		_ = unstructured.SetNestedField(o.Object, false, "spec", "sourceDestCheck")
	}); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("appliance interface v2|%s|false|network", sg2)
	eventually(t, 60*time.Second, "the changes in moto", func() error {
		n, err := eni()
		if err != nil || describe(n) != want {
			return fmt.Errorf("%v %s", err, describe(n))
		}
		return nil
	})
	e.waitSynced(t, u)

	// Drift: the description changed in EC2; the resync puts it back.
	if _, err := c.ModifyNetworkInterfaceAttribute(e.ctx, &ec2.ModifyNetworkInterfaceAttributeInput{
		NetworkInterfaceId: &id, Description: &ec2types.AttributeValue{Value: aws.String("changed by hand")},
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "drift put back in moto", func() error {
		n, err := eni()
		if err != nil || describe(n) != want {
			return fmt.Errorf("%v %s", err, describe(n))
		}
		return nil
	})

	// The subnet is create-only.
	err = e.update(t, u, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, "subnet-0000000000000000", "spec", "subnetId")
	})
	if err == nil || !strings.Contains(err.Error(), "create-only") {
		t.Errorf("a new subnetId was not refused as create-only: %v", err)
	}

	if err := e.c.Delete(e.ctx, u); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, u)
	if _, err := eni(); err == nil {
		t.Errorf("interface %s still in moto after delete", id)
	}
}
