// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	ackiam "github.com/aws-controllers-k8s/iam-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	iamv1 "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

const trust = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"pods.eks.amazonaws.com"},"Action":["sts:AssumeRole","sts:TagSession"]}]}`
const s3Read = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`

func attached(ctx context.Context, c *iam.Client, role string) (map[string]bool, error) {
	out, err := c.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{RoleName: aws.String(role)})
	if err != nil {
		return nil, err
	}
	m := map[string]bool{}
	for _, p := range out.AttachedPolicies {
		m[aws.ToString(p.PolicyArn)] = true
	}
	return m, nil
}

func synced(conds []*ackv1alpha1.Condition) (bool, string) {
	for _, c := range conds {
		if c.Type == ackv1alpha1.ConditionTypeTerminal && c.Status == corev1.ConditionTrue {
			return false, "terminal: " + aws.ToString(c.Message)
		}
	}
	for _, c := range conds {
		if c.Type == ackv1alpha1.ConditionTypeResourceSynced {
			return c.Status == corev1.ConditionTrue, string(c.Status) + " " + aws.ToString(c.Message)
		}
	}
	return false, "no ACK.ResourceSynced condition"
}

// The case for the kind: a role that ACK does not manage (made by another
// team, by Terraform, or by EKS for a node group) gets a policy attached,
// and loses it when the object goes.
func TestRolePolicyAttachment(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = iamv1.AddToScheme(scheme)
	_ = ackiam.AddToScheme(scheme)
	env := harness.Start(t, "iam", scheme)
	ctx := context.Background()
	cloud := iam.NewFromConfig(env.AWS)

	role := "app-workload"
	if _, err := cloud.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String(role), AssumeRolePolicyDocument: aws.String(trust)}); err != nil {
		t.Fatal(err)
	}
	pol, err := cloud.CreatePolicy(ctx, &iam.CreatePolicyInput{PolicyName: aws.String("app-s3-read"), PolicyDocument: aws.String(s3Read)})
	if err != nil {
		t.Fatal(err)
	}
	policyARN := aws.ToString(pol.Policy.Arn)
	// Deleted last (cleanups run in reverse): the roles are detached first.
	cleanupLog(t, "policy app-s3-read", func() error { return deleteIAMPolicy(ctx, cloud, policyARN) })
	cleanupLog(t, "role "+role, func() error { return deleteIAMRole(ctx, cloud, role) })

	rpa := &iamv1.RolePolicyAttachment{
		ObjectMeta: metav1.ObjectMeta{Name: "app-s3-read", Namespace: "default"},
		Spec:       iamv1.RolePolicyAttachmentSpec{RoleName: aws.String(role), PolicyARN: aws.String(policyARN)},
	}

	t.Run("create attaches the policy", func(t *testing.T) {
		if err := env.Client.Create(ctx, rpa); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "RolePolicyAttachment synced", func() error {
			got := &iamv1.RolePolicyAttachment{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(rpa), got); err != nil {
				return err
			}
			if ok, why := synced(got.Status.Conditions); !ok {
				return fmt.Errorf("not synced: %s", why)
			}
			return nil
		})
		m, err := attached(ctx, cloud, role)
		if err != nil {
			t.Fatal(err)
		}
		if !m[policyARN] {
			t.Fatalf("cloud: %s not attached to %s: %v", policyARN, role, m)
		}
	})

	t.Run("references resolve to ACK's Role", func(t *testing.T) {
		// An ACK Role and Policy, as ACK's iam-controller would leave them
		// once synced (that controller does not run here).
		if _, err := cloud.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String("ack-made"), AssumeRolePolicyDocument: aws.String(trust)}); err != nil {
			t.Fatal(err)
		}
		cleanupLog(t, "role ack-made", func() error { return deleteIAMRole(ctx, cloud, "ack-made") })
		ackRole := &ackiam.Role{
			ObjectMeta: metav1.ObjectMeta{Name: "ack-made", Namespace: "default"},
			Spec:       ackiam.RoleSpec{Name: aws.String("ack-made"), AssumeRolePolicyDocument: aws.String(trust)},
		}
		if err := env.Client.Create(ctx, ackRole); err != nil {
			t.Fatal(err)
		}
		ackRole.Status.Conditions = []*ackv1alpha1.Condition{{Type: ackv1alpha1.ConditionTypeResourceSynced, Status: corev1.ConditionTrue}}
		if err := env.Client.Status().Update(ctx, ackRole); err != nil {
			t.Fatal(err)
		}
		byRef := &iamv1.RolePolicyAttachment{
			ObjectMeta: metav1.ObjectMeta{Name: "by-ref", Namespace: "default"},
			Spec: iamv1.RolePolicyAttachmentSpec{
				RoleRef:   &ackv1alpha1.AWSResourceReferenceWrapper{From: &ackv1alpha1.AWSResourceReference{Name: aws.String("ack-made")}},
				PolicyARN: aws.String(policyARN),
			},
		}
		if err := env.Client.Create(ctx, byRef); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "attached through roleRef", func() error {
			m, err := attached(ctx, cloud, "ack-made")
			if err != nil {
				return err
			}
			if !m[policyARN] {
				return fmt.Errorf("not attached yet: %v", m)
			}
			return nil
		})
	})

	// Plan question 3 (answered 2026-11-15): ACK's Role syncs
	// spec.policies as the whole set and would detach an attachment made
	// beside it, so a roleRef to a Role that lists policies is refused with
	// ACK.Terminal naming the Role and the field, and nothing is attached.
	// The kind's fields are immutable and a terminal object is not
	// resynced, so once the Role's list is emptied the object is made again
	// (deleted and recreated); its delete detaches nothing.
	t.Run("refused beside an ACK Role that lists policies", func(t *testing.T) {
		if _, err := cloud.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String("ack-owned"), AssumeRolePolicyDocument: aws.String(trust)}); err != nil {
			t.Fatal(err)
		}
		owned := &ackiam.Role{
			ObjectMeta: metav1.ObjectMeta{Name: "ack-owned", Namespace: "default"},
			Spec: ackiam.RoleSpec{Name: aws.String("ack-owned"), AssumeRolePolicyDocument: aws.String(trust),
				Policies: []*string{aws.String("arn:aws:iam::aws:policy/ReadOnlyAccess")}},
		}
		if err := env.Client.Create(ctx, owned); err != nil {
			t.Fatal(err)
		}
		owned.Status.Conditions = []*ackv1alpha1.Condition{{Type: ackv1alpha1.ConditionTypeResourceSynced, Status: corev1.ConditionTrue}}
		if err := env.Client.Status().Update(ctx, owned); err != nil {
			t.Fatal(err)
		}
		beside := func() *iamv1.RolePolicyAttachment {
			return &iamv1.RolePolicyAttachment{
				ObjectMeta: metav1.ObjectMeta{Name: "beside-owner", Namespace: "default"},
				Spec: iamv1.RolePolicyAttachmentSpec{
					RoleRef:   &ackv1alpha1.AWSResourceReferenceWrapper{From: &ackv1alpha1.AWSResourceReference{Name: aws.String("ack-owned")}},
					PolicyARN: aws.String(policyARN),
				},
			}
		}
		refused := beside()
		if err := env.Client.Create(ctx, refused); err != nil {
			t.Fatal(err)
		}
		want := "Role default/ack-owned lists spec.policies"
		harness.Eventually(t, 60*time.Second, "refused with ACK.Terminal", func() error {
			got := &iamv1.RolePolicyAttachment{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(refused), got); err != nil {
				return err
			}
			for _, c := range got.Status.Conditions {
				if c.Type == ackv1alpha1.ConditionTypeTerminal && c.Status == corev1.ConditionTrue {
					if msg := aws.ToString(c.Message); !strings.Contains(msg, want) || !strings.Contains(msg, "this RolePolicyAttachment would be undone") {
						return fmt.Errorf("ACK.Terminal's message %q does not name the conflict", msg)
					}
					return nil
				}
			}
			return fmt.Errorf("no ACK.Terminal: %v", got.Status.Conditions)
		})
		if m, err := attached(ctx, cloud, "ack-owned"); err != nil || m[policyARN] {
			t.Fatalf("a refused attachment reached IAM: %v %v", m, err)
		}

		// The Role's list emptied; the object made again is attached.
		harness.Eventually(t, 30*time.Second, "Role's policies emptied", func() error {
			cur := &ackiam.Role{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(owned), cur); err != nil {
				return err
			}
			cur.Spec.Policies = nil
			return env.Client.Update(ctx, cur)
		})
		if err := env.Client.Delete(ctx, refused); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "refused object gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(refused), &iamv1.RolePolicyAttachment{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
		again := beside()
		if err := env.Client.Create(ctx, again); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "attached once the Role lists no policies", func() error {
			m, err := attached(ctx, cloud, "ack-owned")
			if err != nil {
				return err
			}
			if !m[policyARN] {
				return fmt.Errorf("not attached yet: %v", m)
			}
			return nil
		})
	})

	t.Run("delete detaches the policy", func(t *testing.T) {
		if err := env.Client.Delete(ctx, rpa); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "policy detached and object gone", func() error {
			m, err := attached(ctx, cloud, role)
			if err != nil {
				return err
			}
			if m[policyARN] {
				return fmt.Errorf("still attached")
			}
			err = env.Client.Get(ctx, client.ObjectKeyFromObject(rpa), &iamv1.RolePolicyAttachment{})
			if client.IgnoreNotFound(err) != nil {
				return err
			}
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return nil
		})
	})

	t.Run("deletion-policy retain keeps the attachment", func(t *testing.T) {
		keep := &iamv1.RolePolicyAttachment{
			ObjectMeta: metav1.ObjectMeta{
				Name: "kept", Namespace: "default",
				Annotations: map[string]string{ackv1alpha1.AnnotationDeletionPolicy: string(ackv1alpha1.DeletionPolicyRetain)},
			},
			Spec: iamv1.RolePolicyAttachmentSpec{RoleName: aws.String(role), PolicyARN: aws.String(policyARN)},
		}
		if err := env.Client.Create(ctx, keep); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "attached", func() error {
			m, err := attached(ctx, cloud, role)
			if err != nil {
				return err
			}
			if !m[policyARN] {
				return fmt.Errorf("not attached")
			}
			return nil
		})
		if err := env.Client.Delete(ctx, keep); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "object gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(keep), &iamv1.RolePolicyAttachment{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
		m, err := attached(ctx, cloud, role)
		if err != nil {
			t.Fatal(err)
		}
		if !m[policyARN] {
			t.Fatalf("deletion-policy retain detached the policy")
		}
	})
}

// IAM fixtures made with the SDK are deleted after the test: on a live run
// nothing else would (live run ackx-11160256-57fe left users, groups,
// roles, a policy and 105 MFA devices). What hangs on them is taken off
// first, as IAM requires.

func deleteIAMUser(ctx context.Context, c *iam.Client, name string) error {
	u := aws.String(name)
	if keys, err := c.ListAccessKeys(ctx, &iam.ListAccessKeysInput{UserName: u}); err == nil {
		for _, k := range keys.AccessKeyMetadata {
			_, _ = c.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{UserName: u, AccessKeyId: k.AccessKeyId})
		}
	}
	_, _ = c.DeleteLoginProfile(ctx, &iam.DeleteLoginProfileInput{UserName: u})
	if keys, err := c.ListSSHPublicKeys(ctx, &iam.ListSSHPublicKeysInput{UserName: u}); err == nil {
		for _, k := range keys.SSHPublicKeys {
			_, _ = c.DeleteSSHPublicKey(ctx, &iam.DeleteSSHPublicKeyInput{UserName: u, SSHPublicKeyId: k.SSHPublicKeyId})
		}
	}
	if mfa, err := c.ListMFADevices(ctx, &iam.ListMFADevicesInput{UserName: u}); err == nil {
		for _, d := range mfa.MFADevices {
			_, _ = c.DeactivateMFADevice(ctx, &iam.DeactivateMFADeviceInput{UserName: u, SerialNumber: d.SerialNumber})
		}
	}
	if gs, err := c.ListGroupsForUser(ctx, &iam.ListGroupsForUserInput{UserName: u}); err == nil {
		for _, g := range gs.Groups {
			_, _ = c.RemoveUserFromGroup(ctx, &iam.RemoveUserFromGroupInput{UserName: u, GroupName: g.GroupName})
		}
	}
	if ps, err := c.ListAttachedUserPolicies(ctx, &iam.ListAttachedUserPoliciesInput{UserName: u}); err == nil {
		for _, p := range ps.AttachedPolicies {
			_, _ = c.DetachUserPolicy(ctx, &iam.DetachUserPolicyInput{UserName: u, PolicyArn: p.PolicyArn})
		}
	}
	_, err := c.DeleteUser(ctx, &iam.DeleteUserInput{UserName: u})
	return err
}

func deleteIAMGroup(ctx context.Context, c *iam.Client, name string) error {
	g := aws.String(name)
	if out, err := c.GetGroup(ctx, &iam.GetGroupInput{GroupName: g}); err == nil {
		for _, u := range out.Users {
			_, _ = c.RemoveUserFromGroup(ctx, &iam.RemoveUserFromGroupInput{UserName: u.UserName, GroupName: g})
		}
	}
	if ps, err := c.ListAttachedGroupPolicies(ctx, &iam.ListAttachedGroupPoliciesInput{GroupName: g}); err == nil {
		for _, p := range ps.AttachedPolicies {
			_, _ = c.DetachGroupPolicy(ctx, &iam.DetachGroupPolicyInput{GroupName: g, PolicyArn: p.PolicyArn})
		}
	}
	_, err := c.DeleteGroup(ctx, &iam.DeleteGroupInput{GroupName: g})
	return err
}

func deleteIAMRole(ctx context.Context, c *iam.Client, name string) error {
	r := aws.String(name)
	if ps, err := c.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{RoleName: r}); err == nil {
		for _, p := range ps.AttachedPolicies {
			_, _ = c.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{RoleName: r, PolicyArn: p.PolicyArn})
		}
	}
	_, err := c.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: r})
	return err
}

func deleteIAMPolicy(ctx context.Context, c *iam.Client, arn string) error {
	if vs, err := c.ListPolicyVersions(ctx, &iam.ListPolicyVersionsInput{PolicyArn: aws.String(arn)}); err == nil {
		for _, v := range vs.Versions {
			if !v.IsDefaultVersion {
				_, _ = c.DeletePolicyVersion(ctx, &iam.DeletePolicyVersionInput{PolicyArn: aws.String(arn), VersionId: v.VersionId})
			}
		}
	}
	_, err := c.DeletePolicy(ctx, &iam.DeletePolicyInput{PolicyArn: aws.String(arn)})
	return err
}
