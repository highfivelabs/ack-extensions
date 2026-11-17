// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/identitystore"
	"github.com/aws/aws-sdk-go-v2/service/identitystore/document"
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

// ---- AWS::IdentityStore::User on moto's Identity Store ----

// IdentityStoreUser is the backend of a user in the Identity Center directory,
// identified by its Arn ("arn:aws:identitystore:::<IdentityStoreId>/user/<UserId>")
// so Read, Update and Delete have both IDs without keeping state.
type IdentityStoreUser struct{ IS *identitystore.Client }

// NewIdentityStoreUser makes the backend.
func NewIdentityStoreUser(cfg aws.Config) *IdentityStoreUser {
	return &IdentityStoreUser{IS: identitystore.NewFromConfig(cfg)}
}

func userIDFromARN(arn string) (store, uid string, err error) {
	rest := strings.TrimPrefix(arn, "arn:aws:identitystore:::")
	parts := strings.Split(rest, "/user/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", NotFound("user %s", arn)
	}
	return parts[0], parts[1], nil
}

func (b *IdentityStoreUser) Create(ctx context.Context, m map[string]any) (string, error) {
	store := str(m, "IdentityStoreId")
	in := &identitystore.CreateUserInput{IdentityStoreId: aws.String(store)}
	if v := str(m, "UserName"); v != "" {
		in.UserName = aws.String(v)
	}
	if v := str(m, "DisplayName"); v != "" {
		in.DisplayName = aws.String(v)
	}
	if v := str(m, "Title"); v != "" {
		in.Title = aws.String(v)
	}
	if nm, ok := m["Name"].(map[string]any); ok {
		in.Name = &istypes.Name{
			GivenName:  optStr(nm, "GivenName"),
			FamilyName: optStr(nm, "FamilyName"),
		}
	}
	out, err := b.IS.CreateUser(ctx, in)
	if err != nil {
		return "", InvalidRequest("%v", err)
	}
	return "arn:aws:identitystore:::" + store + "/user/" + aws.ToString(out.UserId), nil
}

func optStr(m map[string]any, k string) *string {
	if s := str(m, k); s != "" {
		return aws.String(s)
	}
	return nil
}

func (b *IdentityStoreUser) Read(ctx context.Context, arn string) (map[string]any, error) {
	store, uid, err := userIDFromARN(arn)
	if err != nil {
		return nil, err
	}
	out, err := b.IS.DescribeUser(ctx, &identitystore.DescribeUserInput{IdentityStoreId: aws.String(store), UserId: aws.String(uid)})
	if err != nil {
		if code(err) == "ResourceNotFoundException" {
			return nil, NotFound("user %s", arn)
		}
		return nil, err
	}
	m := map[string]any{
		"Arn":             arn,
		"IdentityStoreId": store,
		"UserId":          uid,
	}
	if out.UserName != nil {
		m["UserName"] = aws.ToString(out.UserName)
	}
	if out.DisplayName != nil {
		m["DisplayName"] = aws.ToString(out.DisplayName)
	}
	if out.Title != nil {
		m["Title"] = aws.ToString(out.Title)
	}
	if out.Name != nil {
		nm := map[string]any{}
		if out.Name.GivenName != nil {
			nm["GivenName"] = aws.ToString(out.Name.GivenName)
		}
		if out.Name.FamilyName != nil {
			nm["FamilyName"] = aws.ToString(out.Name.FamilyName)
		}
		if len(nm) > 0 {
			m["Name"] = nm
		}
	}
	return m, nil
}

func (b *IdentityStoreUser) Update(ctx context.Context, arn string, _, next map[string]any) error {
	store, uid, err := userIDFromARN(arn)
	if err != nil {
		return err
	}
	var ops []istypes.AttributeOperation
	for _, k := range []struct{ cfn, path string }{
		{"DisplayName", "displayName"},
		{"Title", "title"},
	} {
		if v := str(next, k.cfn); v != "" {
			ops = append(ops, istypes.AttributeOperation{
				AttributePath:  aws.String(k.path),
				AttributeValue: document.NewLazyDocument(v),
			})
		}
	}
	if len(ops) == 0 {
		return nil
	}
	_, err = b.IS.UpdateUser(ctx, &identitystore.UpdateUserInput{
		IdentityStoreId: aws.String(store),
		UserId:          aws.String(uid),
		Operations:      ops,
	})
	if err != nil {
		return InvalidRequest("%v", err)
	}
	return nil
}

func (b *IdentityStoreUser) Delete(ctx context.Context, arn string) error {
	if _, err := b.Read(ctx, arn); err != nil {
		return err
	}
	store, uid, _ := userIDFromARN(arn)
	_, err := b.IS.DeleteUser(ctx, &identitystore.DeleteUserInput{IdentityStoreId: aws.String(store), UserId: aws.String(uid)})
	return err
}
