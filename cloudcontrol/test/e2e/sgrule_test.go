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

// TestSecurityGroupRules, EC2: standalone security group rules (Terraform's
// aws_vpc_security_group_ingress_rule and _egress_rule; plan question 7),
// on a group named by reference to ACK's SecurityGroup (groupRef):
//
//   - an inbound rule from another group (sourceSecurityGroupRef): nothing is
//     sent while the groups are not synced; once they are, the rule is
//     authorized in EC2 with the IDs from their status, the object's status
//     carries the rule ID (sgr-...), a resync sends no update, and a change
//     to a create-only reference is refused by the CRD;
//   - an inbound rule from a CIDR with a description: the description is the
//     one mutable property, and a change to it reaches EC2 in place, the
//     rule ID unchanged;
//   - an outbound rule to a prefix list (destinationPrefixListRef, ACK's
//     ManagedPrefixList) for every protocol;
//   - each delete revokes its own rule only: the group's other rules (the
//     default outbound rule among them) stay.
func TestSecurityGroupRules(t *testing.T) {
	e := start(t)
	c := ec2.NewFromConfig(e.moto)
	const ingress, egress = "AWS::EC2::SecurityGroupIngress", "AWS::EC2::SecurityGroupEgress"

	vpc, err := c.CreateVpc(e.ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.42.0.0/16")})
	if err != nil {
		t.Fatal(err)
	}
	group := func(name string) string {
		out, err := c.CreateSecurityGroup(e.ctx, &ec2.CreateSecurityGroupInput{
			GroupName: aws.String(name), Description: aws.String(name), VpcId: vpc.Vpc.VpcId,
		})
		if err != nil {
			t.Fatal(err)
		}
		return aws.ToString(out.GroupId)
	}
	webID, lbID := group("web"), group("lb")
	pl, err := c.CreateManagedPrefixList(e.ctx, &ec2.CreateManagedPrefixListInput{
		PrefixListName: aws.String("corp"), AddressFamily: aws.String("IPv4"), MaxEntries: aws.Int32(5),
		Entries: []ec2types.AddPrefixListEntry{{Cidr: aws.String("192.0.2.0/24")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	plID := aws.ToString(pl.PrefixList.PrefixListId)

	rules := func() []ec2types.SecurityGroupRule {
		t.Helper()
		out, err := c.DescribeSecurityGroupRules(e.ctx, &ec2.DescribeSecurityGroupRulesInput{
			Filters: []ec2types.Filter{{Name: aws.String("group-id"), Values: []string{webID}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return out.SecurityGroupRules
	}
	ruleByID := func(id string) (ec2types.SecurityGroupRule, bool) {
		for _, r := range rules() {
			if aws.ToString(r.SecurityGroupRuleId) == id {
				return r, true
			}
		}
		return ec2types.SecurityGroupRule{}, false
	}
	before := len(rules()) // the default outbound rule
	ruleID := func(u *unstructured.Unstructured) string {
		t.Helper()
		got := e.waitSynced(t, u)
		id, _, _ := unstructured.NestedString(got.Object, "status", "id")
		if !strings.HasPrefix(id, "sgr-") {
			t.Fatalf("%s: status.id %q, want the rule ID (sgr-...)", u.GetName(), id)
		}
		if ident, _, _ := unstructured.NestedString(got.Object, "status", "cloudControl", "identifier"); ident != id {
			t.Errorf("%s: identifier %q, want the rule ID %q", u.GetName(), ident, id)
		}
		return id
	}

	// ACK's SecurityGroup and ManagedPrefixList, as stand-ins
	// (TestReferences): the manager reads only their status.
	web := e.standIn(t, "SecurityGroup", "securitygroups", "web")
	lb := e.standIn(t, "SecurityGroup", "securitygroups", "lb")
	corp := e.standIn(t, "ManagedPrefixList", "managedprefixlists", "corp")

	// Both an ID and its reference: refused by the CRD.
	both := objOf(ingress, "both", map[string]any{"groupRef": refTo("web"), "groupId": webID, "ipProtocol": "tcp", "cidrIp": "10.0.0.0/8", "fromPort": int64(22), "toPort": int64(22)})
	if err := e.c.Create(e.ctx, both); err == nil || !strings.Contains(err.Error(), "cannot both be given") {
		t.Errorf("an ID with its reference: %v; want the CRD's refusal", err)
	}

	// 1. Inbound from another group, both by reference.
	fromLB := objOf(ingress, "web-from-lb", map[string]any{
		"groupRef": refTo("web"), "sourceSecurityGroupRef": refTo("lb"),
		"ipProtocol": "tcp", "fromPort": int64(443), "toPort": int64(443),
	})
	if err := e.c.Create(e.ctx, fromLB.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "references not resolved", func() error {
		s, reason := conditionReason(e.get(t, fromLB), ackv1alpha1.ConditionTypeReferencesResolved)
		if s != "Unknown" || !strings.Contains(reason, "not synced") {
			return fmt.Errorf("ACK.ReferencesResolved=%q: %s", s, reason)
		}
		return nil
	})
	if n := len(e.calls("CreateResource", ingress)); n != 0 {
		t.Fatalf("%d CreateResource calls before the references resolved", n)
	}
	e.setSynced(t, web, "id", webID)
	e.setSynced(t, lb, "id", lbID)
	e.setSynced(t, corp, "id", plID)
	lbRule := ruleID(fromLB)
	if s, _ := condition(e.get(t, fromLB), ackv1alpha1.ConditionTypeReferencesResolved); s != "True" {
		t.Errorf("ACK.ReferencesResolved=%q after the objects synced", s)
	}
	r, ok := ruleByID(lbRule)
	if !ok {
		t.Fatalf("rule %s is not on group %s in EC2", lbRule, webID)
	}
	if aws.ToBool(r.IsEgress) || aws.ToString(r.IpProtocol) != "tcp" || aws.ToInt32(r.FromPort) != 443 || aws.ToInt32(r.ToPort) != 443 ||
		r.ReferencedGroupInfo == nil || aws.ToString(r.ReferencedGroupInfo.GroupId) != lbID {
		t.Errorf("in EC2 the rule is %+v; want inbound tcp 443 from %s", r, lbID)
	}
	spec, _, _ := unstructured.NestedMap(e.get(t, fromLB).Object, "spec")
	for _, k := range []string{"groupId", "sourceSecurityGroupId"} {
		if _, ok := spec[k]; ok {
			t.Errorf("the object's spec %v holds the resolved %s; it keeps the reference only", spec, k)
		}
	}

	// 2. Inbound from a CIDR, with a description.
	ssh := objOf(ingress, "web-ssh", map[string]any{
		"groupRef": refTo("web"), "ipProtocol": "tcp", "fromPort": int64(22), "toPort": int64(22),
		"cidrIp": "10.0.0.0/8", "description": "ssh",
	})
	if err := e.c.Create(e.ctx, ssh.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	sshRule := ruleID(ssh)

	// 3. Outbound to a prefix list, every protocol.
	toCorp := objOf(egress, "web-to-corp", map[string]any{
		"groupRef": refTo("web"), "destinationPrefixListRef": refTo("corp"), "ipProtocol": "-1",
	})
	if err := e.c.Create(e.ctx, toCorp.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	corpRule := ruleID(toCorp)
	r, ok = ruleByID(corpRule)
	if !ok || !aws.ToBool(r.IsEgress) || aws.ToString(r.PrefixListId) != plID {
		t.Errorf("in EC2 the outbound rule is %+v (found %v); want egress to %s", r, ok, plID)
	}
	if n := len(rules()); n != before+3 {
		t.Errorf("%d rules on the group, want the %d it had and the three", n, before)
	}

	// A resync of the unchanged objects sends no update.
	time.Sleep(3 * time.Second)
	for _, typ := range []string{ingress, egress} {
		if n := len(e.calls("UpdateResource", typ)); n != 0 {
			t.Errorf("%d UpdateResource calls for unchanged %s objects", n, typ)
		}
	}
	// The references are create-only, as the IDs are.
	if err := e.update(t, fromLB, func(u *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(u.Object, "other", "spec", "sourceSecurityGroupRef", "from", "name")
	}); err == nil || !strings.Contains(err.Error(), "create-only") {
		t.Errorf("a changed sourceSecurityGroupRef: %v; want the CRD's refusal", err)
	}

	// The description changes in place.
	if err := e.update(t, ssh, func(u *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(u.Object, "ssh from the VPN", "spec", "description")
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "the new description in EC2", func() error {
		r, ok := ruleByID(sshRule)
		if !ok {
			return fmt.Errorf("rule %s gone", sshRule)
		}
		if d := aws.ToString(r.Description); d != "ssh from the VPN" {
			return fmt.Errorf("description %q", d)
		}
		return nil
	})
	if id, _, _ := unstructured.NestedString(e.get(t, ssh).Object, "status", "id"); id != sshRule {
		t.Errorf("after the description's change the rule is %q, want %q (in place)", id, sshRule)
	}

	// Each delete revokes its own rule; the group's others stay.
	left := before + 3
	for _, c := range []struct {
		u  *unstructured.Unstructured
		id string
	}{{fromLB, lbRule}, {toCorp, corpRule}, {ssh, sshRule}} {
		if err := e.c.Delete(e.ctx, c.u); err != nil {
			t.Fatal(err)
		}
		e.waitGone(t, c.u)
		left--
		if _, ok := ruleByID(c.id); ok {
			t.Errorf("%s: rule %s still in EC2 after the delete", c.u.GetName(), c.id)
		}
		if n := len(rules()); n != left {
			t.Errorf("after deleting %s: %d rules on the group, want %d", c.u.GetName(), n, left)
		}
	}
}
