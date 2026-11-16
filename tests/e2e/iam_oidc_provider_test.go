// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	iamv1 "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

// oidcProviderByURL is the ARN of the account's OpenID Connect provider for
// url (with or without https://), "" when there is none.
func oidcProviderByURL(ctx context.Context, c *iam.Client, url string) (string, error) {
	out, err := c.ListOpenIDConnectProviders(ctx, &iam.ListOpenIDConnectProvidersInput{})
	if err != nil {
		return "", err
	}
	host := strings.TrimPrefix(url, "https://")
	for _, p := range out.OpenIDConnectProviderList {
		arn := aws.ToString(p.Arn)
		if strings.HasSuffix(arn, ":oidc-provider/"+host) {
			return arn, nil
		}
	}
	return "", nil
}

// OpenIDConnectProvider, our fixed copy of ACK's (docs/plan.md section 6.42):
// ACK's kind with urlRef, the URL read from ACK's eks Cluster's issuer
// (status.identity.oidc.issuer), for an EKS cluster's IRSA provider
// (the catalog's eks chart, enable_irsa). The Cluster is ACK's CRD
// (hack/ack-crds) with its status set by the test, as eks-controller would
// leave it; eks-controller does not run here.
func TestOpenIDConnectProvider(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = iamv1.AddToScheme(scheme)
	env := harness.Start(t, "iam", scheme)
	cloud := iam.NewFromConfig(env.AWS)
	ctx := context.Background()

	id := strings.ToUpper(strings.ReplaceAll(fmt.Sprintf("%x", time.Now().UnixNano()), "-", ""))
	// An issuer of EKS's form (https://oidc.eks.<region>.amazonaws.com/id/<id>)
	// on a host of its own: tests name no real AWS endpoint (task noaws).
	issuer := "https://oidc.ack-extensions.example/id/" + id
	t.Cleanup(func() {
		if arn, err := oidcProviderByURL(ctx, cloud, issuer); err == nil && arn != "" {
			_, _ = cloud.DeleteOpenIDConnectProvider(ctx, &iam.DeleteOpenIDConnectProviderInput{OpenIDConnectProviderArn: aws.String(arn)})
		}
	})

	cluster := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "eks.services.k8s.aws/v1alpha1",
		"kind":       "Cluster",
		"metadata":   map[string]any{"name": "main", "namespace": "default"},
		"spec": map[string]any{
			"name":               "main",
			"resourcesVPCConfig": map[string]any{"subnetIDs": []any{"subnet-0123456789abcdef0"}},
		},
	}}
	if err := env.Client.Create(ctx, cluster); err != nil {
		t.Fatal(err)
	}

	provider := &iamv1.OpenIDConnectProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "main-irsa", Namespace: "default"},
		Spec: iamv1.OpenIDConnectProviderSpec{
			URLRef:    &ackv1alpha1.AWSResourceReferenceWrapper{From: &ackv1alpha1.AWSResourceReference{Name: aws.String("main")}},
			ClientIDs: []*string{aws.String("sts.amazonaws.com")},
			// Given, so IAM need not fetch the issuer's certificate.
			Thumbprints: []*string{aws.String("9e99a48a9960b14926bb7f3b02e22da2b0ab7280")},
			Tags:        []*iamv1.Tag{{Key: aws.String("team"), Value: aws.String("platform")}},
		},
	}

	waitSynced := func(t *testing.T) *iamv1.OpenIDConnectProvider {
		t.Helper()
		got := &iamv1.OpenIDConnectProvider{}
		harness.Eventually(t, 60*time.Second, "provider synced", func() error {
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(provider), got); err != nil {
				return err
			}
			if ok, why := synced(got.Status.Conditions); !ok {
				return fmt.Errorf("not synced: %s", why)
			}
			return nil
		})
		return got
	}

	t.Run("nothing is made before the cluster has an issuer", func(t *testing.T) {
		if err := env.Client.Create(ctx, provider); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Second)
		if arn, err := oidcProviderByURL(ctx, cloud, issuer); err != nil || arn != "" {
			t.Fatalf("provider %q (%v) made before the cluster synced", arn, err)
		}
	})

	t.Run("the cluster's issuer becomes the provider", func(t *testing.T) {
		harness.Eventually(t, 10*time.Second, "cluster status", func() error {
			cur := &unstructured.Unstructured{}
			cur.SetGroupVersionKind(cluster.GroupVersionKind())
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(cluster), cur); err != nil {
				return err
			}
			_ = unstructured.SetNestedField(cur.Object, issuer, "status", "identity", "oidc", "issuer")
			_ = unstructured.SetNestedSlice(cur.Object, []any{
				map[string]any{"type": string(ackv1alpha1.ConditionTypeResourceSynced), "status": "True"},
			}, "status", "conditions")
			return env.Client.Status().Update(ctx, cur)
		})
		got := waitSynced(t)
		if got.Spec.URL != nil {
			t.Errorf("the stored spec holds the resolved url %q; it keeps the reference only", aws.ToString(got.Spec.URL))
		}
		arn, err := oidcProviderByURL(ctx, cloud, issuer)
		if err != nil || arn == "" {
			t.Fatalf("no provider for the cluster's issuer: %v", err)
		}
		if got.Status.ACKResourceMetadata == nil || string(*got.Status.ACKResourceMetadata.ARN) != arn {
			t.Errorf("status ARN %v, IAM's %s", got.Status.ACKResourceMetadata, arn)
		}
		out, err := cloud.GetOpenIDConnectProvider(ctx, &iam.GetOpenIDConnectProviderInput{OpenIDConnectProviderArn: aws.String(arn)})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(out.ClientIDList, ",") != "sts.amazonaws.com" {
			t.Errorf("client IDs %v", out.ClientIDList)
		}
		tagged := false
		for _, tg := range out.Tags {
			if aws.ToString(tg.Key) == "team" && aws.ToString(tg.Value) == "platform" {
				tagged = true
			}
		}
		if !tagged {
			t.Errorf("tags %v lack team=platform", out.Tags)
		}
	})

	t.Run("client IDs change in place", func(t *testing.T) {
		harness.Eventually(t, 30*time.Second, "update client IDs", func() error {
			cur := &iamv1.OpenIDConnectProvider{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(provider), cur); err != nil {
				return err
			}
			cur.Spec.ClientIDs = []*string{aws.String("sts.amazonaws.com"), aws.String("example-app")}
			return env.Client.Update(ctx, cur)
		})
		harness.Eventually(t, 60*time.Second, "client IDs in IAM", func() error {
			arn, err := oidcProviderByURL(ctx, cloud, issuer)
			if err != nil || arn == "" {
				return fmt.Errorf("no provider: %v", err)
			}
			out, err := cloud.GetOpenIDConnectProvider(ctx, &iam.GetOpenIDConnectProviderInput{OpenIDConnectProviderArn: aws.String(arn)})
			if err != nil {
				return err
			}
			ids := append([]string(nil), out.ClientIDList...)
			sort.Strings(ids)
			if strings.Join(ids, ",") != "example-app,sts.amazonaws.com" {
				return fmt.Errorf("client IDs %v", ids)
			}
			return nil
		})
	})

	t.Run("delete removes the provider", func(t *testing.T) {
		if err := env.Client.Delete(ctx, provider); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "provider gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(provider), &iamv1.OpenIDConnectProvider{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			if err := client.IgnoreNotFound(err); err != nil {
				return err
			}
			arn, err := oidcProviderByURL(ctx, cloud, issuer)
			if err != nil {
				return err
			}
			if arn != "" {
				return fmt.Errorf("IAM still has %s", arn)
			}
			return nil
		})
	})
}
