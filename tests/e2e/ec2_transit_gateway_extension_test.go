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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	transitgateway "github.com/highfivelabs/ack-extensions/services/ec2/pkg/extend/transit_gateway"
)

// The TransitGateway extension (docs/extensions.md): our ec2 manager, beside
// ACK's ec2-controller, sets the options ACK's TransitGateway has no field
// for, from annotations in our group, with ModifyTransitGateway.
//
// ACK's ec2-controller is a Go module here, but building its controller
// needs modules go.sum does not hold (its cloudwatchlogs-controller import),
// a new upstream dependency: so the test plays ACK's side, as
// ec2_ack_refs_test.go does for the references. It makes the gateway in
// the fake and ACK's TransitGateway with the status ACK writes once it
// has made it (status.transitGatewayID, status.ackResourceMetadata).
//
// moto 5.2.5 keeps ModifyTransitGateway's options as given, so the
// security group referencing support and the default route tables are
// shown here; it answers EncryptionSupport with an empty object (no
// state), so encryption in transit is shown by the unit tests of
// services/ec2/pkg/extend/transit_gateway only (docs/plan.md section 4.4).
func TestTransitGatewayExtension(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = ackec2.AddToScheme(scheme)
	env := harness.Start(t, "ec2", scheme)
	cloud := ec2.NewFromConfig(env.AWS)
	ctx := context.Background()

	// A transit gateway takes minutes to become available on real EC2;
	// harness.Eventually waits longer on a live run.
	const wait = 90 * time.Second

	// newGateway makes a transit gateway in the fake, two route tables of
	// it, and ACK's TransitGateway for it, annotated, as ACK leaves it
	// once synced.
	newGateway := func(t *testing.T, name string, opts *ec2types.TransitGatewayRequestOptions, annotations map[string]string) (id string, tables []string) {
		t.Helper()
		out, err := cloud.CreateTransitGateway(ctx, &ec2.CreateTransitGatewayInput{Description: aws.String(name), Options: opts})
		if err != nil {
			t.Fatal(err)
		}
		id = aws.ToString(out.TransitGateway.TransitGatewayId)
		t.Cleanup(func() {
			for _, rt := range tables {
				if _, err := cloud.DeleteTransitGatewayRouteTable(ctx, &ec2.DeleteTransitGatewayRouteTableInput{TransitGatewayRouteTableId: aws.String(rt)}); err != nil {
					t.Logf("cleanup: route table %s: %v", rt, err)
				}
			}
			// Real EC2 refuses the gateway's delete while a route table is
			// still being deleted: try again.
			deadline := time.Now().Add(time.Minute)
			if harness.IsLive() {
				deadline = time.Now().Add(15 * time.Minute)
			}
			for {
				_, err := cloud.DeleteTransitGateway(ctx, &ec2.DeleteTransitGatewayInput{TransitGatewayId: aws.String(id)})
				if err == nil || time.Now().After(deadline) {
					if err != nil {
						t.Logf("cleanup: transit gateway %s: %v", id, err)
					}
					return
				}
				time.Sleep(10 * time.Second)
			}
		})
		for i := 0; i < 2; i++ {
			rt, err := cloud.CreateTransitGatewayRouteTable(ctx, &ec2.CreateTransitGatewayRouteTableInput{TransitGatewayId: aws.String(id)})
			if err != nil {
				t.Fatal(err)
			}
			tables = append(tables, aws.ToString(rt.TransitGatewayRouteTable.TransitGatewayRouteTableId))
		}
		// The annotations may name the route tables: %0 and %1.
		ann := map[string]string{}
		for k, v := range annotations {
			ann[k] = strings.NewReplacer("%0", tables[0], "%1", tables[1]).Replace(v)
		}
		obj := &ackec2.TransitGateway{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Annotations: ann},
			Spec:       ackec2.TransitGatewaySpec{Description: aws.String(name)},
		}
		if err := env.Client.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
		region := ackv1alpha1.AWSRegion(harness.Region)
		account := ackv1alpha1.AWSAccountID(env.Account)
		obj.Status.TransitGatewayID = aws.String(id)
		obj.Status.ACKResourceMetadata = &ackv1alpha1.ResourceMetadata{Region: &region, OwnerAccountID: &account}
		obj.Status.Conditions = []*ackv1alpha1.Condition{{Type: ackv1alpha1.ConditionTypeResourceSynced, Status: corev1.ConditionTrue}}
		if err := env.Client.Status().Update(ctx, obj); err != nil {
			t.Fatal(err)
		}
		return id, tables
	}
	options := func(id string) (*ec2types.TransitGatewayOptions, error) {
		out, err := cloud.DescribeTransitGateways(ctx, &ec2.DescribeTransitGatewaysInput{TransitGatewayIds: []string{id}})
		if err != nil {
			return nil, err
		}
		if len(out.TransitGateways) != 1 || out.TransitGateways[0].Options == nil {
			return nil, fmt.Errorf("transit gateway %s: %+v", id, out.TransitGateways)
		}
		return out.TransitGateways[0].Options, nil
	}
	waitOptions := func(t *testing.T, id, what string, ok func(*ec2types.TransitGatewayOptions) bool) {
		t.Helper()
		harness.Eventually(t, wait, id+" "+what, func() error {
			o, err := options(id)
			if err != nil {
				return err
			}
			if !ok(o) {
				return fmt.Errorf("options %+v", *o)
			}
			return nil
		})
	}
	annotate := func(t *testing.T, name string, set map[string]string) {
		t.Helper()
		harness.Eventually(t, 30*time.Second, "annotate "+name, func() error {
			g := &ackec2.TransitGateway{}
			if err := env.Client.Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, g); err != nil {
				return err
			}
			if g.Annotations == nil {
				g.Annotations = map[string]string{}
			}
			for a, v := range set {
				if v == "" {
					delete(g.Annotations, a)
				} else {
					g.Annotations[a] = v
				}
			}
			return env.Client.Update(ctx, g)
		})
	}

	id, tables := newGateway(t, "hub", nil, map[string]string{
		transitgateway.AnnotationSecurityGroupReferencing:     "enable",
		transitgateway.AnnotationAssociationDefaultRouteTable: "%0",
		transitgateway.AnnotationPropagationDefaultRouteTable: "%0",
	})

	t.Run("the options are set once ACK has made the gateway", func(t *testing.T) {
		waitOptions(t, id, "set", func(o *ec2types.TransitGatewayOptions) bool {
			return o.SecurityGroupReferencingSupport == ec2types.SecurityGroupReferencingSupportValueEnable &&
				aws.ToString(o.AssociationDefaultRouteTableId) == tables[0] &&
				aws.ToString(o.PropagationDefaultRouteTableId) == tables[0]
		})
		msg := waitEvent(t, env, "default", "hub", transitgateway.ReasonOptionsSet)
		if !strings.Contains(msg, "securityGroupReferencingSupport enable") || !strings.Contains(msg, tables[0]) {
			t.Fatalf("event %q", msg)
		}
	})

	t.Run("a changed annotation is applied", func(t *testing.T) {
		annotate(t, "hub", map[string]string{transitgateway.AnnotationPropagationDefaultRouteTable: tables[1]})
		waitOptions(t, id, "propagation table changed", func(o *ec2types.TransitGatewayOptions) bool {
			return aws.ToString(o.PropagationDefaultRouteTableId) == tables[1] && aws.ToString(o.AssociationDefaultRouteTableId) == tables[0]
		})
	})

	t.Run("an option changed outside Kubernetes is put back", func(t *testing.T) {
		if _, err := cloud.ModifyTransitGateway(ctx, &ec2.ModifyTransitGatewayInput{
			TransitGatewayId: aws.String(id),
			Options:          &ec2types.ModifyTransitGatewayOptions{SecurityGroupReferencingSupport: ec2types.SecurityGroupReferencingSupportValueDisable},
		}); err != nil {
			t.Fatal(err)
		}
		// The harness gives the managers a five-second resync.
		waitOptions(t, id, "referencing enabled again", func(o *ec2types.TransitGatewayOptions) bool {
			return o.SecurityGroupReferencingSupport == ec2types.SecurityGroupReferencingSupportValueEnable
		})
	})

	t.Run("an annotation that is not valid is an Event, and the others still hold", func(t *testing.T) {
		annotate(t, "hub", map[string]string{transitgateway.AnnotationAssociationDefaultRouteTable: "rtb-0123"})
		msg := waitEvent(t, env, "default", "hub", transitgateway.ReasonInvalidAnnotation)
		if !strings.Contains(msg, "not a transit gateway route table ID") {
			t.Fatalf("event %q", msg)
		}
		o, err := options(id)
		if err != nil {
			t.Fatal(err)
		}
		if aws.ToString(o.AssociationDefaultRouteTableId) != tables[0] || o.SecurityGroupReferencingSupport != ec2types.SecurityGroupReferencingSupportValueEnable {
			t.Fatalf("options %+v", *o)
		}
	})

	t.Run("a default route table is refused as an Event while the default is off", func(t *testing.T) {
		otherID, otherTables := newGateway(t, "no-propagation",
			&ec2types.TransitGatewayRequestOptions{DefaultRouteTablePropagation: ec2types.DefaultRouteTablePropagationValueDisable},
			map[string]string{transitgateway.AnnotationPropagationDefaultRouteTable: "%1"})
		msg := waitEvent(t, env, "default", "no-propagation", transitgateway.ReasonOptionIgnored)
		if !strings.Contains(msg, transitgateway.AnnotationPropagationDefaultRouteTable) {
			t.Fatalf("event %q", msg)
		}
		o, err := options(otherID)
		if err != nil {
			t.Fatal(err)
		}
		if aws.ToString(o.PropagationDefaultRouteTableId) == otherTables[1] {
			t.Fatalf("propagation default route table set to %s", otherTables[1])
		}
	})

	t.Run("nothing is changed before ACK has made the gateway", func(t *testing.T) {
		out, err := cloud.CreateTransitGateway(ctx, &ec2.CreateTransitGatewayInput{Description: aws.String("unmade")})
		if err != nil {
			t.Fatal(err)
		}
		unmade := aws.ToString(out.TransitGateway.TransitGatewayId)
		t.Cleanup(func() {
			if _, err := cloud.DeleteTransitGateway(ctx, &ec2.DeleteTransitGatewayInput{TransitGatewayId: aws.String(unmade)}); err != nil {
				t.Logf("cleanup: transit gateway %s: %v", unmade, err)
			}
		})
		obj := &ackec2.TransitGateway{ObjectMeta: metav1.ObjectMeta{Name: "unmade", Namespace: "default", Annotations: map[string]string{
			transitgateway.AnnotationSecurityGroupReferencing: "enable",
		}}}
		if err := env.Client.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
		// Long enough for the manager to have seen it (several resyncs).
		time.Sleep(10 * time.Second)
		o, err := options(unmade)
		if err != nil {
			t.Fatal(err)
		}
		if o.SecurityGroupReferencingSupport == ec2types.SecurityGroupReferencingSupportValueEnable {
			t.Fatal("referencing support enabled on a gateway ACK's object does not name")
		}
	})
}
