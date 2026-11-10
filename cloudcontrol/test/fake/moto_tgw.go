// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// F04, Transit Gateway routing, on moto's EC2. moto 5.2.5 implements every
// call these handlers make, with four differences from AWS that the
// backends make up for (probed 2026-11-09):
//   - it associates an attachment with a route table twice, and makes a
//     static route twice, where AWS refuses the second
//     (Resource.AlreadyAssociated, RouteAlreadyExists): the backends look
//     first and answer AlreadyExists;
//   - a static route to an attachment comes back from
//     SearchTransitGatewayRoutes with no attachments, so the route backend
//     keeps the attachment ID it was given;
//   - DeleteTransitGatewayRoute of a route that is gone is an internal
//     error, so the backends read before they delete;
//   - a deleted peering attachment stays, in state "deleted", as AWS shows
//     one for a while; the read treats it as gone.

// ---- AWS::EC2::TransitGatewayRoute ----

// TransitGatewayRoute is a static route in a Transit Gateway route table,
// to an attachment or a blackhole. Every property is create-only; the
// identifier is TransitGatewayRouteTableId|DestinationCidrBlock.
type TransitGatewayRoute struct {
	EC2 *ec2.Client
	mu  sync.Mutex
	att map[string]string // identifier -> TransitGatewayAttachmentId (moto drops it)
}

// NewTransitGatewayRoute makes the backend.
func NewTransitGatewayRoute(cfg aws.Config) *TransitGatewayRoute {
	return &TransitGatewayRoute{EC2: ec2.NewFromConfig(cfg), att: map[string]string{}}
}

func (b *TransitGatewayRoute) search(ctx context.Context, rt, cidr string) (*ec2types.TransitGatewayRoute, error) {
	out, err := b.EC2.SearchTransitGatewayRoutes(ctx, &ec2.SearchTransitGatewayRoutesInput{
		TransitGatewayRouteTableId: aws.String(rt),
		Filters: []ec2types.Filter{
			{Name: aws.String("type"), Values: []string{"static"}},
			{Name: aws.String("route-search.exact-match"), Values: []string{cidr}},
		},
	})
	if err != nil {
		if strings.Contains(code(err), "NotFound") {
			return nil, NotFound("%s|%s", rt, cidr)
		}
		return nil, err
	}
	for i := range out.Routes {
		r := &out.Routes[i]
		if aws.ToString(r.DestinationCidrBlock) == cidr && r.State != ec2types.TransitGatewayRouteStateDeleted {
			return r, nil
		}
	}
	return nil, NotFound("%s|%s", rt, cidr)
}

func (b *TransitGatewayRoute) Create(ctx context.Context, m map[string]any) (string, error) {
	rt, cidr := str(m, "TransitGatewayRouteTableId"), str(m, "DestinationCidrBlock")
	id := rt + "|" + cidr
	if _, err := b.search(ctx, rt, cidr); err == nil {
		return "", &HandlerError{"AlreadyExists", "RouteAlreadyExists: a route to " + cidr + " is already in " + rt}
	}
	in := &ec2.CreateTransitGatewayRouteInput{TransitGatewayRouteTableId: aws.String(rt), DestinationCidrBlock: aws.String(cidr)}
	if a := str(m, "TransitGatewayAttachmentId"); a != "" {
		in.TransitGatewayAttachmentId = aws.String(a)
	}
	if bh, ok := m["Blackhole"].(bool); ok {
		in.Blackhole = aws.Bool(bh)
	}
	if _, err := b.EC2.CreateTransitGatewayRoute(ctx, in); err != nil {
		return "", InvalidRequest("%v", err)
	}
	b.mu.Lock()
	b.att[id] = str(m, "TransitGatewayAttachmentId")
	b.mu.Unlock()
	return id, nil
}

func (b *TransitGatewayRoute) Read(ctx context.Context, id string) (map[string]any, error) {
	p, err := splitID(id, 2)
	if err != nil {
		return nil, err
	}
	r, err := b.search(ctx, p[0], p[1])
	if err != nil {
		return nil, err
	}
	m := map[string]any{"TransitGatewayRouteTableId": p[0], "DestinationCidrBlock": p[1]}
	if r.State == ec2types.TransitGatewayRouteStateBlackhole {
		m["Blackhole"] = true
	}
	if len(r.TransitGatewayAttachments) > 0 {
		m["TransitGatewayAttachmentId"] = aws.ToString(r.TransitGatewayAttachments[0].TransitGatewayAttachmentId)
	} else {
		b.mu.Lock()
		if a := b.att[id]; a != "" {
			m["TransitGatewayAttachmentId"] = a
		}
		b.mu.Unlock()
	}
	return m, nil
}

func (b *TransitGatewayRoute) Update(context.Context, string, map[string]any, map[string]any) error {
	return &HandlerError{"NotUpdatable", "AWS::EC2::TransitGatewayRoute has no update handler"}
}

func (b *TransitGatewayRoute) Delete(ctx context.Context, id string) error {
	if _, err := b.Read(ctx, id); err != nil {
		return err
	}
	p, _ := splitID(id, 2)
	if _, err := b.EC2.DeleteTransitGatewayRoute(ctx, &ec2.DeleteTransitGatewayRouteInput{
		TransitGatewayRouteTableId: aws.String(p[0]), DestinationCidrBlock: aws.String(p[1]),
	}); err != nil {
		return err
	}
	b.mu.Lock()
	delete(b.att, id)
	b.mu.Unlock()
	return nil
}

// ---- AWS::EC2::TransitGatewayRouteTableAssociation and ...Propagation ----

// tgwLink is an attachment linked to a route table: associated (the table
// the attachment's traffic is routed by) or propagated (the attachment's
// routes put in the table). Both properties are create-only; the
// identifier is TransitGatewayRouteTableId|TransitGatewayAttachmentId.
type tgwLink struct {
	EC2       *ec2.Client
	typeName  string
	propagate bool
}

// NewTransitGatewayRouteTableAssociation makes the association backend.
func NewTransitGatewayRouteTableAssociation(cfg aws.Config) Backend {
	return &tgwLink{EC2: ec2.NewFromConfig(cfg), typeName: "AWS::EC2::TransitGatewayRouteTableAssociation"}
}

// NewTransitGatewayRouteTablePropagation makes the propagation backend.
func NewTransitGatewayRouteTablePropagation(cfg aws.Config) Backend {
	return &tgwLink{EC2: ec2.NewFromConfig(cfg), typeName: "AWS::EC2::TransitGatewayRouteTablePropagation", propagate: true}
}

// linked is whether the attachment is associated with (or propagates to)
// the route table.
func (b *tgwLink) linked(ctx context.Context, rt, att string) (bool, error) {
	var states []string
	if b.propagate {
		out, err := b.EC2.GetTransitGatewayRouteTablePropagations(ctx, &ec2.GetTransitGatewayRouteTablePropagationsInput{TransitGatewayRouteTableId: aws.String(rt)})
		if err != nil {
			return false, err
		}
		for _, p := range out.TransitGatewayRouteTablePropagations {
			if aws.ToString(p.TransitGatewayAttachmentId) == att {
				states = append(states, string(p.State))
			}
		}
	} else {
		out, err := b.EC2.GetTransitGatewayRouteTableAssociations(ctx, &ec2.GetTransitGatewayRouteTableAssociationsInput{TransitGatewayRouteTableId: aws.String(rt)})
		if err != nil {
			return false, err
		}
		for _, a := range out.Associations {
			if aws.ToString(a.TransitGatewayAttachmentId) == att {
				states = append(states, string(a.State))
			}
		}
	}
	for _, s := range states {
		if s == "associated" || s == "associating" || s == "enabled" || s == "enabling" {
			return true, nil
		}
	}
	return false, nil
}

func (b *tgwLink) Create(ctx context.Context, m map[string]any) (string, error) {
	rt, att := str(m, "TransitGatewayRouteTableId"), str(m, "TransitGatewayAttachmentId")
	ok, err := b.linked(ctx, rt, att)
	if err != nil {
		return "", InvalidRequest("%v", err)
	}
	if ok {
		return "", &HandlerError{"AlreadyExists", att + " is already linked to " + rt}
	}
	if b.propagate {
		_, err = b.EC2.EnableTransitGatewayRouteTablePropagation(ctx, &ec2.EnableTransitGatewayRouteTablePropagationInput{TransitGatewayRouteTableId: aws.String(rt), TransitGatewayAttachmentId: aws.String(att)})
	} else {
		_, err = b.EC2.AssociateTransitGatewayRouteTable(ctx, &ec2.AssociateTransitGatewayRouteTableInput{TransitGatewayRouteTableId: aws.String(rt), TransitGatewayAttachmentId: aws.String(att)})
	}
	if err != nil {
		return "", InvalidRequest("%v", err)
	}
	return rt + "|" + att, nil
}

func (b *tgwLink) Read(ctx context.Context, id string) (map[string]any, error) {
	p, err := splitID(id, 2)
	if err != nil {
		return nil, err
	}
	ok, err := b.linked(ctx, p[0], p[1])
	if err != nil {
		if strings.Contains(code(err), "NotFound") {
			return nil, NotFound("%s", id)
		}
		return nil, err
	}
	if !ok {
		return nil, NotFound("%s", id)
	}
	return map[string]any{"TransitGatewayRouteTableId": p[0], "TransitGatewayAttachmentId": p[1]}, nil
}

func (b *tgwLink) Update(context.Context, string, map[string]any, map[string]any) error {
	return &HandlerError{"NotUpdatable", b.typeName + " has no update handler"}
}

func (b *tgwLink) Delete(ctx context.Context, id string) error {
	if _, err := b.Read(ctx, id); err != nil {
		return err
	}
	p, _ := splitID(id, 2)
	var err error
	if b.propagate {
		_, err = b.EC2.DisableTransitGatewayRouteTablePropagation(ctx, &ec2.DisableTransitGatewayRouteTablePropagationInput{TransitGatewayRouteTableId: aws.String(p[0]), TransitGatewayAttachmentId: aws.String(p[1])})
	} else {
		_, err = b.EC2.DisassociateTransitGatewayRouteTable(ctx, &ec2.DisassociateTransitGatewayRouteTableInput{TransitGatewayRouteTableId: aws.String(p[0]), TransitGatewayAttachmentId: aws.String(p[1])})
	}
	return err
}

// ---- AWS::EC2::TransitGatewayPeeringAttachment ----

// TransitGatewayPeeringAttachment is the requester's side of a peering
// between two transit gateways. The handler makes the request and stops:
// the peer accepts it (AcceptTransitGatewayPeeringAttachment, which no
// CloudFormation type has), so a new attachment is pendingAcceptance. Only
// tags can change. The identifier is the attachment's ID.
type TransitGatewayPeeringAttachment struct{ EC2 *ec2.Client }

// NewTransitGatewayPeeringAttachment makes the backend.
func NewTransitGatewayPeeringAttachment(cfg aws.Config) *TransitGatewayPeeringAttachment {
	return &TransitGatewayPeeringAttachment{EC2: ec2.NewFromConfig(cfg)}
}

func (b *TransitGatewayPeeringAttachment) Create(ctx context.Context, m map[string]any) (string, error) {
	in := &ec2.CreateTransitGatewayPeeringAttachmentInput{
		TransitGatewayId:     aws.String(str(m, "TransitGatewayId")),
		PeerTransitGatewayId: aws.String(str(m, "PeerTransitGatewayId")),
		PeerAccountId:        aws.String(str(m, "PeerAccountId")),
		PeerRegion:           aws.String(str(m, "PeerRegion")),
	}
	if tags := tagsOf(m["Tags"]); len(tags) > 0 {
		in.TagSpecifications = []ec2types.TagSpecification{{ResourceType: ec2types.ResourceTypeTransitGatewayAttachment, Tags: ec2Tags(tags)}}
	}
	out, err := b.EC2.CreateTransitGatewayPeeringAttachment(ctx, in)
	if err != nil {
		return "", InvalidRequest("%v", err)
	}
	return aws.ToString(out.TransitGatewayPeeringAttachment.TransitGatewayAttachmentId), nil
}

func (b *TransitGatewayPeeringAttachment) Read(ctx context.Context, id string) (map[string]any, error) {
	out, err := b.EC2.DescribeTransitGatewayPeeringAttachments(ctx, &ec2.DescribeTransitGatewayPeeringAttachmentsInput{TransitGatewayAttachmentIds: []string{id}})
	if err != nil {
		if strings.Contains(code(err), "NotFound") {
			return nil, NotFound("%s", id)
		}
		return nil, err
	}
	for _, a := range out.TransitGatewayPeeringAttachments {
		if aws.ToString(a.TransitGatewayAttachmentId) != id {
			continue
		}
		switch a.State {
		case ec2types.TransitGatewayAttachmentStateDeleted, ec2types.TransitGatewayAttachmentStateDeleting,
			ec2types.TransitGatewayAttachmentStateFailed, ec2types.TransitGatewayAttachmentStateRejected:
			continue
		}
		m := map[string]any{"TransitGatewayAttachmentId": id, "State": string(a.State)}
		if r := a.RequesterTgwInfo; r != nil {
			m["TransitGatewayId"] = aws.ToString(r.TransitGatewayId)
		}
		if p := a.AccepterTgwInfo; p != nil {
			m["PeerTransitGatewayId"] = aws.ToString(p.TransitGatewayId)
			m["PeerAccountId"] = aws.ToString(p.OwnerId)
			m["PeerRegion"] = aws.ToString(p.Region)
		}
		if s := a.Status; s != nil {
			m["Status"] = map[string]any{"Code": aws.ToString(s.Code), "Message": aws.ToString(s.Message)}
		}
		if a.CreationTime != nil {
			m["CreationTime"] = a.CreationTime.UTC().Format(time.RFC3339)
		}
		tags := map[string]string{}
		for _, tg := range a.Tags {
			tags[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
		}
		if len(tags) > 0 {
			m["Tags"] = tagList(tags)
		}
		return m, nil
	}
	return nil, NotFound("%s", id)
}

func (b *TransitGatewayPeeringAttachment) Update(ctx context.Context, id string, prev, next map[string]any) error {
	return ec2SyncTags(ctx, b.EC2, id, prev, next)
}

func (b *TransitGatewayPeeringAttachment) Delete(ctx context.Context, id string) error {
	if _, err := b.Read(ctx, id); err != nil {
		return err
	}
	_, err := b.EC2.DeleteTransitGatewayPeeringAttachment(ctx, &ec2.DeleteTransitGatewayPeeringAttachmentInput{TransitGatewayAttachmentId: aws.String(id)})
	return err
}
