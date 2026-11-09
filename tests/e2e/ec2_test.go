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
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	ec2v1 "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
)

func cloudCustomerGateway(ctx context.Context, c *ec2.Client, id string) (*ec2types.CustomerGateway, error) {
	out, err := c.DescribeCustomerGateways(ctx, &ec2.DescribeCustomerGatewaysInput{CustomerGatewayIds: []string{id}})
	if err != nil {
		return nil, err
	}
	if len(out.CustomerGateways) != 1 {
		return nil, fmt.Errorf("%d customer gateways with ID %s", len(out.CustomerGateways), id)
	}
	return &out.CustomerGateways[0], nil
}

func ec2TagMap(tags []ec2types.Tag) map[string]string {
	m := map[string]string{}
	for _, t := range tags {
		m[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return m
}

// CustomerGateway: the on-premises end of a site-to-site VPN. ACK's
// ec2-controller (v1.23.1) ignores it.
func TestCustomerGateway(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = ec2v1.AddToScheme(scheme)
	env := harness.Start(t, "ec2", scheme)
	cloud := ec2.NewFromConfig(env.AWS)
	ctx := context.Background()

	waitSynced := func(t *testing.T, obj *ec2v1.CustomerGateway) *ec2v1.CustomerGateway {
		t.Helper()
		got := &ec2v1.CustomerGateway{}
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
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &ec2v1.CustomerGateway{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}

	office := &ec2v1.CustomerGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "office", Namespace: "default"},
		Spec: ec2v1.CustomerGatewaySpec{
			BGPASN: aws.Int64(65010), IPAddress: aws.String("203.0.113.12"), Type: aws.String("ipsec.1"),
			Tags: []*ec2v1.Tag{{Key: aws.String("site"), Value: aws.String("oslo")}},
		},
	}
	var id string

	t.Run("a customer gateway is made, tagged, and available", func(t *testing.T) {
		if err := env.Client.Create(ctx, office); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t, office)
		id = aws.ToString(got.Status.CustomerGatewayID)
		if !strings.HasPrefix(id, "cgw-") || aws.ToString(got.Status.State) != "available" {
			t.Fatalf("status id %q state %q", id, aws.ToString(got.Status.State))
		}
		cg, err := cloudCustomerGateway(ctx, cloud, id)
		if err != nil {
			t.Fatal(err)
		}
		if aws.ToString(cg.IpAddress) != "203.0.113.12" || aws.ToString(cg.BgpAsn) != "65010" || ec2TagMap(cg.Tags)["site"] != "oslo" {
			t.Fatalf("cloud gateway ip %q asn %q tags %v", aws.ToString(cg.IpAddress), aws.ToString(cg.BgpAsn), ec2TagMap(cg.Tags))
		}
	})

	t.Run("its tags change", func(t *testing.T) {
		obj := &ec2v1.CustomerGateway{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(office), obj); err != nil {
			t.Fatal(err)
		}
		obj.Spec.Tags = []*ec2v1.Tag{{Key: aws.String("team"), Value: aws.String("network")}}
		if err := env.Client.Update(ctx, obj); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "new tags in the cloud", func() error {
			cg, err := cloudCustomerGateway(ctx, cloud, id)
			if err != nil {
				return err
			}
			tags := ec2TagMap(cg.Tags)
			if tags["team"] != "network" || tags["site"] != "" {
				return fmt.Errorf("tags %v", tags)
			}
			return nil
		})
		waitSynced(t, office)
	})

	t.Run("its address cannot change", func(t *testing.T) {
		obj := &ec2v1.CustomerGateway{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(office), obj); err != nil {
			t.Fatal(err)
		}
		obj.Spec.IPAddress = aws.String("203.0.113.13")
		err := env.Client.Update(ctx, obj)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("changing ipAddress: got %v, want the CRD's immutability refusal", err)
		}
	})

	t.Run("deletion-policy retain leaves the gateway", func(t *testing.T) {
		keep := &ec2v1.CustomerGateway{
			ObjectMeta: metav1.ObjectMeta{
				Name: "branch", Namespace: "default",
				Annotations: map[string]string{ackv1alpha1.AnnotationDeletionPolicy: string(ackv1alpha1.DeletionPolicyRetain)},
			},
			Spec: ec2v1.CustomerGatewaySpec{BGPASN: aws.Int64(65020), IPAddress: aws.String("198.51.100.7"), Type: aws.String("ipsec.1")},
		}
		if err := env.Client.Create(ctx, keep); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t, keep)
		if err := env.Client.Delete(ctx, keep); err != nil {
			t.Fatal(err)
		}
		gone(t, keep)
		cg, err := cloudCustomerGateway(ctx, cloud, aws.ToString(got.Status.CustomerGatewayID))
		if err != nil || aws.ToString(cg.State) != "available" {
			t.Fatalf("retained gateway: %v %v", cg, err)
		}
	})

	t.Run("delete deletes the gateway", func(t *testing.T) {
		if err := env.Client.Delete(ctx, office); err != nil {
			t.Fatal(err)
		}
		gone(t, office)
		// EC2 keeps a deleted gateway listed for a while, in state deleted.
		cg, err := cloudCustomerGateway(ctx, cloud, id)
		if err != nil {
			t.Fatal(err)
		}
		if aws.ToString(cg.State) != "deleted" {
			t.Fatalf("cloud gateway state %q, want deleted", aws.ToString(cg.State))
		}
	})
}
