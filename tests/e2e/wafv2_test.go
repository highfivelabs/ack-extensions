// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackwafv2 "github.com/aws-controllers-k8s/wafv2-controller/apis/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/wafv2"
	wafv2types "github.com/aws/aws-sdk-go-v2/service/wafv2/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	wafv2v1 "github.com/highfivelabs/ack-extensions/services/wafv2/apis/v1alpha1"
)

// webACLOf is the ARN of the web ACL the resource is associated with, ""
// when none.
func webACLOf(ctx context.Context, c *wafv2.Client, resourceARN string) (string, error) {
	out, err := c.GetWebACLForResource(ctx, &wafv2.GetWebACLForResourceInput{ResourceArn: aws.String(resourceARN)})
	if err != nil {
		return "", err
	}
	if out.WebACL == nil {
		return "", nil
	}
	return aws.ToString(out.WebACL.ARN), nil
}

// WebACLAssociation: a regional web ACL associated with a resource,
// Terraform's aws_wafv2_web_acl_association. ACK's wafv2-controller
// (v1.8.1) has WebACL but no association.
func TestWebACLAssociation(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = wafv2v1.AddToScheme(scheme)
	_ = ackwafv2.AddToScheme(scheme)
	env := harness.Start(t, "wafv2", scheme)
	cloud := wafv2.NewFromConfig(env.AWS)
	ctx := context.Background()

	acl, err := cloud.CreateWebACL(ctx, &wafv2.CreateWebACLInput{
		Name: aws.String("edge"), Scope: wafv2types.ScopeRegional,
		DefaultAction:    &wafv2types.DefaultAction{Allow: &wafv2types.AllowAction{}},
		VisibilityConfig: &wafv2types.VisibilityConfig{CloudWatchMetricsEnabled: false, MetricName: aws.String("edge"), SampledRequestsEnabled: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	aclARN := aws.ToString(acl.Summary.ARN)

	// A WebACL as ACK's wafv2-controller leaves it once synced; the
	// controller does not run here.
	ackACL := &ackwafv2.WebACL{
		ObjectMeta: metav1.ObjectMeta{Name: "edge", Namespace: "default"},
		Spec: ackwafv2.WebACLSpec{
			Name: aws.String("edge"), Scope: aws.String("REGIONAL"),
			DefaultAction:    &ackwafv2.DefaultAction{Allow: &ackwafv2.AllowAction{}},
			VisibilityConfig: &ackwafv2.VisibilityConfig{CloudWatchMetricsEnabled: aws.Bool(false), MetricName: aws.String("edge"), SampledRequestsEnabled: aws.Bool(false)},
		},
	}
	if err := env.Client.Create(ctx, ackACL); err != nil {
		t.Fatal(err)
	}
	arn := ackv1alpha1.AWSResourceName(aclARN)
	ackACL.Status.ACKResourceMetadata = &ackv1alpha1.ResourceMetadata{ARN: &arn}
	ackACL.Status.Conditions = []*ackv1alpha1.Condition{{Type: ackv1alpha1.ConditionTypeResourceSynced, Status: corev1.ConditionTrue}}
	if err := env.Client.Status().Update(ctx, ackACL); err != nil {
		t.Fatal(err)
	}

	// moto does not check that the resource exists; these are load balancer
	// ARNs as AWS writes them.
	const lb = "arn:aws:elasticloadbalancing:us-west-2:123456789012:loadbalancer/app/web/50dc6c495c0c9188"
	const lb2 = "arn:aws:elasticloadbalancing:us-west-2:123456789012:loadbalancer/app/admin/6d0ecf831eec9f09"

	waitSynced := func(t *testing.T, obj *wafv2v1.WebACLAssociation) *wafv2v1.WebACLAssociation {
		t.Helper()
		got := &wafv2v1.WebACLAssociation{}
		harness.Eventually(t, 60*time.Second, obj.Name+" synced", func() error {
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
				return err
			}
			if ok, why := synced(got.Status.Conditions); !ok {
				return fmt.Errorf("not synced: %s", why)
			}
			return nil
		})
		return got
	}
	gone := func(t *testing.T, obj client.Object) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.GetName()+" gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &wafv2v1.WebACLAssociation{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}

	web := &wafv2v1.WebACLAssociation{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: wafv2v1.WebACLAssociationSpec{
			ResourceARN: aws.String(lb),
			WebACLRef:   &ackv1alpha1.AWSResourceReferenceWrapper{From: &ackv1alpha1.AWSResourceReference{Name: aws.String("edge")}},
		},
	}

	t.Run("the referenced web ACL is associated and settles", func(t *testing.T) {
		if err := env.Client.Create(ctx, web); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t, web)
		// The runtime resolves webACLRef for each reconcile and does not
		// write the ARN into the stored spec; the cloud shows which web ACL
		// it associated.
		if aws.ToString(got.Status.WebACLName) != "edge" {
			t.Fatalf("status.webACLName %q", aws.ToString(got.Status.WebACLName))
		}
		if a, err := webACLOf(ctx, cloud, lb); err != nil || a != aclARN {
			t.Fatalf("cloud web ACL for the load balancer %q: %v", a, err)
		}
		updates := func() int {
			n := 0
			for _, line := range strings.Split(env.Logs(), "\n") {
				if strings.Contains(line, ">>> rm.sdkUpdate") && strings.Contains(line, `"name": "web"`) {
					n++
				}
			}
			return n
		}
		before := updates()
		time.Sleep(15 * time.Second)
		if n := updates(); n != before {
			t.Fatalf("updated %d times across resyncs", n-before)
		}
	})

	t.Run("the resource cannot change", func(t *testing.T) {
		obj := &wafv2v1.WebACLAssociation{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(web), obj); err != nil {
			t.Fatal(err)
		}
		obj.Spec.ResourceARN = aws.String(lb2)
		err := env.Client.Update(ctx, obj)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("changing resourceARN: got %v, want the CRD's immutability refusal", err)
		}
	})

	t.Run("a disassociation outside Kubernetes is put back", func(t *testing.T) {
		if _, err := cloud.DisassociateWebACL(ctx, &wafv2.DisassociateWebACLInput{ResourceArn: aws.String(lb)}); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "associated again", func() error {
			a, err := webACLOf(ctx, cloud, lb)
			if err != nil {
				return err
			}
			if a != aclARN {
				return fmt.Errorf("web ACL %q", a)
			}
			return nil
		})
	})

	t.Run("deletion-policy retain leaves the association", func(t *testing.T) {
		keep := &wafv2v1.WebACLAssociation{
			ObjectMeta: metav1.ObjectMeta{
				Name: "admin", Namespace: "default",
				Annotations: map[string]string{ackv1alpha1.AnnotationDeletionPolicy: string(ackv1alpha1.DeletionPolicyRetain)},
			},
			Spec: wafv2v1.WebACLAssociationSpec{ResourceARN: aws.String(lb2), WebACLARN: aws.String(aclARN)},
		}
		if err := env.Client.Create(ctx, keep); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, keep)
		if err := env.Client.Delete(ctx, keep); err != nil {
			t.Fatal(err)
		}
		gone(t, keep)
		if a, err := webACLOf(ctx, cloud, lb2); err != nil || a != aclARN {
			t.Fatalf("retained association: web ACL %q, %v", a, err)
		}
	})

	t.Run("delete disassociates", func(t *testing.T) {
		if err := env.Client.Delete(ctx, web); err != nil {
			t.Fatal(err)
		}
		gone(t, web)
		if a, err := webACLOf(ctx, cloud, lb); err != nil || a != "" {
			t.Fatalf("still associated with %q: %v", a, err)
		}
	})
}
