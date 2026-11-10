// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package vpn_connection

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	corev1 "k8s.io/api/core/v1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
)

func psk(name string) *ackv1alpha1.SecretKeyReference {
	return &ackv1alpha1.SecretKeyReference{SecretReference: corev1.SecretReference{Name: name}, Key: "psk"}
}

func TestReadOptionsKeepsOnlyWhatTheSpecSets(t *testing.T) {
	spec := &svcapitypes.VPNConnectionOptionsSpecification{
		StaticRoutesOnly:     aws.Bool(true),
		LocalIPv4NetworkCIDR: aws.String("10.0.0.0/16"),
		TunnelOptions: []*svcapitypes.VPNTunnelOptionsSpecification{
			{
				PreSharedKey:          psk("one"),
				Phase1LifetimeSeconds: aws.Int64(3600),
				IKEVersions:           []*svcapitypes.IKEVersionsRequestListValue{{Value: aws.String("ikev2")}},
			},
			{PreSharedKey: psk("two")},
		},
	}
	got := &svcsdktypes.VpnConnectionOptions{
		StaticRoutesOnly:      aws.Bool(true),
		LocalIpv4NetworkCidr:  aws.String("10.9.0.0/16"), // drifted
		RemoteIpv4NetworkCidr: aws.String("0.0.0.0/0"),   // EC2's default
		TunnelInsideIpVersion: svcsdktypes.TunnelInsideIpVersionIpv4,
		TunnelOptions: []svcsdktypes.TunnelOption{
			{
				OutsideIpAddress:      aws.String("198.51.100.1"),
				Phase1LifetimeSeconds: aws.Int32(28800), // drifted
				Phase2LifetimeSeconds: aws.Int32(3600),  // default
				IkeVersions:           []svcsdktypes.IKEVersionsListValue{{Value: aws.String("ikev1")}, {Value: aws.String("ikev2")}},
			},
			{OutsideIpAddress: aws.String("198.51.100.2"), DpdTimeoutSeconds: aws.Int32(30)},
		},
	}
	out, err := readOptions(spec, got)
	if err != nil {
		t.Fatal(err)
	}
	want := &svcapitypes.VPNConnectionOptionsSpecification{
		StaticRoutesOnly:     aws.Bool(true),
		LocalIPv4NetworkCIDR: aws.String("10.9.0.0/16"),
		TunnelOptions: []*svcapitypes.VPNTunnelOptionsSpecification{
			{
				PreSharedKey:          psk("one"),
				Phase1LifetimeSeconds: aws.Int64(28800),
				IKEVersions:           []*svcapitypes.IKEVersionsRequestListValue{{Value: aws.String("ikev1")}, {Value: aws.String("ikev2")}},
			},
			{PreSharedKey: psk("two")},
		},
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("readOptions:\n got %s\nwant %s", show(out), show(want))
	}
}

func TestReadOptionsWithNothingFromEC2IsTheSpec(t *testing.T) {
	spec := &svcapitypes.VPNConnectionOptionsSpecification{StaticRoutesOnly: aws.Bool(true)}
	if out, _ := readOptions(spec, nil); out != spec {
		t.Error("not the spec's")
	}
	if out, _ := readOptions(nil, &svcsdktypes.VpnConnectionOptions{}); out != nil {
		t.Error("options read for a spec without")
	}
}

func TestAWSOptionsOfClearsTheKeys(t *testing.T) {
	resp := &svcsdk.DescribeVpnConnectionsOutput{VpnConnections: []svcsdktypes.VpnConnection{{
		VpnConnectionId:              aws.String("vpn-1"),
		CustomerGatewayConfiguration: aws.String("<pre_shared_key>secret</pre_shared_key>"),
		Options: &svcsdktypes.VpnConnectionOptions{TunnelOptions: []svcsdktypes.TunnelOption{
			{PreSharedKey: aws.String("secret")}, {PreSharedKey: aws.String("secret")},
		}},
	}}}
	o := awsOptionsOf(resp, aws.String("vpn-1"))
	if o == nil || len(o.TunnelOptions) != 2 {
		t.Fatalf("options %+v", o)
	}
	if strings.Contains(show(resp), "secret") {
		t.Error("a key is left in the response")
	}
	if awsOptionsOf(resp, aws.String("vpn-2")) != nil {
		t.Error("options of another connection")
	}
}

func TestRouteChanges(t *testing.T) {
	s := func(v ...string) []*string {
		var out []*string
		for _, x := range v {
			out = append(out, aws.String(x))
		}
		return out
	}
	add, remove := routeChanges(s("10.1.0.0/16", "10.2.0.0/16", "10.1.0.0/16"), s("10.2.0.0/16", "10.3.0.0/16"))
	if !reflect.DeepEqual(add, []string{"10.1.0.0/16"}) || !reflect.DeepEqual(remove, []string{"10.3.0.0/16"}) {
		t.Errorf("add %v remove %v", add, remove)
	}
}

func TestStaticRoutesOfFollowsTheSpecsOrder(t *testing.T) {
	ko := &svcapitypes.VPNConnection{}
	ko.Spec.StaticRoutes = []*string{aws.String("10.2.0.0/16"), aws.String("10.1.0.0/16")}
	ko.Status.Routes = []*svcapitypes.VPNStaticRoute{
		{DestinationCIDRBlock: aws.String("10.1.0.0/16"), Source: aws.String("Static"), State: aws.String("available")},
		{DestinationCIDRBlock: aws.String("10.9.0.0/16"), Source: aws.String("Static"), State: aws.String("available")},
		{DestinationCIDRBlock: aws.String("10.2.0.0/16"), Source: aws.String("Static"), State: aws.String("pending")},
		{DestinationCIDRBlock: aws.String("10.4.0.0/16"), Source: aws.String("Static"), State: aws.String("deleted")},
		{DestinationCIDRBlock: aws.String("10.5.0.0/16"), Source: aws.String("Bgp")},
	}
	got := staticRoutesOf(ko)
	want := []*string{aws.String("10.2.0.0/16"), aws.String("10.1.0.0/16"), aws.String("10.9.0.0/16")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("staticRoutesOf %s", show(got))
	}
	if staticRoutesOf(&svcapitypes.VPNConnection{}) != nil {
		t.Error("routes from none")
	}
}

func show(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
