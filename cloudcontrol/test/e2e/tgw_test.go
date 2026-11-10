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

// tgwFixture is what the routing kinds need: two transit gateways, a VPC
// attached to the first, and a route table on it. Made with the EC2 SDK,
// deleted at the end in reverse order (on a live run the gateways and the
// attachment are billed hourly).
type tgwFixture struct {
	hub, peer, vpc, subnet, attachment, routeTable string
}

func makeTGWFixture(t *testing.T, e *env) tgwFixture {
	t.Helper()
	ec := ec2.NewFromConfig(e.moto)
	var f tgwFixture
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	hub, err := ec.CreateTransitGateway(e.ctx, &ec2.CreateTransitGatewayInput{Description: aws.String("hub")})
	must(err)
	f.hub = aws.ToString(hub.TransitGateway.TransitGatewayId)
	t.Cleanup(func() {
		_, _ = ec.DeleteTransitGateway(e.ctx, &ec2.DeleteTransitGatewayInput{TransitGatewayId: &f.hub})
	})
	peer, err := ec.CreateTransitGateway(e.ctx, &ec2.CreateTransitGatewayInput{Description: aws.String("peer")})
	must(err)
	f.peer = aws.ToString(peer.TransitGateway.TransitGatewayId)
	t.Cleanup(func() {
		_, _ = ec.DeleteTransitGateway(e.ctx, &ec2.DeleteTransitGatewayInput{TransitGatewayId: &f.peer})
	})
	vpc, err := ec.CreateVpc(e.ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.0.0.0/16")})
	must(err)
	f.vpc = aws.ToString(vpc.Vpc.VpcId)
	t.Cleanup(func() { _, _ = ec.DeleteVpc(e.ctx, &ec2.DeleteVpcInput{VpcId: &f.vpc}) })
	sn, err := ec.CreateSubnet(e.ctx, &ec2.CreateSubnetInput{VpcId: &f.vpc, CidrBlock: aws.String("10.0.1.0/24")})
	must(err)
	f.subnet = aws.ToString(sn.Subnet.SubnetId)
	t.Cleanup(func() { _, _ = ec.DeleteSubnet(e.ctx, &ec2.DeleteSubnetInput{SubnetId: &f.subnet}) })
	att, err := ec.CreateTransitGatewayVpcAttachment(e.ctx, &ec2.CreateTransitGatewayVpcAttachmentInput{
		TransitGatewayId: &f.hub, VpcId: &f.vpc, SubnetIds: []string{f.subnet},
	})
	must(err)
	f.attachment = aws.ToString(att.TransitGatewayVpcAttachment.TransitGatewayAttachmentId)
	t.Cleanup(func() {
		_, _ = ec.DeleteTransitGatewayVpcAttachment(e.ctx, &ec2.DeleteTransitGatewayVpcAttachmentInput{TransitGatewayAttachmentId: &f.attachment})
	})
	rt, err := ec.CreateTransitGatewayRouteTable(e.ctx, &ec2.CreateTransitGatewayRouteTableInput{TransitGatewayId: &f.hub})
	must(err)
	f.routeTable = aws.ToString(rt.TransitGatewayRouteTable.TransitGatewayRouteTableId)
	t.Cleanup(func() {
		_, _ = ec.DeleteTransitGatewayRouteTable(e.ctx, &ec2.DeleteTransitGatewayRouteTableInput{TransitGatewayRouteTableId: &f.routeTable})
	})
	return f
}

func staticRoutes(t *testing.T, e *env, rt string) map[string]string {
	t.Helper()
	out, err := ec2.NewFromConfig(e.moto).SearchTransitGatewayRoutes(e.ctx, &ec2.SearchTransitGatewayRoutesInput{
		TransitGatewayRouteTableId: &rt,
		Filters:                    []ec2types.Filter{{Name: aws.String("type"), Values: []string{"static"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, r := range out.Routes {
		m[aws.ToString(r.DestinationCidrBlock)] = string(r.State)
	}
	return m
}

// F04, Transit Gateway routing: an attachment associated with a route
// table and propagating to it, a static route to the attachment and a
// blackhole route, a peering request to a second gateway with a tag
// change; create-only changes refused; resyncs make no update; each
// delete removes it from EC2.
func TestTransitGatewayRouting(t *testing.T) {
	e := start(t)
	ec := ec2.NewFromConfig(e.moto)
	f := makeTGWFixture(t, e)

	link := map[string]any{"transitGatewayRouteTableId": f.routeTable, "transitGatewayAttachmentId": f.attachment}
	assoc := obj("TransitGatewayRouteTableAssociation", "vpc-to-spokes", link)
	prop := obj("TransitGatewayRouteTablePropagation", "vpc-into-spokes", link)
	route := obj("TransitGatewayRoute", "to-vpc", map[string]any{
		"transitGatewayRouteTableId": f.routeTable, "destinationCidrBlock": "10.1.0.0/16",
		"transitGatewayAttachmentId": f.attachment,
	})
	hole := obj("TransitGatewayRoute", "drop-legacy", map[string]any{
		"transitGatewayRouteTableId": f.routeTable, "destinationCidrBlock": "10.9.0.0/16", "blackhole": true,
	})
	for _, u := range []*unstructured.Unstructured{assoc, prop, route, hole} {
		if err := e.c.Create(e.ctx, u); err != nil {
			t.Fatalf("create %s: %v", u.GetName(), err)
		}
	}
	for _, c := range []struct {
		u  *unstructured.Unstructured
		id string
	}{
		{assoc, f.routeTable + "|" + f.attachment},
		{prop, f.routeTable + "|" + f.attachment},
		{route, f.routeTable + "|10.1.0.0/16"},
		{hole, f.routeTable + "|10.9.0.0/16"},
	} {
		got := e.waitSynced(t, c.u)
		if id, _, _ := unstructured.NestedString(got.Object, "status", "cloudControl", "identifier"); id != c.id {
			t.Errorf("%s: identifier %q, want %q", c.u.GetName(), id, c.id)
		}
	}

	as, err := ec.GetTransitGatewayRouteTableAssociations(e.ctx, &ec2.GetTransitGatewayRouteTableAssociationsInput{TransitGatewayRouteTableId: &f.routeTable})
	if err != nil || len(as.Associations) != 1 || aws.ToString(as.Associations[0].TransitGatewayAttachmentId) != f.attachment {
		t.Errorf("associations in EC2: %v %+v", err, as)
	}
	ps, err := ec.GetTransitGatewayRouteTablePropagations(e.ctx, &ec2.GetTransitGatewayRouteTablePropagationsInput{TransitGatewayRouteTableId: &f.routeTable})
	if err != nil || len(ps.TransitGatewayRouteTablePropagations) != 1 || ps.TransitGatewayRouteTablePropagations[0].State != ec2types.TransitGatewayPropagationStateEnabled {
		t.Errorf("propagations in EC2: %v %+v", err, ps)
	}
	if r := staticRoutes(t, e, f.routeTable); r["10.1.0.0/16"] != "active" || r["10.9.0.0/16"] != "blackhole" {
		t.Errorf("static routes in EC2: %v", r)
	}

	// The peering request: the requester's side, pending the peer's
	// acceptance, which is outside the kind.
	const peerType = "AWS::EC2::TransitGatewayPeeringAttachment"
	peering := obj("TransitGatewayPeeringAttachment", "hub-to-peer", map[string]any{
		"transitGatewayId": f.hub, "peerTransitGatewayId": f.peer,
		"peerAccountId": e.account, "peerRegion": region,
		"tags": []any{map[string]any{"key": "link", "value": "hub-peer"}},
	})
	if err := e.c.Create(e.ctx, peering); err != nil {
		t.Fatal(err)
	}
	got := e.waitSynced(t, peering)
	pid, _, _ := unstructured.NestedString(got.Object, "status", "transitGatewayAttachmentId")
	state, _, _ := unstructured.NestedString(got.Object, "status", "state")
	if !strings.HasPrefix(pid, "tgw-attach-") || state != "pendingAcceptance" {
		t.Errorf("status.transitGatewayAttachmentId %q, status.state %q (want pendingAcceptance)", pid, state)
	}
	peerTags := func() map[string]string {
		out, err := ec.DescribeTransitGatewayPeeringAttachments(e.ctx, &ec2.DescribeTransitGatewayPeeringAttachmentsInput{TransitGatewayAttachmentIds: []string{pid}})
		if err != nil || len(out.TransitGatewayPeeringAttachments) != 1 {
			t.Fatalf("describe %s: %v", pid, err)
		}
		m := map[string]string{}
		for _, tg := range out.TransitGatewayPeeringAttachments[0].Tags {
			m[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
		}
		return m
	}
	if tags := peerTags(); tags["link"] != "hub-peer" || tags["services.k8s.aws/namespace"] != envNS {
		t.Errorf("peering tags in EC2: %v", tags)
	}

	// Resyncs of the unchanged five make no update.
	time.Sleep(12 * time.Second)
	if n := len(e.calls("UpdateResource", "")); n != 0 {
		t.Errorf("%d update(s) of unchanged objects: %v", n, patchPaths(t, e.calls("UpdateResource", "")))
	}

	// A tag change on the peering: one replace of /Tags.
	if err := e.update(t, peering, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedSlice(o.Object, []any{map[string]any{"key": "link", "value": "hub-peer-2"}}, "spec", "tags")
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "the peering's tag changed in EC2", func() error {
		if v := peerTags()["link"]; v != "hub-peer-2" {
			return fmt.Errorf("link=%q", v)
		}
		return nil
	})
	if p := patchPaths(t, e.calls("UpdateResource", peerType)); strings.Join(p, ",") != "replace /Tags" {
		t.Errorf("patches %v, want one replace of /Tags", p)
	}
	e.waitSynced(t, peering)

	// Every property of the three links is create-only, and the peer is.
	for _, c := range []struct {
		u     *unstructured.Unstructured
		field string
		value any
	}{
		{assoc, "transitGatewayAttachmentId", "tgw-attach-0123456789abcdef0"},
		{prop, "transitGatewayRouteTableId", "tgw-rtb-0123456789abcdef0"},
		{route, "destinationCidrBlock", "10.2.0.0/16"},
		{hole, "blackhole", false},
		{peering, "peerTransitGatewayId", "tgw-0123456789abcdef0"},
	} {
		err := e.update(t, c.u, func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, c.value, "spec", c.field)
		})
		if err == nil || !strings.Contains(err.Error(), "create-only") {
			t.Errorf("%s: a new %s was not refused as create-only: %v", c.u.GetName(), c.field, err)
		}
	}

	for _, u := range []*unstructured.Unstructured{peering, route, hole, prop, assoc} {
		if err := e.c.Delete(e.ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []*unstructured.Unstructured{peering, route, hole, prop, assoc} {
		e.waitGone(t, u)
	}
	if r := staticRoutes(t, e, f.routeTable); len(r) != 0 {
		t.Errorf("static routes left in EC2: %v", r)
	}
	as, err = ec.GetTransitGatewayRouteTableAssociations(e.ctx, &ec2.GetTransitGatewayRouteTableAssociationsInput{TransitGatewayRouteTableId: &f.routeTable})
	if err != nil || len(as.Associations) != 0 {
		t.Errorf("associations left in EC2: %v %+v", err, as)
	}
	ps, err = ec.GetTransitGatewayRouteTablePropagations(e.ctx, &ec2.GetTransitGatewayRouteTablePropagationsInput{TransitGatewayRouteTableId: &f.routeTable})
	if err != nil || len(ps.TransitGatewayRouteTablePropagations) != 0 {
		t.Errorf("propagations left in EC2: %v %+v", err, ps)
	}
	out, err := ec.DescribeTransitGatewayPeeringAttachments(e.ctx, &ec2.DescribeTransitGatewayPeeringAttachmentsInput{TransitGatewayAttachmentIds: []string{pid}})
	if err == nil && len(out.TransitGatewayPeeringAttachments) == 1 {
		if s := out.TransitGatewayPeeringAttachments[0].State; s != ec2types.TransitGatewayAttachmentStateDeleted && s != ec2types.TransitGatewayAttachmentStateDeleting {
			t.Errorf("the peering attachment is %s after delete", s)
		}
	}
}
