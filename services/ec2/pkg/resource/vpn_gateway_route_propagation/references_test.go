// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package vpn_gateway_route_propagation

// routeTableRef names ACK's RouteTable (ec2.services.k8s.aws; open question
// 16) and gatewayRef our VPNGateway. moto has no EnableVgwRoutePropagation,
// so the kind has no end-to-end test; the generated resolution is checked
// here against a Kubernetes client holding the referenced objects.

import (
	"context"
	"errors"
	"testing"

	ackec2 "github.com/aws-controllers-k8s/ec2-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
)

func syncedConds() []*ackv1alpha1.Condition {
	return []*ackv1alpha1.Condition{{Type: ackv1alpha1.ConditionTypeResourceSynced, Status: corev1.ConditionTrue}}
}

func refTo(name string) *ackv1alpha1.AWSResourceReferenceWrapper {
	return &ackv1alpha1.AWSResourceReferenceWrapper{From: &ackv1alpha1.AWSResourceReference{Name: aws.String(name)}}
}

func referenceClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := svcapitypes.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := ackec2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func TestRouteTableRefResolvesACKsRouteTable(t *testing.T) {
	table := &ackec2.RouteTable{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "app"}}
	table.Status.RouteTableID = aws.String("rtb-0private")
	table.Status.Conditions = syncedConds()
	gw := &svcapitypes.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "hub", Namespace: "app"}}
	gw.Status.VPNGatewayID = aws.String("vgw-0hub")
	gw.Status.Conditions = syncedConds()
	c := referenceClient(t, table, gw)

	ko := &svcapitypes.VPNGatewayRoutePropagation{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "app"}}
	ko.Spec.RouteTableRef, ko.Spec.GatewayRef = refTo("private"), refTo("hub")
	rm := &resourceManager{}
	got, has, err := rm.ResolveReferences(context.Background(), c, &resource{ko})
	if err != nil || !has {
		t.Fatalf("resolve: has %v, err %v", has, err)
	}
	spec := got.(*resource).ko.Spec
	if aws.ToString(spec.RouteTableID) != "rtb-0private" || aws.ToString(spec.GatewayID) != "vgw-0hub" {
		t.Fatalf("resolved table %q gateway %q", aws.ToString(spec.RouteTableID), aws.ToString(spec.GatewayID))
	}
	// The resolved IDs are not kept in the stored spec.
	cleared := rm.ClearResolvedReferences(got).(*resource).ko.Spec
	if cleared.RouteTableID != nil || cleared.GatewayID != nil {
		t.Fatalf("cleared spec keeps table %q gateway %q", aws.ToString(cleared.RouteTableID), aws.ToString(cleared.GatewayID))
	}
}

func TestRouteTableRefWaitsAndRefuses(t *testing.T) {
	notSynced := &ackec2.RouteTable{ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: "app"}}
	ready := &ackec2.RouteTable{ObjectMeta: metav1.ObjectMeta{Name: "ready", Namespace: "app"}}
	ready.Status.RouteTableID = aws.String("rtb-0ready")
	ready.Status.Conditions = syncedConds()
	c := referenceClient(t, notSynced, ready)
	rm := &resourceManager{}

	ko := &svcapitypes.VPNGatewayRoutePropagation{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "app"}}
	ko.Spec.GatewayID, ko.Spec.RouteTableRef = aws.String("vgw-0hub"), refTo("pending")
	if _, _, err := rm.ResolveReferences(context.Background(), c, &resource{ko}); !errors.Is(err, ackerr.ResourceReferenceNotSynced) {
		t.Fatalf("a table not synced: got %v, want ResourceReferenceNotSynced", err)
	}

	// ACK's generated resolution reports a reference that cannot resolve
	// first, so the refusal of both is checked with one that can.
	both := ko.DeepCopy()
	both.Spec.RouteTableRef, both.Spec.RouteTableID = refTo("ready"), aws.String("rtb-0x")
	if _, _, err := rm.ResolveReferences(context.Background(), c, &resource{both}); !errors.Is(err, ackerr.ResourceReferenceAndIDNotSupported) {
		t.Fatalf("routeTableID and routeTableRef: got %v, want ResourceReferenceAndIDNotSupported", err)
	}
}
