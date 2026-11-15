// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/highfivelabs/ack-extensions/pkg/test/awsemulator"
)

// TestOnlySomeCRDsInstalled (RECCC, docs/cloudcontrol.md section 11): a
// cluster with two Cloud Control CRDs installed, Secrets Manager's
// ResourcePolicy and EC2's EIPAssociation, and the manager started as the
// operator starts it for a context that names no kinds. The catalog's
// fake-AWS run is such a cluster: there the manager started controllers for
// every kind in the registry, the ones with no CRD never synced their
// caches, and after controller-runtime's two-minute cache sync timeout the
// manager exited, so each object kept its first reconcile ("Unable to
// determine if desired resource state matches latest observed state") or
// none at all.
//
// Both objects sync (ACK.ResourceSynced and Ready True), the manager says
// which kinds it does not serve, and after the cache sync timeout it still
// reconciles: a policy changed then reaches Secrets Manager, and the
// association is deleted from EC2.
func TestOnlySomeCRDsInstalled(t *testing.T) {
	if target(t) == "real" {
		t.Skip("a cluster shape, not a service: the fake is enough")
	}
	if awsemulator.Selected(t) {
		// The emulator's Cloud Control reaches neither Secrets Manager nor
		// EC2 (docs/aws-emulator.md), and the test reads both.
		t.Skip("reads Secrets Manager and EC2, which the emulator's Cloud Control does not reach")
	}
	e := startWithOptions(t, startOptions{crds: []string{
		"secretsmanager.cloudcontrol.aws.hfl.dev_resourcepolicies.yaml",
		"ec2.cloudcontrol.aws.hfl.dev_eipassociations.yaml",
	}})
	const (
		policyType = "AWS::SecretsManager::ResourcePolicy"
		eipType    = "AWS::EC2::EIPAssociation"
	)
	sm := secretsmanager.NewFromConfig(e.moto)
	c := ec2.NewFromConfig(e.moto)
	reader := fmt.Sprintf("arn:aws:iam::%s:role/reader", e.account)
	auditor := fmt.Sprintf("arn:aws:iam::%s:role/auditor", e.account)

	s, err := sm.CreateSecret(e.ctx, &secretsmanager.CreateSecretInput{Name: aws.String("subset-secret"), SecretString: aws.String("not-a-real-password")})
	if err != nil {
		t.Fatal(err)
	}
	arn := aws.ToString(s.ARN)
	t.Cleanup(func() {
		_, _ = sm.DeleteSecret(e.ctx, &secretsmanager.DeleteSecretInput{SecretId: aws.String(arn), ForceDeleteWithoutRecovery: aws.Bool(true)})
	})
	policy := objOf(policyType, "subset-policy", map[string]any{"secretId": arn, "resourcePolicy": secretPolicy(reader)})
	if err := e.c.Create(e.ctx, policy); err != nil {
		t.Fatal(err)
	}

	vpc, err := c.CreateVpc(e.ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.42.0.0/16")})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := c.CreateSubnet(e.ctx, &ec2.CreateSubnetInput{VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.42.1.0/24")})
	if err != nil {
		t.Fatal(err)
	}
	images, err := c.DescribeImages(e.ctx, &ec2.DescribeImagesInput{Owners: []string{"amazon"}})
	if err != nil || len(images.Images) == 0 {
		t.Fatalf("no image in the fake cloud: %v", err)
	}
	run, err := c.RunInstances(e.ctx, &ec2.RunInstancesInput{
		ImageId: images.Images[0].ImageId, InstanceType: ec2types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: sub.Subnet.SubnetId,
	})
	if err != nil {
		t.Fatal(err)
	}
	instance := aws.ToString(run.Instances[0].InstanceId)
	alloc, err := c.AllocateAddress(e.ctx, &ec2.AllocateAddressInput{Domain: ec2types.DomainTypeVpc})
	if err != nil {
		t.Fatal(err)
	}
	allocation := aws.ToString(alloc.AllocationId)
	assoc := objOf(eipType, "subset-eip", map[string]any{"allocationId": allocation, "instanceId": instance})
	if err := e.c.Create(e.ctx, assoc); err != nil {
		t.Fatal(err)
	}

	e.waitSynced(t, policy)
	got := e.waitSynced(t, assoc)
	if id, _, _ := unstructured.NestedString(got.Object, "status", "id"); id == "" {
		t.Error("the association has no status.id")
	}
	if !strings.Contains(e.logs.String(), "not serving the kinds with no CRD installed") {
		t.Error("the manager did not say it leaves out the kinds with no CRD")
	}

	// Past controller-runtime's cache sync timeout (two minutes from the
	// manager's start): before the fix the manager had exited by now.
	time.Sleep(2*time.Minute + 15*time.Second)
	if strings.Contains(e.logs.String(), "timed out waiting for cache to be synced") {
		t.Fatal("a controller's cache never synced: the manager serves a kind with no CRD")
	}
	if err := e.update(t, policy, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, secretPolicy(auditor), "spec", "resourcePolicy")
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "the changed policy in Secrets Manager", func() error {
		out, err := sm.GetResourcePolicy(e.ctx, &secretsmanager.GetResourcePolicyInput{SecretId: aws.String(arn)})
		if err != nil {
			return err
		}
		if p := aws.ToString(out.ResourcePolicy); !strings.Contains(p, auditor) {
			return fmt.Errorf("%s", p)
		}
		return nil
	})
	e.waitSynced(t, policy)

	if err := e.c.Delete(e.ctx, assoc); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "the association gone from EC2", func() error {
		out, err := c.DescribeAddresses(e.ctx, &ec2.DescribeAddressesInput{AllocationIds: []string{allocation}})
		if err != nil {
			return err
		}
		if len(out.Addresses) == 1 && aws.ToString(out.Addresses[0].AssociationId) != "" {
			return fmt.Errorf("still associated: %s", aws.ToString(out.Addresses[0].AssociationId))
		}
		return nil
	})
}
