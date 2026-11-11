// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/identitystore"
	istypes "github.com/aws/aws-sdk-go-v2/service/identitystore/types"
)

func idPair(id string) (string, string, error) {
	p, err := splitID(id, 2)
	if err != nil {
		return "", "", err
	}
	return p[0], p[1], nil
}

// ---- AWS::IdentityStore::Group on moto's Identity Store ----

// IdentityStoreGroup is the backend of a group in the Identity Center
// directory, identified by "<GroupId>|<IdentityStoreId>" as Cloud Control
// joins its two primary identifiers. moto 5.2.5 implements Create, Describe
// and DeleteGroup and has no UpdateGroup: an update fails here, so a
// change of DisplayName or Description is unproven on the fake.
type IdentityStoreGroup struct{ IS *identitystore.Client }

// NewIdentityStoreGroup makes the backend.
func NewIdentityStoreGroup(cfg aws.Config) *IdentityStoreGroup {
	return &IdentityStoreGroup{IS: identitystore.NewFromConfig(cfg)}
}

func (b *IdentityStoreGroup) Create(ctx context.Context, m map[string]any) (string, error) {
	in := &identitystore.CreateGroupInput{IdentityStoreId: aws.String(str(m, "IdentityStoreId")), DisplayName: aws.String(str(m, "DisplayName"))}
	if d := str(m, "Description"); d != "" {
		in.Description = aws.String(d)
	}
	out, err := b.IS.CreateGroup(ctx, in)
	if err != nil {
		return "", InvalidRequest("%v", err)
	}
	return aws.ToString(out.GroupId) + "|" + aws.ToString(out.IdentityStoreId), nil
}

func (b *IdentityStoreGroup) Read(ctx context.Context, id string) (map[string]any, error) {
	gid, store, err := idPair(id)
	if err != nil {
		return nil, err
	}
	out, err := b.IS.DescribeGroup(ctx, &identitystore.DescribeGroupInput{IdentityStoreId: aws.String(store), GroupId: aws.String(gid)})
	if err != nil {
		if code(err) == "ResourceNotFoundException" {
			return nil, NotFound("group %s", id)
		}
		return nil, err
	}
	m := map[string]any{"GroupId": gid, "IdentityStoreId": store, "DisplayName": aws.ToString(out.DisplayName)}
	if d := aws.ToString(out.Description); d != "" {
		m["Description"] = d
	}
	return m, nil
}

func (b *IdentityStoreGroup) Update(context.Context, string, map[string]any, map[string]any) error {
	return InvalidRequest("moto 5.2.5 has no UpdateGroup")
}

func (b *IdentityStoreGroup) Delete(ctx context.Context, id string) error {
	if _, err := b.Read(ctx, id); err != nil {
		return err
	}
	gid, store, _ := idPair(id)
	_, err := b.IS.DeleteGroup(ctx, &identitystore.DeleteGroupInput{IdentityStoreId: aws.String(store), GroupId: aws.String(gid)})
	return err
}

// ---- AWS::IdentityStore::GroupMembership on moto's Identity Store ----

// IdentityStoreGroupMembership is the backend of a user's membership of a
// group, identified by "<MembershipId>|<IdentityStoreId>". Every property
// is create-only, so there is no update. moto 5.2.5 implements Create,
// Describe and DeleteGroupMembership.
type IdentityStoreGroupMembership struct{ IS *identitystore.Client }

// NewIdentityStoreGroupMembership makes the backend.
func NewIdentityStoreGroupMembership(cfg aws.Config) *IdentityStoreGroupMembership {
	return &IdentityStoreGroupMembership{IS: identitystore.NewFromConfig(cfg)}
}

func (b *IdentityStoreGroupMembership) Create(ctx context.Context, m map[string]any) (string, error) {
	member, _ := m["MemberId"].(map[string]any)
	out, err := b.IS.CreateGroupMembership(ctx, &identitystore.CreateGroupMembershipInput{
		IdentityStoreId: aws.String(str(m, "IdentityStoreId")),
		GroupId:         aws.String(str(m, "GroupId")),
		MemberId:        &istypes.MemberIdMemberUserId{Value: str(member, "UserId")},
	})
	if err != nil {
		return "", InvalidRequest("%v", err)
	}
	return aws.ToString(out.MembershipId) + "|" + aws.ToString(out.IdentityStoreId), nil
}

func (b *IdentityStoreGroupMembership) Read(ctx context.Context, id string) (map[string]any, error) {
	mid, store, err := idPair(id)
	if err != nil {
		return nil, err
	}
	out, err := b.IS.DescribeGroupMembership(ctx, &identitystore.DescribeGroupMembershipInput{IdentityStoreId: aws.String(store), MembershipId: aws.String(mid)})
	if err != nil {
		if code(err) == "ResourceNotFoundException" {
			return nil, NotFound("membership %s", id)
		}
		return nil, err
	}
	m := map[string]any{"MembershipId": mid, "IdentityStoreId": store, "GroupId": aws.ToString(out.GroupId)}
	if u, ok := out.MemberId.(*istypes.MemberIdMemberUserId); ok {
		m["MemberId"] = map[string]any{"UserId": u.Value}
	}
	return m, nil
}

func (b *IdentityStoreGroupMembership) Update(context.Context, string, map[string]any, map[string]any) error {
	return InvalidRequest("every property of a group membership is create-only")
}

func (b *IdentityStoreGroupMembership) Delete(ctx context.Context, id string) error {
	if _, err := b.Read(ctx, id); err != nil {
		return err
	}
	mid, store, _ := idPair(id)
	_, err := b.IS.DeleteGroupMembership(ctx, &identitystore.DeleteGroupMembershipInput{IdentityStoreId: aws.String(store), MembershipId: aws.String(mid)})
	return err
}
