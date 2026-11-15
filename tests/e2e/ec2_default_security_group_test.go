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
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	ec2v1 "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
)

// ruleStrings is a group's rules as "proto from-to cidr" strings, one per
// CIDR, so tests compare what EC2 holds whatever its grouping.
func ruleStrings(perms []ec2types.IpPermission) []string {
	var out []string
	for _, p := range perms {
		for _, r := range p.IpRanges {
			out = append(out, fmt.Sprintf("%s %d-%d %s", aws.ToString(p.IpProtocol), aws.ToInt32(p.FromPort), aws.ToInt32(p.ToPort), aws.ToString(r.CidrIp)))
		}
	}
	return out
}

// DefaultSecurityGroup: a VPC's default security group taken over by the
// VPC (Terraform's aws_default_security_group; open question 16), its
// rules and tags exclusive, never deleted. moto makes a default group
// with each VPC (with the default egress rule; without AWS's
// self-referencing ingress rule).
func TestDefaultSecurityGroup(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = ec2v1.AddToScheme(scheme)
	_ = ackec2.AddToScheme(scheme)
	env := harness.Start(t, "ec2", scheme)
	cloud := ec2.NewFromConfig(env.AWS)
	ctx := context.Background()

	v, err := cloud.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.50.0.0/16")})
	if err != nil {
		t.Fatal(err)
	}
	vpcID := aws.ToString(v.Vpc.VpcId)
	ackVPC(t, ctx, env.Client, "main", "10.50.0.0/16", vpcID)
	groups, err := cloud.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{Filters: []ec2types.Filter{
		{Name: aws.String("vpc-id"), Values: []string{vpcID}}, {Name: aws.String("group-name"), Values: []string{"default"}},
	}})
	if err != nil || len(groups.SecurityGroups) != 1 {
		t.Fatalf("the VPC's default group: %v %v", groups, err)
	}
	groupID := aws.ToString(groups.SecurityGroups[0].GroupId)
	cloudGroup := func() ec2types.SecurityGroup {
		t.Helper()
		out, err := cloud.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{groupID}})
		if err != nil || len(out.SecurityGroups) != 1 {
			t.Fatalf("describe %s: %v", groupID, err)
		}
		return out.SecurityGroups[0]
	}
	waitRules := func(t *testing.T, what string, ingress, egress string) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, what, func() error {
			g := cloudGroup()
			in, eg := strings.Join(ruleStrings(g.IpPermissions), ","), strings.Join(ruleStrings(g.IpPermissionsEgress), ",")
			if in != ingress || eg != egress {
				return fmt.Errorf("ingress %q egress %q", in, eg)
			}
			return nil
		})
	}
	waitSynced := func(t *testing.T, obj *ec2v1.DefaultSecurityGroup) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.Name+" synced", func() error {
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
				return err
			}
			if ok, why := synced(obj.Status.Conditions); !ok {
				return fmt.Errorf("not synced: %s", why)
			}
			return nil
		})
	}
	tcp := func(port int64, cidr string) *ackec2.IPPermission {
		return &ackec2.IPPermission{IPProtocol: aws.String("tcp"), FromPort: aws.Int64(port), ToPort: aws.Int64(port), IPRanges: []*ackec2.IPRange{{CIDRIP: aws.String(cidr)}}}
	}

	dsg := &ec2v1.DefaultSecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "main-default", Namespace: "default"},
		Spec: ec2v1.DefaultSecurityGroupSpec{
			VPCRef:       &ackv1alpha1.AWSResourceReferenceWrapper{From: &ackv1alpha1.AWSResourceReference{Name: aws.String("main")}},
			IngressRules: []*ackec2.IPPermission{tcp(443, "10.0.0.0/8")},
			Tags:         []*ec2v1.Tag{{Key: aws.String("team"), Value: aws.String("net")}},
		},
	}

	t.Run("the VPC's default group is taken over: its rules and tags become the spec's", func(t *testing.T) {
		if err := env.Client.Create(ctx, dsg); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, dsg)
		if aws.ToString(dsg.Status.ID) != groupID || aws.ToString(dsg.Status.GroupName) != "default" {
			t.Fatalf("status id %q name %q, want %q default", aws.ToString(dsg.Status.ID), aws.ToString(dsg.Status.GroupName), groupID)
		}
		// The default egress rule (all traffic to 0.0.0.0/0) is revoked:
		// the spec lists no egress rule.
		waitRules(t, "the spec's rules only", "tcp 443-443 10.0.0.0/8", "")
		if ec2TagMap(cloudGroup().Tags)["team"] != "net" {
			t.Fatalf("cloud tags %v", ec2TagMap(cloudGroup().Tags))
		}
	})

	t.Run("it settles: no update across resyncs", func(t *testing.T) {
		updates := func() int {
			n := 0
			for _, line := range strings.Split(env.Logs(), "\n") {
				if strings.Contains(line, ">>> rm.sdkUpdate") && strings.Contains(line, `"name": "main-default"`) {
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

	t.Run("a rule added outside Kubernetes is revoked", func(t *testing.T) {
		if _, err := cloud.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
			GroupId: aws.String(groupID),
			IpPermissions: []ec2types.IpPermission{{IpProtocol: aws.String("tcp"), FromPort: aws.Int32(22), ToPort: aws.Int32(22),
				IpRanges: []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}}}},
		}); err != nil {
			t.Fatal(err)
		}
		waitRules(t, "the outside rule revoked", "tcp 443-443 10.0.0.0/8", "")
	})

	t.Run("its rules and tags change", func(t *testing.T) {
		harness.Eventually(t, 30*time.Second, "spec updated", func() error {
			obj := &ec2v1.DefaultSecurityGroup{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(dsg), obj); err != nil {
				return err
			}
			obj.Spec.EgressRules = []*ackec2.IPPermission{tcp(443, "0.0.0.0/0")}
			obj.Spec.Tags = []*ec2v1.Tag{{Key: aws.String("team"), Value: aws.String("platform")}}
			return env.Client.Update(ctx, obj)
		})
		waitRules(t, "the egress rule authorized", "tcp 443-443 10.0.0.0/8", "tcp 443-443 0.0.0.0/0")
		harness.Eventually(t, 60*time.Second, "the new tag", func() error {
			if got := ec2TagMap(cloudGroup().Tags)["team"]; got != "platform" {
				return fmt.Errorf("team=%q", got)
			}
			return nil
		})
	})

	t.Run("vpcID and vpcRef: exactly one", func(t *testing.T) {
		both := &ec2v1.DefaultSecurityGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "both", Namespace: "default"},
			Spec:       ec2v1.DefaultSecurityGroupSpec{VPCID: aws.String(vpcID), VPCRef: dsg.Spec.VPCRef},
		}
		if err := env.Client.Create(ctx, both); err == nil || !strings.Contains(err.Error(), "exactly one of vpcID and vpcRef") {
			t.Fatalf("both: got %v, want the CRD's refusal", err)
		}
		neither := &ec2v1.DefaultSecurityGroup{ObjectMeta: metav1.ObjectMeta{Name: "neither", Namespace: "default"}}
		if err := env.Client.Create(ctx, neither); err == nil || !strings.Contains(err.Error(), "exactly one of vpcID and vpcRef") {
			t.Fatalf("neither: got %v, want the CRD's refusal", err)
		}
	})

	t.Run("delete leaves the group and its rules", func(t *testing.T) {
		if err := env.Client.Delete(ctx, dsg); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "object gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(dsg), &ec2v1.DefaultSecurityGroup{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
		waitRules(t, "the rules kept", "tcp 443-443 10.0.0.0/8", "tcp 443-443 0.0.0.0/0")
	})
}
