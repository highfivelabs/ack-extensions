// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssoadmin"
	ssotypes "github.com/aws/aws-sdk-go-v2/service/ssoadmin/types"
)

// ---- AWS::SSO::PermissionSet on moto's sso-admin ----

// PermissionSet is the Identity Center permission set backend. moto 5.2.5
// implements the permission set, its managed and customer managed policy
// attachments and its inline policy; it has no TagResource,
// ListTagsForResource or permissions boundary calls for sso-admin, so the
// backend keeps Tags and PermissionsBoundary itself. The identifier is
// InstanceArn|PermissionSetArn, the schema's primary identifier.
type PermissionSet struct {
	SSO   *ssoadmin.Client
	mu    sync.Mutex
	extra map[string]map[string]any
}

// NewPermissionSet makes the backend.
func NewPermissionSet(cfg aws.Config) *PermissionSet {
	return &PermissionSet{SSO: ssoadmin.NewFromConfig(cfg), extra: map[string]map[string]any{}}
}

var permissionSetExtra = []string{"Tags", "PermissionsBoundary"}

func splitID(id string, n int) ([]string, error) {
	p := strings.Split(id, "|")
	if len(p) != n {
		return nil, NotFound("identifier %q has %d parts, want %d", id, len(p), n)
	}
	return p, nil
}

func strList(v any) []string {
	l, _ := v.([]any)
	var out []string
	for _, x := range l {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func cmpRefs(v any) map[string]ssotypes.CustomerManagedPolicyReference {
	out := map[string]ssotypes.CustomerManagedPolicyReference{}
	l, _ := v.([]any)
	for _, x := range l {
		m, _ := x.(map[string]any)
		path := str(m, "Path")
		if path == "" {
			path = "/"
		}
		out[path+str(m, "Name")] = ssotypes.CustomerManagedPolicyReference{Name: aws.String(str(m, "Name")), Path: aws.String(path)}
	}
	return out
}

// inlineJSON is the inline policy as the API takes it: a document given as
// an object (the CRD's free-form field) or a string.
func inlineJSON(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func notFoundOr(err error, id string) error {
	if c := code(err); c == "ResourceNotFoundException" || c == "ResourceNotFound" {
		return NotFound("%s", id)
	}
	return err
}

func (b *PermissionSet) Create(ctx context.Context, m map[string]any) (string, error) {
	inst := str(m, "InstanceArn")
	in := &ssoadmin.CreatePermissionSetInput{InstanceArn: aws.String(inst), Name: aws.String(str(m, "Name"))}
	if v := str(m, "Description"); v != "" {
		in.Description = aws.String(v)
	}
	if v := str(m, "SessionDuration"); v != "" {
		in.SessionDuration = aws.String(v)
	}
	if v := str(m, "RelayStateType"); v != "" {
		in.RelayState = aws.String(v)
	}
	out, err := b.SSO.CreatePermissionSet(ctx, in)
	if err != nil {
		if code(err) == "ConflictException" {
			return "", &HandlerError{"AlreadyExists", err.Error()}
		}
		return "", InvalidRequest("%v", err)
	}
	ps := aws.ToString(out.PermissionSet.PermissionSetArn)
	if err := b.sync(ctx, inst, ps, map[string]any{}, m); err != nil {
		return "", err
	}
	return inst + "|" + ps, nil
}

// sync makes the attachments and the inline policy go from prev to next,
// one call per difference, as the handler does.
func (b *PermissionSet) sync(ctx context.Context, inst, ps string, prev, next map[string]any) error {
	was, want := strList(prev["ManagedPolicies"]), strList(next["ManagedPolicies"])
	for _, a := range want {
		if !contains(was, a) {
			if _, err := b.SSO.AttachManagedPolicyToPermissionSet(ctx, &ssoadmin.AttachManagedPolicyToPermissionSetInput{InstanceArn: &inst, PermissionSetArn: &ps, ManagedPolicyArn: aws.String(a)}); err != nil {
				return InvalidRequest("%v", err)
			}
		}
	}
	for _, a := range was {
		if !contains(want, a) {
			if _, err := b.SSO.DetachManagedPolicyFromPermissionSet(ctx, &ssoadmin.DetachManagedPolicyFromPermissionSetInput{InstanceArn: &inst, PermissionSetArn: &ps, ManagedPolicyArn: aws.String(a)}); err != nil {
				return InvalidRequest("%v", err)
			}
		}
	}
	wasRefs, wantRefs := cmpRefs(prev["CustomerManagedPolicyReferences"]), cmpRefs(next["CustomerManagedPolicyReferences"])
	for k, r := range wantRefs {
		if _, ok := wasRefs[k]; !ok {
			if _, err := b.SSO.AttachCustomerManagedPolicyReferenceToPermissionSet(ctx, &ssoadmin.AttachCustomerManagedPolicyReferenceToPermissionSetInput{InstanceArn: &inst, PermissionSetArn: &ps, CustomerManagedPolicyReference: &r}); err != nil {
				return InvalidRequest("%v", err)
			}
		}
	}
	for k, r := range wasRefs {
		if _, ok := wantRefs[k]; !ok {
			if _, err := b.SSO.DetachCustomerManagedPolicyReferenceFromPermissionSet(ctx, &ssoadmin.DetachCustomerManagedPolicyReferenceFromPermissionSetInput{InstanceArn: &inst, PermissionSetArn: &ps, CustomerManagedPolicyReference: &r}); err != nil {
				return InvalidRequest("%v", err)
			}
		}
	}
	if was, want := inlineJSON(prev["InlinePolicy"]), inlineJSON(next["InlinePolicy"]); was != want {
		var err error
		if want == "" {
			_, err = b.SSO.DeleteInlinePolicyFromPermissionSet(ctx, &ssoadmin.DeleteInlinePolicyFromPermissionSetInput{InstanceArn: &inst, PermissionSetArn: &ps})
		} else {
			_, err = b.SSO.PutInlinePolicyToPermissionSet(ctx, &ssoadmin.PutInlinePolicyToPermissionSetInput{InstanceArn: &inst, PermissionSetArn: &ps, InlinePolicy: aws.String(want)})
		}
		if err != nil {
			return InvalidRequest("%v", err)
		}
	}
	b.mu.Lock()
	b.extra[ps] = map[string]any{}
	for _, k := range permissionSetExtra {
		if v, ok := next[k]; ok {
			b.extra[ps][k] = v
		}
	}
	b.mu.Unlock()
	return nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func (b *PermissionSet) Read(ctx context.Context, id string) (map[string]any, error) {
	p, err := splitID(id, 2)
	if err != nil {
		return nil, err
	}
	inst, ps := p[0], p[1]
	d, err := b.SSO.DescribePermissionSet(ctx, &ssoadmin.DescribePermissionSetInput{InstanceArn: &inst, PermissionSetArn: &ps})
	if err != nil {
		return nil, notFoundOr(err, id)
	}
	m := map[string]any{"InstanceArn": inst, "PermissionSetArn": ps, "Name": aws.ToString(d.PermissionSet.Name)}
	for k, v := range map[string]*string{"Description": d.PermissionSet.Description, "SessionDuration": d.PermissionSet.SessionDuration, "RelayStateType": d.PermissionSet.RelayState} {
		if aws.ToString(v) != "" {
			m[k] = aws.ToString(v)
		}
	}
	mp, err := b.SSO.ListManagedPoliciesInPermissionSet(ctx, &ssoadmin.ListManagedPoliciesInPermissionSetInput{InstanceArn: &inst, PermissionSetArn: &ps})
	if err != nil {
		return nil, err
	}
	if len(mp.AttachedManagedPolicies) > 0 {
		var l []any
		for _, a := range mp.AttachedManagedPolicies {
			l = append(l, aws.ToString(a.Arn))
		}
		m["ManagedPolicies"] = l
	}
	cr, err := b.SSO.ListCustomerManagedPolicyReferencesInPermissionSet(ctx, &ssoadmin.ListCustomerManagedPolicyReferencesInPermissionSetInput{InstanceArn: &inst, PermissionSetArn: &ps})
	if err != nil {
		return nil, err
	}
	if len(cr.CustomerManagedPolicyReferences) > 0 {
		var l []any
		for _, r := range cr.CustomerManagedPolicyReferences {
			l = append(l, map[string]any{"Name": aws.ToString(r.Name), "Path": aws.ToString(r.Path)})
		}
		m["CustomerManagedPolicyReferences"] = l
	}
	ip, err := b.SSO.GetInlinePolicyForPermissionSet(ctx, &ssoadmin.GetInlinePolicyForPermissionSetInput{InstanceArn: &inst, PermissionSetArn: &ps})
	if err != nil {
		return nil, err
	}
	if doc := aws.ToString(ip.InlinePolicy); doc != "" {
		var obj any
		if json.Unmarshal([]byte(doc), &obj) == nil {
			m["InlinePolicy"] = obj
		} else {
			m["InlinePolicy"] = doc
		}
	}
	b.mu.Lock()
	for k, v := range b.extra[ps] {
		m[k] = v
	}
	b.mu.Unlock()
	return m, nil
}

func (b *PermissionSet) Update(ctx context.Context, id string, prev, next map[string]any) error {
	p, err := splitID(id, 2)
	if err != nil {
		return err
	}
	inst, ps := p[0], p[1]
	if str(prev, "Description") != str(next, "Description") || str(prev, "SessionDuration") != str(next, "SessionDuration") || str(prev, "RelayStateType") != str(next, "RelayStateType") {
		in := &ssoadmin.UpdatePermissionSetInput{InstanceArn: &inst, PermissionSetArn: &ps}
		if v := str(next, "Description"); v != "" {
			in.Description = aws.String(v)
		}
		if v := str(next, "SessionDuration"); v != "" {
			in.SessionDuration = aws.String(v)
		}
		if v := str(next, "RelayStateType"); v != "" {
			in.RelayState = aws.String(v)
		}
		if _, err := b.SSO.UpdatePermissionSet(ctx, in); err != nil {
			return InvalidRequest("%v", err)
		}
	}
	if err := b.sync(ctx, inst, ps, prev, next); err != nil {
		return err
	}
	// The handler provisions the set to the accounts it is assigned to.
	if _, err := b.SSO.ProvisionPermissionSet(ctx, &ssoadmin.ProvisionPermissionSetInput{InstanceArn: &inst, PermissionSetArn: &ps, TargetType: ssotypes.ProvisionTargetTypeAllProvisionedAccounts}); err != nil {
		return err
	}
	return nil
}

func (b *PermissionSet) Delete(ctx context.Context, id string) error {
	p, err := splitID(id, 2)
	if err != nil {
		return err
	}
	if _, err := b.SSO.DeletePermissionSet(ctx, &ssoadmin.DeletePermissionSetInput{InstanceArn: &p[0], PermissionSetArn: &p[1]}); err != nil {
		return notFoundOr(err, id)
	}
	b.mu.Lock()
	delete(b.extra, p[1])
	b.mu.Unlock()
	return nil
}

// ---- AWS::SSO::Assignment on moto's sso-admin ----

// Assignment is the account assignment backend: a permission set assigned
// to a user or group in an account. Every property is create-only and the
// identifier is all six, in the schema's order.
type Assignment struct{ SSO *ssoadmin.Client }

// NewAssignment makes the backend.
func NewAssignment(cfg aws.Config) *Assignment {
	return &Assignment{SSO: ssoadmin.NewFromConfig(cfg)}
}

var assignmentID = []string{"InstanceArn", "TargetId", "TargetType", "PermissionSetArn", "PrincipalType", "PrincipalId"}

func (b *Assignment) Create(ctx context.Context, m map[string]any) (string, error) {
	_, err := b.SSO.CreateAccountAssignment(ctx, &ssoadmin.CreateAccountAssignmentInput{
		InstanceArn: aws.String(str(m, "InstanceArn")), TargetId: aws.String(str(m, "TargetId")),
		TargetType: ssotypes.TargetType(str(m, "TargetType")), PermissionSetArn: aws.String(str(m, "PermissionSetArn")),
		PrincipalType: ssotypes.PrincipalType(str(m, "PrincipalType")), PrincipalId: aws.String(str(m, "PrincipalId")),
	})
	if err != nil {
		return "", InvalidRequest("%v", err)
	}
	var parts []string
	for _, k := range assignmentID {
		parts = append(parts, str(m, k))
	}
	return strings.Join(parts, "|"), nil
}

func (b *Assignment) Read(ctx context.Context, id string) (map[string]any, error) {
	p, err := splitID(id, len(assignmentID))
	if err != nil {
		return nil, err
	}
	out, err := b.SSO.ListAccountAssignments(ctx, &ssoadmin.ListAccountAssignmentsInput{InstanceArn: &p[0], AccountId: &p[1], PermissionSetArn: &p[3]})
	if err != nil {
		return nil, notFoundOr(err, id)
	}
	for _, a := range out.AccountAssignments {
		if string(a.PrincipalType) == p[4] && aws.ToString(a.PrincipalId) == p[5] {
			m := map[string]any{}
			for i, k := range assignmentID {
				m[k] = p[i]
			}
			return m, nil
		}
	}
	return nil, NotFound("%s", id)
}

func (b *Assignment) Update(context.Context, string, map[string]any, map[string]any) error {
	return &HandlerError{"NotUpdatable", "AWS::SSO::Assignment has no update handler"}
}

func (b *Assignment) Delete(ctx context.Context, id string) error {
	if _, err := b.Read(ctx, id); err != nil {
		return err
	}
	p, _ := splitID(id, len(assignmentID))
	if _, err := b.SSO.DeleteAccountAssignment(ctx, &ssoadmin.DeleteAccountAssignmentInput{
		InstanceArn: &p[0], TargetId: &p[1], TargetType: ssotypes.TargetType(p[2]), PermissionSetArn: &p[3],
		PrincipalType: ssotypes.PrincipalType(p[4]), PrincipalId: &p[5],
	}); err != nil {
		return fmt.Errorf("delete assignment: %w", err)
	}
	return nil
}
