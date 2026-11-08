// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
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
