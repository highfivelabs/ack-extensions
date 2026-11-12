// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
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

// ResourceTag: one tag on an EC2 resource made elsewhere, Terraform's
// aws_ec2_tag. Written by hand: CreateTags takes lists of resources and
// tags, not one tag.
func TestResourceTag(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = ec2v1.AddToScheme(scheme)
	env := harness.Start(t, "ec2", scheme)
	cloud := ec2.NewFromConfig(env.AWS)
	ctx := context.Background()

	// The VPC is made elsewhere (here, with the SDK), with a tag of its own
	// that no object manages.
	vpc, err := cloud.CreateVpc(ctx, &ec2.CreateVpcInput{
		CidrBlock: aws.String("10.40.0.0/16"),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeVpc,
			Tags:         []ec2types.Tag{{Key: aws.String("owner"), Value: aws.String("network-team")}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	vpcID := aws.ToString(vpc.Vpc.VpcId)

	cloudTags := func() map[string]string {
		t.Helper()
		out, err := cloud.DescribeTags(ctx, &ec2.DescribeTagsInput{Filters: []ec2types.Filter{{Name: aws.String("resource-id"), Values: []string{vpcID}}}})
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		for _, tg := range out.Tags {
			m[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
		}
		return m
	}
	waitSynced := func(t *testing.T, obj *ec2v1.ResourceTag) *ec2v1.ResourceTag {
		t.Helper()
		got := &ec2v1.ResourceTag{}
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
	waitValue := func(t *testing.T, key, want string) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, key+"="+want, func() error {
			if got, ok := cloudTags()[key]; !ok || got != want {
				return fmt.Errorf("%s is %q (set %v)", key, got, ok)
			}
			return nil
		})
	}
	gone := func(t *testing.T, obj *ec2v1.ResourceTag) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.Name+" gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &ec2v1.ResourceTag{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}
	newTag := func(name, key, value string, annotations map[string]string) *ec2v1.ResourceTag {
		return &ec2v1.ResourceTag{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Annotations: annotations},
			Spec:       ec2v1.ResourceTagSpec{ResourceID: aws.String(vpcID), Key: aws.String(key), Value: aws.String(value)},
		}
	}

	team := newTag("vpc-team", "team", "payments", nil)
	if err := env.Client.Create(ctx, team); err != nil {
		t.Fatal(err)
	}

	t.Run("create puts the tag and leaves the resource's others", func(t *testing.T) {
		got := waitSynced(t, team)
		tags := cloudTags()
		if tags["team"] != "payments" || tags["owner"] != "network-team" {
			t.Fatalf("tags %v", tags)
		}
		if aws.ToString(got.Status.ResourceType) != "vpc" {
			t.Fatalf("status.resourceType %q", aws.ToString(got.Status.ResourceType))
		}
	})

	t.Run("a changed value is put", func(t *testing.T) {
		harness.Eventually(t, 30*time.Second, "update", func() error {
			got := &ec2v1.ResourceTag{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(team), got); err != nil {
				return err
			}
			got.Spec.Value = aws.String("ledger")
			return env.Client.Update(ctx, got)
		})
		waitValue(t, "team", "ledger")
	})

	// The manager resyncs every 5 seconds (pkg/test/harness).
	t.Run("a value changed outside Kubernetes is put back", func(t *testing.T) {
		if _, err := cloud.CreateTags(ctx, &ec2.CreateTagsInput{Resources: []string{vpcID}, Tags: []ec2types.Tag{{Key: aws.String("team"), Value: aws.String("someone-else")}}}); err != nil {
			t.Fatal(err)
		}
		waitValue(t, "team", "ledger")
	})

	t.Run("a tag removed outside Kubernetes is put back", func(t *testing.T) {
		if _, err := cloud.DeleteTags(ctx, &ec2.DeleteTagsInput{Resources: []string{vpcID}, Tags: []ec2types.Tag{{Key: aws.String("team")}}}); err != nil {
			t.Fatal(err)
		}
		waitValue(t, "team", "ledger")
	})

	t.Run("an empty value is a value", func(t *testing.T) {
		flag := newTag("vpc-flag", "kubernetes.io/role/internal-elb", "", nil)
		if err := env.Client.Create(ctx, flag); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, flag)
		if v, ok := cloudTags()["kubernetes.io/role/internal-elb"]; !ok || v != "" {
			t.Fatalf("the empty-valued tag: %q %v", v, ok)
		}
		if err := env.Client.Delete(ctx, flag); err != nil {
			t.Fatal(err)
		}
		gone(t, flag)
	})

	t.Run("delete removes the tag only", func(t *testing.T) {
		if err := env.Client.Delete(ctx, team); err != nil {
			t.Fatal(err)
		}
		gone(t, team)
		tags := cloudTags()
		if _, ok := tags["team"]; ok {
			t.Fatalf("the tag is still there: %v", tags)
		}
		if tags["owner"] != "network-team" {
			t.Fatalf("the resource's own tag went too: %v", tags)
		}
	})

	t.Run("deletion-policy retain leaves the tag", func(t *testing.T) {
		keep := newTag("vpc-env", "env", "prod", map[string]string{ackv1alpha1.AnnotationDeletionPolicy: string(ackv1alpha1.DeletionPolicyRetain)})
		if err := env.Client.Create(ctx, keep); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, keep)
		if err := env.Client.Delete(ctx, keep); err != nil {
			t.Fatal(err)
		}
		gone(t, keep)
		if cloudTags()["env"] != "prod" {
			t.Fatalf("the retained tag went: %v", cloudTags())
		}
	})
}
