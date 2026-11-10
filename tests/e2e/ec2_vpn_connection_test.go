// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	ec2v1 "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
)

func cloudVpnConnection(ctx context.Context, c *ec2.Client, id string) (*ec2types.VpnConnection, error) {
	out, err := c.DescribeVpnConnections(ctx, &ec2.DescribeVpnConnectionsInput{VpnConnectionIds: []string{id}})
	if err != nil {
		return nil, err
	}
	if len(out.VpnConnections) != 1 {
		return nil, fmt.Errorf("%d VPN connections with ID %s", len(out.VpnConnections), id)
	}
	return &out.VpnConnections[0], nil
}

// VPNConnection: a site-to-site VPN connection between our CustomerGateway
// and our VPNGateway, its tunnels' pre-shared keys from a Secret. ACK's
// ec2-controller (v1.23.1) ignores VpnConnection. moto has no
// ModifyVpnTunnelOptions, ModifyVpnConnectionOptions or
// CreateVpnConnectionRoute, and returns no options: tunnel option changes
// and static routes are unit-tested only (docs/plan.md, the VPNConnection
// section).
func TestVPNConnection(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = ec2v1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	env := harness.Start(t, "ec2", scheme)
	cloud := ec2.NewFromConfig(env.AWS)
	ctx := context.Background()

	syncedObj := func(t *testing.T, obj client.Object, conds func() []*ackv1alpha1.Condition) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.GetName()+" synced", func() error {
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
				return err
			}
			if ok, why := synced(conds()); !ok {
				return fmt.Errorf("not synced: %s", why)
			}
			return nil
		})
	}
	logCount := func(name, what string) int {
		n := 0
		for _, line := range strings.Split(env.Logs(), "\n") {
			if strings.Contains(line, what) && strings.Contains(line, `"name": "`+name+`"`) {
				n++
			}
		}
		return n
	}

	const key1, key2 = "tunnel_one_key_7f3a9c", "tunnel_two_key_2b8e4d"
	keys := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "office-vpn-keys", Namespace: "default"},
		StringData: map[string]string{"tunnel1": key1, "tunnel2": key2},
	}
	office := &ec2v1.CustomerGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "office", Namespace: "default"},
		Spec:       ec2v1.CustomerGatewaySpec{BGPASN: aws.Int64(65010), IPAddress: aws.String("203.0.113.12"), Type: aws.String("ipsec.1")},
	}
	vgw := &ec2v1.VPNGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "hub", Namespace: "default"},
		Spec:       ec2v1.VPNGatewaySpec{Type: aws.String("ipsec.1")},
	}
	ref := func(name string) *ackv1alpha1.AWSResourceReferenceWrapper {
		return &ackv1alpha1.AWSResourceReferenceWrapper{From: &ackv1alpha1.AWSResourceReference{Name: aws.String(name)}}
	}
	secretKey := func(key string) *ackv1alpha1.SecretKeyReference {
		return &ackv1alpha1.SecretKeyReference{SecretReference: corev1.SecretReference{Name: "office-vpn-keys"}, Key: key}
	}
	conn := &ec2v1.VPNConnection{
		ObjectMeta: metav1.ObjectMeta{Name: "office-hub", Namespace: "default"},
		Spec: ec2v1.VPNConnectionSpec{
			Type:               aws.String("ipsec.1"),
			CustomerGatewayRef: ref("office"),
			VPNGatewayRef:      ref("hub"),
			Options: &ec2v1.VPNConnectionOptionsSpecification{
				StaticRoutesOnly: aws.Bool(true),
				TunnelOptions: []*ec2v1.VPNTunnelOptionsSpecification{
					{PreSharedKey: secretKey("tunnel1"), TunnelInsideCIDR: aws.String("169.254.10.0/30"), Phase1LifetimeSeconds: aws.Int64(3600)},
					{PreSharedKey: secretKey("tunnel2"), TunnelInsideCIDR: aws.String("169.254.11.0/30")},
				},
			},
			Tags: []*ec2v1.Tag{{Key: aws.String("site"), Value: aws.String("oslo")}},
		},
	}
	var id string

	t.Run("a connection is made between the referenced gateways, tagged, and available", func(t *testing.T) {
		for _, o := range []client.Object{keys, office, vgw, conn} {
			if err := env.Client.Create(ctx, o); err != nil {
				t.Fatal(err)
			}
		}
		syncedObj(t, office, func() []*ackv1alpha1.Condition { return office.Status.Conditions })
		syncedObj(t, vgw, func() []*ackv1alpha1.Condition { return vgw.Status.Conditions })
		syncedObj(t, conn, func() []*ackv1alpha1.Condition { return conn.Status.Conditions })
		id = aws.ToString(conn.Status.VPNConnectionID)
		if !strings.HasPrefix(id, "vpn-") || aws.ToString(conn.Status.State) != "available" {
			t.Fatalf("status id %q state %q", id, aws.ToString(conn.Status.State))
		}
		c, err := cloudVpnConnection(ctx, cloud, id)
		if err != nil {
			t.Fatal(err)
		}
		if aws.ToString(c.CustomerGatewayId) != aws.ToString(office.Status.CustomerGatewayID) ||
			aws.ToString(c.VpnGatewayId) != aws.ToString(vgw.Status.VPNGatewayID) ||
			string(c.Type) != "ipsec.1" || ec2TagMap(c.Tags)["site"] != "oslo" {
			t.Fatalf("cloud connection cgw %q vgw %q type %q tags %v", aws.ToString(c.CustomerGatewayId), aws.ToString(c.VpnGatewayId), c.Type, ec2TagMap(c.Tags))
		}
	})

	t.Run("it settles: no second create and no update across resyncs", func(t *testing.T) {
		creates := logCount("office-hub", ">>> rm.sdkCreate")
		updates := logCount("office-hub", ">>> rm.customUpdateVpnConnection")
		time.Sleep(15 * time.Second)
		if c, u := logCount("office-hub", ">>> rm.sdkCreate"), logCount("office-hub", ">>> rm.customUpdateVpnConnection"); c != creates || u != updates {
			t.Fatalf("across resyncs: %d more creates, %d more updates", c-creates, u-updates)
		}
		out, err := cloud.DescribeVpnConnections(ctx, &ec2.DescribeVpnConnectionsInput{})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.VpnConnections) != 1 {
			t.Fatalf("%d connections", len(out.VpnConnections))
		}
	})

	t.Run("the pre-shared keys are in no log, nothing of the object's and no event", func(t *testing.T) {
		got := &ec2v1.VPNConnection{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(conn), got); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		events := &corev1.EventList{}
		if err := env.Client.List(ctx, events, client.InNamespace("default")); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{key1, key2} {
			if strings.Contains(env.Logs(), k) {
				t.Fatal("a pre-shared key is in the manager's log")
			}
			if strings.Contains(string(raw), k) {
				t.Fatal("a pre-shared key is in the object")
			}
			for _, e := range events.Items {
				if strings.Contains(e.Message, k) {
					t.Fatalf("a pre-shared key is in event %s", e.Name)
				}
			}
		}
	})

	t.Run("its tags change", func(t *testing.T) {
		harness.Eventually(t, 30*time.Second, "tags updated", func() error {
			obj := &ec2v1.VPNConnection{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(conn), obj); err != nil {
				return err
			}
			obj.Spec.Tags = []*ec2v1.Tag{{Key: aws.String("team"), Value: aws.String("network")}}
			return env.Client.Update(ctx, obj)
		})
		harness.Eventually(t, 60*time.Second, "new tags in the cloud", func() error {
			c, err := cloudVpnConnection(ctx, cloud, id)
			if err != nil {
				return err
			}
			tags := ec2TagMap(c.Tags)
			if tags["team"] != "network" || tags["site"] != "" {
				return fmt.Errorf("tags %v", tags)
			}
			return nil
		})
		syncedObj(t, conn, func() []*ackv1alpha1.Condition { return conn.Status.Conditions })
	})

	t.Run("its gateways cannot change", func(t *testing.T) {
		harness.Eventually(t, 30*time.Second, "the CRD's immutability refusal", func() error {
			obj := &ec2v1.VPNConnection{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(conn), obj); err != nil {
				return err
			}
			obj.Spec.Type = aws.String("ipsec.2")
			err := env.Client.Update(ctx, obj)
			if err == nil {
				t.Fatal("type changed")
			}
			if !strings.Contains(err.Error(), "immutable") {
				return err
			}
			return nil
		})
	})

	t.Run("delete deletes the connection", func(t *testing.T) {
		if err := env.Client.Delete(ctx, conn); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "object gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(conn), &ec2v1.VPNConnection{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
		c, err := cloudVpnConnection(ctx, cloud, id)
		if err != nil {
			t.Fatal(err)
		}
		if c.State != ec2types.VpnStateDeleted {
			t.Fatalf("cloud state %q", c.State)
		}
		for _, o := range []client.Object{vgw, office} {
			if err := env.Client.Delete(ctx, o); err != nil {
				t.Fatal(err)
			}
		}
	})
}
