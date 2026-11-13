// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestEIPAssociation, EC2: an Elastic IP associated with an instance (the
// catalog's ec2-instance chart, create_eip), both named by reference to
// ACK's ElasticIPAddress and Instance (allocationRef, instanceRef). Nothing
// is sent while the referenced objects are not synced; once they are, the
// association is made in EC2 with the IDs from their status, and the
// object's status carries the association ID; a resync sends no update; a
// change to a create-only reference is refused by the CRD; the delete
// disassociates the address and leaves it allocated.
func TestEIPAssociation(t *testing.T) {
	e := start(t)
	c := ec2.NewFromConfig(e.moto)
	const typ = "AWS::EC2::EIPAssociation"

	vpc, err := c.CreateVpc(e.ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.41.0.0/16")})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := c.CreateSubnet(e.ctx, &ec2.CreateSubnetInput{VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.41.1.0/24")})
	if err != nil {
		t.Fatal(err)
	}
	// The image is one the backend has (moto's ids are not AWS's).
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

	// ACK's ElasticIPAddress and Instance, as stand-ins (TestReferences):
	// the manager reads only their status.
	eip := e.standIn(t, "ElasticIPAddress", "elasticipaddresses", "web-ip")
	inst := e.standIn(t, "Instance", "instances", "web")

	// Both an ID and its reference, or no target: refused by the CRD.
	both := objOf(typ, "both", map[string]any{"allocationRef": refTo("web-ip"), "allocationId": allocation, "instanceId": instance})
	if err := e.c.Create(e.ctx, both); err == nil || !strings.Contains(err.Error(), "cannot both be given") {
		t.Errorf("an ID with its reference: %v; want the CRD's refusal", err)
	}

	a := objOf(typ, "web-ip-on-web", map[string]any{"allocationRef": refTo("web-ip"), "instanceRef": refTo("web")})
	if err := e.c.Create(e.ctx, a.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "references not resolved", func() error {
		got := e.get(t, a)
		s, reason := conditionReason(got, ackv1alpha1.ConditionTypeReferencesResolved)
		if s != "Unknown" || !strings.Contains(reason, "not synced") {
			return fmt.Errorf("ACK.ReferencesResolved=%q: %s", s, reason)
		}
		return nil
	})
	if n := len(e.calls("CreateResource", typ)); n != 0 {
		t.Fatalf("%d CreateResource calls before the references resolved", n)
	}

	e.setSynced(t, eip, "allocationID", allocation)
	e.setSynced(t, inst, "instanceID", instance)
	got := e.waitSynced(t, a)
	if s, _ := condition(got, ackv1alpha1.ConditionTypeReferencesResolved); s != "True" {
		t.Errorf("ACK.ReferencesResolved=%q after the objects synced", s)
	}
	assoc, _, _ := unstructured.NestedString(got.Object, "status", "id")
	if !strings.HasPrefix(assoc, "eipassoc-") {
		t.Errorf("status.id %q: want the association ID (eipassoc-...)", assoc)
	}
	if id, _, _ := unstructured.NestedString(got.Object, "status", "cloudControl", "identifier"); id != assoc {
		t.Errorf("identifier %q, want the association ID %q", id, assoc)
	}
	address := func() (ec2types.Address, error) {
		out, err := c.DescribeAddresses(e.ctx, &ec2.DescribeAddressesInput{AllocationIds: []string{allocation}})
		if err != nil {
			return ec2types.Address{}, err
		}
		if len(out.Addresses) != 1 {
			return ec2types.Address{}, fmt.Errorf("%d addresses for %s", len(out.Addresses), allocation)
		}
		return out.Addresses[0], nil
	}
	addr, err := address()
	if err != nil {
		t.Fatal(err)
	}
	if aws.ToString(addr.InstanceId) != instance || aws.ToString(addr.AssociationId) != assoc {
		t.Errorf("in EC2 the address is on %q by %q; want %q by %q", aws.ToString(addr.InstanceId), aws.ToString(addr.AssociationId), instance, assoc)
	}
	spec, _, _ := unstructured.NestedMap(got.Object, "spec")
	for _, k := range []string{"allocationId", "instanceId"} {
		if _, ok := spec[k]; ok {
			t.Errorf("the object's spec %v holds the resolved %s; it keeps the reference only", spec, k)
		}
	}

	// A resync of the unchanged object sends no update.
	time.Sleep(3 * time.Second)
	if n := len(e.calls("UpdateResource", typ)); n != 0 {
		t.Errorf("%d UpdateResource calls for an unchanged object", n)
	}
	// The references are create-only, as the IDs are.
	if err := e.update(t, a, func(u *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(u.Object, "other", "spec", "instanceRef", "from", "name")
	}); err == nil || !strings.Contains(err.Error(), "create-only") {
		t.Errorf("a changed instanceRef: %v; want the CRD's refusal", err)
	}

	if err := e.c.Delete(e.ctx, a); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, a)
	addr, err = address()
	if err != nil {
		t.Fatalf("the address after the delete: %v (the delete disassociates, it does not release)", err)
	}
	if aws.ToString(addr.AssociationId) != "" || aws.ToString(addr.InstanceId) != "" {
		t.Errorf("after the delete the address is still on %q by %q", aws.ToString(addr.InstanceId), aws.ToString(addr.AssociationId))
	}
}
