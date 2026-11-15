// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"testing"

	ackec2 "github.com/aws-controllers-k8s/ec2-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The references from our ec2 kinds to ACK's (open question 16, answered
// 2026-11-13): ACK's ec2-controller does not run here, so each test makes
// ACK's object as that controller would leave it once synced, its status
// carrying the ID a moto call returned.

// ackEC2Synced creates obj (one of ACK's ec2 kinds), then sets its status
// with setStatus and marks it synced, as ACK's controller would.
func ackEC2Synced(t *testing.T, ctx context.Context, c client.Client, obj client.Object, conds *[]*ackv1alpha1.Condition, setStatus func()) {
	t.Helper()
	if err := c.Create(ctx, obj); err != nil {
		t.Fatal(err)
	}
	setStatus()
	*conds = []*ackv1alpha1.Condition{{Type: ackv1alpha1.ConditionTypeResourceSynced, Status: corev1.ConditionTrue}}
	if err := c.Status().Update(ctx, obj); err != nil {
		t.Fatal(err)
	}
}

// ackVPC is ACK's VPC named name, synced, standing for the VPC with ID id.
func ackVPC(t *testing.T, ctx context.Context, c client.Client, name, cidr, id string) {
	t.Helper()
	obj := &ackec2.VPC{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       ackec2.VPCSpec{CIDRBlocks: []*string{aws.String(cidr)}},
	}
	ackEC2Synced(t, ctx, c, obj, &obj.Status.Conditions, func() { obj.Status.VPCID = aws.String(id) })
}

// ackInstance is ACK's Instance named name, synced, standing for the
// instance with ID id in Availability Zone az (spec.placement, which ACK's
// read fills from DescribeInstances).
func ackInstance(t *testing.T, ctx context.Context, c client.Client, name, id, az string) {
	t.Helper()
	obj := &ackec2.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       ackec2.InstanceSpec{Placement: &ackec2.Placement{AvailabilityZone: aws.String(az)}},
	}
	ackEC2Synced(t, ctx, c, obj, &obj.Status.Conditions, func() { obj.Status.InstanceID = aws.String(id) })
}

// ackTransitGateway is ACK's TransitGateway named name, synced, standing
// for the transit gateway with ID id.
func ackTransitGateway(t *testing.T, ctx context.Context, c client.Client, name, id string) {
	t.Helper()
	obj := &ackec2.TransitGateway{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
	ackEC2Synced(t, ctx, c, obj, &obj.Status.Conditions, func() { obj.Status.TransitGatewayID = aws.String(id) })
}
