// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"sort"
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
	ec2v1 "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
)

// The aws_default_* kinds after DefaultSecurityGroup (open question 16;
// ACKQ16-1, answered 2026-11-15): DefaultNetworkACL, DefaultRouteTable
// and DefaultVPC, each found (never made), exclusive, never deleted.

func defaultKindsEnv(t *testing.T) (*harness.Env, *ec2.Client) {
	scheme := runtime.NewScheme()
	_ = ec2v1.AddToScheme(scheme)
	_ = ackec2.AddToScheme(scheme)
	env := harness.Start(t, "ec2", scheme)
	return env, ec2.NewFromConfig(env.AWS)
}

// waitObjectSynced waits for obj to be synced, reading its conditions with
// conds.
func waitObjectSynced(t *testing.T, env *harness.Env, obj client.Object, conds func() []*ackv1alpha1.Condition) {
	t.Helper()
	harness.Eventually(t, 60*time.Second, obj.GetName()+" synced", func() error {
		if err := env.Client.Get(context.Background(), client.ObjectKeyFromObject(obj), obj); err != nil {
			return err
		}
		if ok, why := synced(conds()); !ok {
			return fmt.Errorf("not synced: %s", why)
		}
		return nil
	})
}

// updatesOf counts the manager's sdkUpdate calls for the object named name.
func updatesOf(env *harness.Env, name string) int {
	n := 0
	for _, line := range strings.Split(env.Logs(), "\n") {
		if strings.Contains(line, ">>> rm.sdkUpdate") && strings.Contains(line, fmt.Sprintf("%q: %q", "name", name)) {
			n++
		}
	}
	return n
}

// waitDeleted deletes obj and waits until it is gone.
func waitDeleted(t *testing.T, env *harness.Env, obj client.Object, empty client.Object) {
	t.Helper()
	if err := env.Client.Delete(context.Background(), obj); err != nil {
		t.Fatal(err)
	}
	harness.Eventually(t, 60*time.Second, "object gone", func() error {
		err := env.Client.Get(context.Background(), client.ObjectKeyFromObject(obj), empty)
		if err == nil {
			return fmt.Errorf("object still present")
		}
		return client.IgnoreNotFound(err)
	})
}

// aclEntryStrings is an ACL's entries but EC2's 32767 ones, as
// "in|out rule action proto cidr ports" strings, sorted.
func aclEntryStrings(entries []ec2types.NetworkAclEntry) string {
	var out []string
	for _, e := range entries {
		if aws.ToInt32(e.RuleNumber) == 32767 {
			continue
		}
		dir := "in"
		if aws.ToBool(e.Egress) {
			dir = "out"
		}
		ports := ""
		if e.PortRange != nil {
			ports = fmt.Sprintf(" %d-%d", aws.ToInt32(e.PortRange.From), aws.ToInt32(e.PortRange.To))
		}
		out = append(out, fmt.Sprintf("%s %d %s %s %s%s", dir, aws.ToInt32(e.RuleNumber), e.RuleAction, aws.ToString(e.Protocol), aws.ToString(e.CidrBlock), ports))
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// DefaultNetworkACL: a VPC's default network ACL taken over by vpcRef, its
// entries, subnets and tags made the spec's.
func TestDefaultNetworkACL(t *testing.T) {
	env, cloud := defaultKindsEnv(t)
	ctx := context.Background()

	v, err := cloud.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.60.0.0/16")})
	if err != nil {
		t.Fatal(err)
	}
	vpcID := aws.ToString(v.Vpc.VpcId)
	ackVPC(t, ctx, env.Client, "main", "10.60.0.0/16", vpcID)
	acls, err := cloud.DescribeNetworkAcls(ctx, &ec2.DescribeNetworkAclsInput{Filters: []ec2types.Filter{
		{Name: aws.String("vpc-id"), Values: []string{vpcID}}, {Name: aws.String("default"), Values: []string{"true"}},
	}})
	if err != nil || len(acls.NetworkAcls) != 1 {
		t.Fatalf("the VPC's default ACL: %v %v", acls, err)
	}
	aclID := aws.ToString(acls.NetworkAcls[0].NetworkAclId)
	// A second ACL, and a subnet on it: the spec moves it onto the default.
	other, err := cloud.CreateNetworkAcl(ctx, &ec2.CreateNetworkAclInput{VpcId: aws.String(vpcID)})
	if err != nil {
		t.Fatal(err)
	}
	subnetA, err := cloud.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: aws.String(vpcID), CidrBlock: aws.String("10.60.1.0/24")})
	if err != nil {
		t.Fatal(err)
	}
	subnetB, err := cloud.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: aws.String(vpcID), CidrBlock: aws.String("10.60.2.0/24")})
	if err != nil {
		t.Fatal(err)
	}
	idA, idB := aws.ToString(subnetA.Subnet.SubnetId), aws.ToString(subnetB.Subnet.SubnetId)
	moveTo := func(subnet, acl string) {
		t.Helper()
		out, err := cloud.DescribeNetworkAcls(ctx, &ec2.DescribeNetworkAclsInput{Filters: []ec2types.Filter{{Name: aws.String("association.subnet-id"), Values: []string{subnet}}}})
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range out.NetworkAcls {
			for _, as := range a.Associations {
				if aws.ToString(as.SubnetId) == subnet {
					if _, err := cloud.ReplaceNetworkAclAssociation(ctx, &ec2.ReplaceNetworkAclAssociationInput{AssociationId: as.NetworkAclAssociationId, NetworkAclId: aws.String(acl)}); err != nil {
						t.Fatal(err)
					}
					return
				}
			}
		}
		t.Fatalf("subnet %s has no association", subnet)
	}
	moveTo(idA, aws.ToString(other.NetworkAcl.NetworkAclId))
	subnetObj := &ackec2.Subnet{
		ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "default"},
		Spec:       ackec2.SubnetSpec{CIDRBlock: aws.String("10.60.1.0/24"), VPCID: aws.String(vpcID)},
	}
	ackEC2Synced(t, ctx, env.Client, subnetObj, &subnetObj.Status.Conditions, func() { subnetObj.Status.SubnetID = aws.String(idA) })

	cloudACL := func() ec2types.NetworkAcl {
		t.Helper()
		out, err := cloud.DescribeNetworkAcls(ctx, &ec2.DescribeNetworkAclsInput{NetworkAclIds: []string{aclID}})
		if err != nil || len(out.NetworkAcls) != 1 {
			t.Fatalf("describe %s: %v", aclID, err)
		}
		return out.NetworkAcls[0]
	}
	waitEntries := func(t *testing.T, what, want string) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, what, func() error {
			if got := aclEntryStrings(cloudACL().Entries); got != want {
				return fmt.Errorf("entries %q", got)
			}
			return nil
		})
	}
	entry := func(egress bool, rule int64, proto, action, cidr string, from, to int64) *ackec2.NetworkACLEntry {
		e := &ackec2.NetworkACLEntry{Egress: aws.Bool(egress), RuleNumber: aws.Int64(rule), Protocol: aws.String(proto), RuleAction: aws.String(action), CIDRBlock: aws.String(cidr)}
		if from != 0 || to != 0 {
			e.PortRange = &ackec2.PortRange{From: aws.Int64(from), To: aws.Int64(to)}
		}
		return e
	}

	obj := &ec2v1.DefaultNetworkACL{
		ObjectMeta: metav1.ObjectMeta{Name: "main-acl", Namespace: "default"},
		Spec: ec2v1.DefaultNetworkACLSpec{
			VPCRef:     &ackv1alpha1.AWSResourceReferenceWrapper{From: &ackv1alpha1.AWSResourceReference{Name: aws.String("main")}},
			Entries:    []*ackec2.NetworkACLEntry{entry(false, 100, "6", "allow", "10.0.0.0/8", 443, 443)},
			SubnetRefs: []*ackv1alpha1.AWSResourceReferenceWrapper{{From: &ackv1alpha1.AWSResourceReference{Name: aws.String("a")}}},
			Tags:       []*ec2v1.Tag{{Key: aws.String("team"), Value: aws.String("net")}},
		},
	}
	conds := func() []*ackv1alpha1.Condition { return obj.Status.Conditions }

	t.Run("the default ACL is taken over: entries, subnets and tags become the spec's", func(t *testing.T) {
		if err := env.Client.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
		waitObjectSynced(t, env, obj, conds)
		if aws.ToString(obj.Status.ID) != aclID {
			t.Fatalf("status.id %q, want %q", aws.ToString(obj.Status.ID), aclID)
		}
		// EC2's default allow-all entries (100 in and out) are gone; the
		// spec's one entry is there.
		waitEntries(t, "the spec's entries only", "in 100 allow 6 10.0.0.0/8 443-443")
		harness.Eventually(t, 60*time.Second, "subnet a moved onto the default ACL", func() error {
			for _, as := range cloudACL().Associations {
				if aws.ToString(as.SubnetId) == idA {
					return nil
				}
			}
			return fmt.Errorf("associations %v", cloudACL().Associations)
		})
		if ec2TagMap(cloudACL().Tags)["team"] != "net" {
			t.Fatalf("cloud tags %v", ec2TagMap(cloudACL().Tags))
		}
		// Subnet b is on the default ACL and not listed: reported.
		harness.Eventually(t, 30*time.Second, "subnet b reported unlisted", func() error {
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
				return err
			}
			if got := aws.ToStringSlice(obj.Status.UnlistedSubnetIDs); len(got) != 1 || got[0] != idB {
				return fmt.Errorf("unlistedSubnetIDs %v, want [%s]", got, idB)
			}
			return nil
		})
	})

	t.Run("it settles: no update across resyncs", func(t *testing.T) {
		before := updatesOf(env, "main-acl")
		time.Sleep(15 * time.Second)
		if n := updatesOf(env, "main-acl"); n != before {
			t.Fatalf("updated %d times across resyncs", n-before)
		}
	})

	t.Run("an entry added outside Kubernetes is deleted, a subnet moved off is moved back", func(t *testing.T) {
		if _, err := cloud.CreateNetworkAclEntry(ctx, &ec2.CreateNetworkAclEntryInput{
			NetworkAclId: aws.String(aclID), Egress: aws.Bool(false), RuleNumber: aws.Int32(200), Protocol: aws.String("-1"),
			RuleAction: ec2types.RuleActionDeny, CidrBlock: aws.String("0.0.0.0/0"),
		}); err != nil {
			t.Fatal(err)
		}
		moveTo(idA, aws.ToString(other.NetworkAcl.NetworkAclId))
		waitEntries(t, "the outside entry deleted", "in 100 allow 6 10.0.0.0/8 443-443")
		harness.Eventually(t, 60*time.Second, "subnet a moved back", func() error {
			for _, as := range cloudACL().Associations {
				if aws.ToString(as.SubnetId) == idA {
					return nil
				}
			}
			return fmt.Errorf("associations %v", cloudACL().Associations)
		})
	})

	t.Run("entries change: one replaced in place, one added", func(t *testing.T) {
		harness.Eventually(t, 30*time.Second, "spec updated", func() error {
			o := &ec2v1.DefaultNetworkACL{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), o); err != nil {
				return err
			}
			o.Spec.Entries = []*ackec2.NetworkACLEntry{
				entry(false, 100, "6", "allow", "10.0.0.0/8", 80, 80),
				entry(true, 100, "-1", "allow", "0.0.0.0/0", 0, 0),
			}
			return env.Client.Update(ctx, o)
		})
		waitEntries(t, "the new entries", "in 100 allow 6 10.0.0.0/8 80-80,out 100 allow -1 0.0.0.0/0")
	})

	t.Run("vpcID and vpcRef: exactly one", func(t *testing.T) {
		both := &ec2v1.DefaultNetworkACL{
			ObjectMeta: metav1.ObjectMeta{Name: "both", Namespace: "default"},
			Spec:       ec2v1.DefaultNetworkACLSpec{VPCID: aws.String(vpcID), VPCRef: obj.Spec.VPCRef},
		}
		if err := env.Client.Create(ctx, both); err == nil || !strings.Contains(err.Error(), "exactly one of vpcID and vpcRef") {
			t.Fatalf("both: got %v, want the CRD's refusal", err)
		}
	})

	t.Run("delete leaves the ACL, its entries and subnets", func(t *testing.T) {
		waitDeleted(t, env, obj, &ec2v1.DefaultNetworkACL{})
		waitEntries(t, "the entries kept", "in 100 allow 6 10.0.0.0/8 80-80,out 100 allow -1 0.0.0.0/0")
	})
}

// routeStrings is a table's routes made with CreateRoute, as
// "cidr->target" strings, sorted.
func routeStrings(routes []ec2types.Route) string {
	var out []string
	for _, r := range routes {
		if r.Origin != ec2types.RouteOriginCreateRoute {
			continue
		}
		target := aws.ToString(r.GatewayId) + aws.ToString(r.NatGatewayId) + aws.ToString(r.TransitGatewayId) + aws.ToString(r.VpcPeeringConnectionId)
		out = append(out, aws.ToString(r.DestinationCidrBlock)+"->"+target)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// DefaultRouteTable: a VPC's main route table taken over by vpcID, its
// routes (one by gatewayRef to ACK's InternetGateway), propagating
// gateways and tags made the spec's.
func TestDefaultRouteTable(t *testing.T) {
	env, cloud := defaultKindsEnv(t)
	ctx := context.Background()

	v, err := cloud.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.70.0.0/16")})
	if err != nil {
		t.Fatal(err)
	}
	vpcID := aws.ToString(v.Vpc.VpcId)
	tables, err := cloud.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{Filters: []ec2types.Filter{
		{Name: aws.String("vpc-id"), Values: []string{vpcID}}, {Name: aws.String("association.main"), Values: []string{"true"}},
	}})
	if err != nil || len(tables.RouteTables) != 1 {
		t.Fatalf("the VPC's main route table: %v %v", tables, err)
	}
	tableID := aws.ToString(tables.RouteTables[0].RouteTableId)
	igw, err := cloud.CreateInternetGateway(ctx, &ec2.CreateInternetGatewayInput{})
	if err != nil {
		t.Fatal(err)
	}
	igwID := aws.ToString(igw.InternetGateway.InternetGatewayId)
	if _, err := cloud.AttachInternetGateway(ctx, &ec2.AttachInternetGatewayInput{InternetGatewayId: aws.String(igwID), VpcId: aws.String(vpcID)}); err != nil {
		t.Fatal(err)
	}
	igwObj := &ackec2.InternetGateway{ObjectMeta: metav1.ObjectMeta{Name: "igw", Namespace: "default"}}
	ackEC2Synced(t, ctx, env.Client, igwObj, &igwObj.Status.Conditions, func() { igwObj.Status.InternetGatewayID = aws.String(igwID) })
	vgw, err := cloud.CreateVpnGateway(ctx, &ec2.CreateVpnGatewayInput{Type: ec2types.GatewayTypeIpsec1})
	if err != nil {
		t.Fatal(err)
	}
	vgwID := aws.ToString(vgw.VpnGateway.VpnGatewayId)
	if _, err := cloud.AttachVpnGateway(ctx, &ec2.AttachVpnGatewayInput{VpnGatewayId: aws.String(vgwID), VpcId: aws.String(vpcID)}); err != nil {
		t.Fatal(err)
	}
	// A route made outside Kubernetes before the take-over: removed.
	if _, err := cloud.CreateRoute(ctx, &ec2.CreateRouteInput{RouteTableId: aws.String(tableID), DestinationCidrBlock: aws.String("192.168.0.0/16"), GatewayId: aws.String(igwID)}); err != nil {
		t.Fatal(err)
	}

	cloudTable := func() ec2types.RouteTable {
		t.Helper()
		out, err := cloud.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{RouteTableIds: []string{tableID}})
		if err != nil || len(out.RouteTables) != 1 {
			t.Fatalf("describe %s: %v", tableID, err)
		}
		return out.RouteTables[0]
	}
	waitRoutes := func(t *testing.T, what, want string) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, what, func() error {
			if got := routeStrings(cloudTable().Routes); got != want {
				return fmt.Errorf("routes %q", got)
			}
			return nil
		})
	}
	propagating := func() string {
		var out []string
		for _, p := range cloudTable().PropagatingVgws {
			out = append(out, aws.ToString(p.GatewayId))
		}
		return strings.Join(out, ",")
	}

	obj := &ec2v1.DefaultRouteTable{
		ObjectMeta: metav1.ObjectMeta{Name: "main-rt", Namespace: "default"},
		Spec: ec2v1.DefaultRouteTableSpec{
			VPCID: aws.String(vpcID),
			Routes: []*ackec2.CreateRouteInput{{
				DestinationCIDRBlock: aws.String("0.0.0.0/0"),
				GatewayRef:           &ackv1alpha1.AWSResourceReferenceWrapper{From: &ackv1alpha1.AWSResourceReference{Name: aws.String("igw")}},
			}},
			PropagatingVGWs: []*string{aws.String(vgwID)},
			Tags:            []*ec2v1.Tag{{Key: aws.String("team"), Value: aws.String("net")}},
		},
	}
	conds := func() []*ackv1alpha1.Condition { return obj.Status.Conditions }

	t.Run("the main route table is taken over: routes, propagation and tags become the spec's", func(t *testing.T) {
		if err := env.Client.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
		waitObjectSynced(t, env, obj, conds)
		if aws.ToString(obj.Status.ID) != tableID {
			t.Fatalf("status.id %q, want %q", aws.ToString(obj.Status.ID), tableID)
		}
		waitRoutes(t, "the spec's route only (the outside one deleted, the local one kept)", "0.0.0.0/0->"+igwID)
		hasLocal := false
		for _, r := range cloudTable().Routes {
			if aws.ToString(r.GatewayId) == "local" {
				hasLocal = true
			}
		}
		if !hasLocal {
			t.Fatalf("the local route is gone: %v", cloudTable().Routes)
		}
		harness.Eventually(t, 30*time.Second, "the VPN gateway propagates", func() error {
			if got := propagating(); got != vgwID {
				return fmt.Errorf("propagating %q", got)
			}
			return nil
		})
		if ec2TagMap(cloudTable().Tags)["team"] != "net" {
			t.Fatalf("cloud tags %v", ec2TagMap(cloudTable().Tags))
		}
		// The reference stays in the spec.
		if obj.Spec.Routes[0].GatewayRef == nil {
			t.Fatalf("routes[0].gatewayRef lost: %+v", obj.Spec.Routes[0])
		}
	})

	t.Run("it settles: no update across resyncs", func(t *testing.T) {
		before := updatesOf(env, "main-rt")
		time.Sleep(15 * time.Second)
		if n := updatesOf(env, "main-rt"); n != before {
			t.Fatalf("updated %d times across resyncs", n-before)
		}
	})

	t.Run("a route added outside Kubernetes is deleted", func(t *testing.T) {
		if _, err := cloud.CreateRoute(ctx, &ec2.CreateRouteInput{RouteTableId: aws.String(tableID), DestinationCidrBlock: aws.String("172.16.0.0/12"), GatewayId: aws.String(igwID)}); err != nil {
			t.Fatal(err)
		}
		waitRoutes(t, "the outside route deleted", "0.0.0.0/0->"+igwID)
	})

	t.Run("routes and propagation change", func(t *testing.T) {
		harness.Eventually(t, 30*time.Second, "spec updated", func() error {
			o := &ec2v1.DefaultRouteTable{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), o); err != nil {
				return err
			}
			o.Spec.Routes = []*ackec2.CreateRouteInput{{DestinationCIDRBlock: aws.String("10.99.0.0/16"), GatewayID: aws.String(igwID)}}
			o.Spec.PropagatingVGWs = nil
			return env.Client.Update(ctx, o)
		})
		waitRoutes(t, "the new route only", "10.99.0.0/16->"+igwID)
		harness.Eventually(t, 30*time.Second, "propagation off", func() error {
			if got := propagating(); got != "" {
				return fmt.Errorf("propagating %q", got)
			}
			return nil
		})
	})

	t.Run("delete leaves the table and its routes", func(t *testing.T) {
		waitDeleted(t, env, obj, &ec2v1.DefaultRouteTable{})
		waitRoutes(t, "the route kept", "10.99.0.0/16->"+igwID)
	})
}

// DefaultVPC: the region's default VPC taken over with no reference, its
// DNS settings and tags made the spec's. A region with no default VPC is
// retried until one is made.
func TestDefaultVPC(t *testing.T) {
	env, cloud := defaultKindsEnv(t)
	ctx := context.Background()

	// moto makes the region's default VPC; delete it (with its default
	// subnets and internet gateway) so the object first finds none.
	vpcs, err := cloud.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{Filters: []ec2types.Filter{{Name: aws.String("is-default"), Values: []string{"true"}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vpcs.Vpcs {
		subnets, err := cloud.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{Filters: []ec2types.Filter{{Name: aws.String("vpc-id"), Values: []string{aws.ToString(v.VpcId)}}}})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range subnets.Subnets {
			if _, err := cloud.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: s.SubnetId}); err != nil {
				t.Fatal(err)
			}
		}
		igws, err := cloud.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{Filters: []ec2types.Filter{{Name: aws.String("attachment.vpc-id"), Values: []string{aws.ToString(v.VpcId)}}}})
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range igws.InternetGateways {
			if _, err := cloud.DetachInternetGateway(ctx, &ec2.DetachInternetGatewayInput{InternetGatewayId: g.InternetGatewayId, VpcId: v.VpcId}); err != nil {
				t.Fatal(err)
			}
			if _, err := cloud.DeleteInternetGateway(ctx, &ec2.DeleteInternetGatewayInput{InternetGatewayId: g.InternetGatewayId}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := cloud.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: v.VpcId}); err != nil {
			t.Fatal(err)
		}
	}

	obj := &ec2v1.DefaultVPC{
		ObjectMeta: metav1.ObjectMeta{Name: "default-vpc", Namespace: "default"},
		Spec: ec2v1.DefaultVPCSpec{
			EnableDNSHostnames: aws.Bool(false),
			Tags:               []*ec2v1.Tag{{Key: aws.String("Name"), Value: aws.String("default")}},
		},
	}
	var vpcID string
	attr := func(name ec2types.VpcAttributeName) bool {
		t.Helper()
		out, err := cloud.DescribeVpcAttribute(ctx, &ec2.DescribeVpcAttributeInput{VpcId: aws.String(vpcID), Attribute: name})
		if err != nil {
			t.Fatal(err)
		}
		switch name {
		case ec2types.VpcAttributeNameEnableDnsSupport:
			return aws.ToBool(out.EnableDnsSupport.Value)
		default:
			return aws.ToBool(out.EnableDnsHostnames.Value)
		}
	}

	t.Run("no default VPC: retried, not terminal", func(t *testing.T) {
		if err := env.Client.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
		if aws.ToBool(obj.Spec.EnableDNSSupport) != true {
			t.Fatalf("enableDNSSupport defaulted to %v, want true", obj.Spec.EnableDNSSupport)
		}
		harness.Eventually(t, 60*time.Second, "a recoverable condition naming the missing default VPC", func() error {
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
				return err
			}
			for _, c := range obj.Status.Conditions {
				if c.Type == ackv1alpha1.ConditionTypeTerminal && c.Status == corev1.ConditionTrue {
					t.Fatalf("terminal: %s", aws.ToString(c.Message))
				}
				if c.Type == ackv1alpha1.ConditionTypeRecoverable && c.Status == corev1.ConditionTrue && strings.Contains(aws.ToString(c.Message), "no default VPC") {
					return nil
				}
			}
			return fmt.Errorf("conditions %v", obj.Status.Conditions)
		})
	})

	t.Run("a default VPC made: taken over, its settings and tags the spec's", func(t *testing.T) {
		made, err := cloud.CreateDefaultVpc(ctx, &ec2.CreateDefaultVpcInput{})
		if err != nil {
			t.Fatal(err)
		}
		vpcID = aws.ToString(made.Vpc.VpcId)
		harness.Eventually(t, 120*time.Second, "taken over", func() error {
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
				return err
			}
			if aws.ToString(obj.Status.VPCID) != vpcID {
				return fmt.Errorf("status.vpcID %q, want %q", aws.ToString(obj.Status.VPCID), vpcID)
			}
			if ok, why := synced(obj.Status.Conditions); !ok {
				return fmt.Errorf("not synced: %s", why)
			}
			return nil
		})
		if aws.ToString(obj.Status.CIDRBlock) == "" {
			t.Fatalf("status.cidrBlock empty")
		}
		if attr(ec2types.VpcAttributeNameEnableDnsHostnames) || !attr(ec2types.VpcAttributeNameEnableDnsSupport) {
			t.Fatalf("dns hostnames %v support %v, want false true", attr(ec2types.VpcAttributeNameEnableDnsHostnames), attr(ec2types.VpcAttributeNameEnableDnsSupport))
		}
		out, _ := cloud.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{vpcID}})
		if ec2TagMap(out.Vpcs[0].Tags)["Name"] != "default" {
			t.Fatalf("tags %v", out.Vpcs[0].Tags)
		}
	})

	t.Run("it settles: no update across resyncs", func(t *testing.T) {
		before := updatesOf(env, "default-vpc")
		time.Sleep(15 * time.Second)
		if n := updatesOf(env, "default-vpc"); n != before {
			t.Fatalf("updated %d times across resyncs", n-before)
		}
	})

	t.Run("a setting changed outside Kubernetes is put back", func(t *testing.T) {
		if _, err := cloud.ModifyVpcAttribute(ctx, &ec2.ModifyVpcAttributeInput{VpcId: aws.String(vpcID), EnableDnsHostnames: &ec2types.AttributeBooleanValue{Value: aws.Bool(true)}}); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "dns hostnames off again", func() error {
			if attr(ec2types.VpcAttributeNameEnableDnsHostnames) {
				return fmt.Errorf("still on")
			}
			return nil
		})
	})

	t.Run("delete leaves the VPC", func(t *testing.T) {
		waitDeleted(t, env, obj, &ec2v1.DefaultVPC{})
		out, err := cloud.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{vpcID}})
		if err != nil || len(out.Vpcs) != 1 {
			t.Fatalf("the default VPC: %v %v", out, err)
		}
	})
}
