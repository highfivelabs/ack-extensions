// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	ackec2 "github.com/aws-controllers-k8s/ec2-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	ec2v1 "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
)

// LaunchTemplate, our fixed copy of ACK's (docs/plan.md section 6.43):
// ACK's kind with ec2-controller pull request #364's references under
// spec.data, so a node group's custom launch template names its security
// group by reference (the catalog's eks chart). ACK's SecurityGroup is made
// as ACK's controller would leave it once synced; it does not run here.
func TestLaunchTemplate(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = ec2v1.AddToScheme(scheme)
	_ = ackec2.AddToScheme(scheme)
	env := harness.Start(t, "ec2", scheme)
	cloud := ec2.NewFromConfig(env.AWS)
	ctx := context.Background()

	vpc, err := cloud.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.61.0.0/16")})
	if err != nil {
		t.Fatal(err)
	}
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	cleanupLog(t, "vpc "+vpcID, func() error {
		_, err := cloud.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: aws.String(vpcID)})
		return err
	})
	sg, err := cloud.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName: aws.String("ackx-lt-nodes"), Description: aws.String("nodes"), VpcId: aws.String(vpcID),
	})
	if err != nil {
		t.Fatal(err)
	}
	sgID := aws.ToString(sg.GroupId)
	cleanupLog(t, "security group "+sgID, func() error {
		_, err := cloud.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: aws.String(sgID)})
		return err
	})

	name := "ackx-lt-nodes"
	lt := &ec2v1.LaunchTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "nodes", Namespace: "default"},
		Spec: ec2v1.LaunchTemplateSpec{
			Name: aws.String(name),
			Data: &ec2v1.RequestLaunchTemplateData{
				InstanceType: aws.String("t3.micro"),
				SecurityGroupRefs: []*ackv1alpha1.AWSResourceReferenceWrapper{
					{From: &ackv1alpha1.AWSResourceReference{Name: aws.String("nodes")}},
				},
			},
		},
	}
	versions := func() ([]string, int, error) {
		out, err := cloud.DescribeLaunchTemplateVersions(ctx, &ec2.DescribeLaunchTemplateVersionsInput{LaunchTemplateName: aws.String(name)})
		if err != nil {
			return nil, 0, err
		}
		if len(out.LaunchTemplateVersions) == 0 {
			return nil, 0, fmt.Errorf("no versions")
		}
		latest := out.LaunchTemplateVersions[0]
		for _, v := range out.LaunchTemplateVersions {
			if aws.ToInt64(v.VersionNumber) > aws.ToInt64(latest.VersionNumber) {
				latest = v
			}
		}
		var ids []string
		if latest.LaunchTemplateData != nil {
			ids = latest.LaunchTemplateData.SecurityGroupIds
		}
		return ids, len(out.LaunchTemplateVersions), nil
	}

	t.Run("nothing is made before the security group syncs", func(t *testing.T) {
		if err := env.Client.Create(ctx, lt); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Second)
		if _, _, err := versions(); err == nil {
			t.Fatalf("launch template %s made before the SecurityGroup synced", name)
		}
	})

	t.Run("the referenced group is the template's", func(t *testing.T) {
		obj := &ackec2.SecurityGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "nodes", Namespace: "default"},
			Spec:       ackec2.SecurityGroupSpec{Name: aws.String("nodes"), Description: aws.String("nodes")},
		}
		ackEC2Synced(t, ctx, env.Client, obj, &obj.Status.Conditions, func() { obj.Status.ID = aws.String(sgID) })
		got := &ec2v1.LaunchTemplate{}
		harness.Eventually(t, 60*time.Second, "launch template synced", func() error {
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(lt), got); err != nil {
				return err
			}
			if ok, why := synced(got.Status.Conditions); !ok {
				return fmt.Errorf("not synced: %s", why)
			}
			return nil
		})
		// moto copies CreateLaunchTemplate's TagSpecifications (the
		// template's own tags, ACK's default tags among them) into the first
		// version's data, which EC2 does not; ACK's read then sees a data
		// difference and makes a second version, once. So the latest version
		// is checked, and the count only for growth (below).
		ids, _, err := versions()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(ids, ",") != sgID {
			t.Fatalf("latest version's security groups %v, want [%s]", ids, sgID)
		}
		if got.Spec.Data == nil || len(got.Spec.Data.SecurityGroupRefs) != 1 {
			t.Fatalf("the stored spec lost securityGroupRefs: %+v", got.Spec.Data)
		}
		if len(got.Spec.Data.SecurityGroupIDs) != 0 {
			t.Errorf("the stored spec holds the resolved securityGroupIDs %v; it keeps the references only", aws.ToStringSlice(got.Spec.Data.SecurityGroupIDs))
		}
	})

	t.Run("resyncs make no new version and keep the reference", func(t *testing.T) {
		time.Sleep(10 * time.Second) // moto's one extra version (above) settles
		_, before, err := versions()
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(15 * time.Second)
		ids, n, err := versions()
		if err != nil {
			t.Fatal(err)
		}
		if n != before || before > 2 {
			t.Fatalf("%d versions after resyncs of an unchanged template (%d before), want no growth", n, before)
		}
		if strings.Join(ids, ",") != sgID {
			t.Fatalf("latest version's security groups %v after resyncs, want [%s]", ids, sgID)
		}
		got := &ec2v1.LaunchTemplate{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(lt), got); err != nil {
			t.Fatal(err)
		}
		if got.Spec.Data == nil || len(got.Spec.Data.SecurityGroupRefs) != 1 {
			t.Fatalf("securityGroupRefs gone after resyncs: %+v", got.Spec.Data)
		}
	})

	t.Run("delete removes the template", func(t *testing.T) {
		if err := env.Client.Delete(ctx, lt); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "launch template gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(lt), &ec2v1.LaunchTemplate{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			if err := client.IgnoreNotFound(err); err != nil {
				return err
			}
			if _, _, err := versions(); err == nil {
				return fmt.Errorf("launch template %s still in EC2", name)
			}
			return nil
		})
	})
}
