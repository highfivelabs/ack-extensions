// Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//     http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0
//
// Modified by High Five Labs for ack-extensions: copied from
// aws-controllers-k8s/ec2-controller v1.23.1 (9303654c),
// pkg/resource/security_group/hooks.go (the rule canonicalisation, the
// rule sync, Authorize/Revoke and containsRule) and sdk.go
// (compareIPPermission, newIPPermission, setResourceIPPermission), the
// rules of ACK's SecurityGroup. Changes: package, and the rule types are
// ACK's own (ec2.services.k8s.aws, imported as ackec2), which our
// DefaultSecurityGroup's spec uses.

package default_security_group

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"

	ackec2 "github.com/aws-controllers-k8s/ec2-controller/apis/v1alpha1"
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"k8s.io/apimachinery/pkg/api/equality"
)

// icmpv6Protocol is the canonical protocol name canonicalizeProtocol emits for
// protocol 58. Unlike icmp, icmpv6 does not require an ICMP type/code, so the
// spec may omit fromPort/toPort while AWS returns the "all types and codes"
// wildcard as -1/-1 on read-back.
const icmpv6Protocol = "icmpv6"

// portCarryingProtocols are the protocols EC2 keeps a port range for (or, for
// ICMP, an overloaded type/code) on read-back. For every other protocol -- "-1"
// (all traffic) and any other IP protocol number such as 50 (ESP) or 47 (GRE)
// -- AWS omits fromPort/toPort on read-back. Keyed by the canonical name
// canonicalizeProtocol emits (numeric 6/17/1/58 are mapped to tcp/udp/icmp/icmpv6
// first).
var portCarryingProtocols = map[string]struct{}{
	"tcp":          {},
	"udp":          {},
	"icmp":         {},
	icmpv6Protocol: {},
}

// wellKnownProtocols are the IANA protocol numbers EC2 returns by name on
// read-back, so a numeric spec value ("6") would otherwise differ forever from
// the name ("tcp"). Other protocols (and "-1") are already canonical.
var wellKnownProtocols = map[string]string{
	"1":  "icmp",
	"6":  "tcp",
	"17": "udp",
	"58": "icmpv6",
}

// canonicalizeProtocol maps a numeric IpProtocol to the name EC2 returns;
// unknown protocols and names pass through unchanged.
func canonicalizeProtocol(proto *string) *string {
	if proto == nil {
		return nil
	}
	if name, ok := wellKnownProtocols[*proto]; ok {
		return &name
	}
	return proto
}

// canonicalizeCIDR rewrites a CIDR to the exact network form AWS stores and
// returns on read-back: a network mask is built one byte at a time from the
// prefix length and ANDed with the address, masking off every host bit. The
// masked address is rendered in the same text form AWS returns -- dotted-quad
// for IPv4, RFC 5952 lowercase zero-compressed for IPv6 -- so "100.68.0.18/18"
// -> "100.68.0.0/18" and "2001:DB8:abcd:0012::1/64" -> "2001:db8:abcd:12::/64".
//
// Applying this conversion before the delta comparison is what stops the
// spurious diff: the desired spec form and the AWS read-back form collapse to
// the identical network.
//
// A value that is not a well-formed CIDR -- or whose prefix is longer than the
// address, which AWS rejects -- passes through unchanged.
func canonicalizeCIDR(cidr *string) *string {
	if cidr == nil {
		return nil
	}
	slash := strings.LastIndex(*cidr, "/")
	if slash < 0 {
		return cidr
	}
	ip := net.ParseIP((*cidr)[:slash])
	prefixLength, err := strconv.Atoi((*cidr)[slash+1:])
	if ip == nil || err != nil || prefixLength < 0 {
		return cidr
	}

	// Use the canonical byte layout: 4 bytes for IPv4, 16 for IPv6.
	addr := ip.To4()
	if addr == nil {
		addr = ip.To16()
	}
	if addr == nil {
		return cidr
	}

	// Build the network mask one byte at a time and AND it into the address.
	// Bytes past the prefix stay zero, which is the host-bit masking.
	remaining := prefixLength
	network := make(net.IP, len(addr))
	for i := 0; i < len(addr) && remaining > 0; i++ {
		var maskByte byte
		if remaining > 8 {
			maskByte = 0xff
			remaining -= 8
		} else {
			maskByte = byte(((1 << remaining) - 1) << (8 - remaining))
			remaining = 0
		}
		network[i] = addr[i] & maskByte
	}
	// remaining != 0 means the prefix is longer than the address (e.g. /40 on an
	// IPv4 address); AWS treats this as malformed, so leave it untouched.
	if remaining != 0 {
		return cidr
	}

	canon := network.String() + "/" + strconv.Itoa(prefixLength)
	return &canon
}

// customPostCompare compares Spec.IngressRules/EgressRules, which the generated
// delta skips (they are marked compare.is_ignored). It normalises canonical
// *copies* of both sides -- never a or b, which become the merge-patch base in
// patchResourceMetadataAndSpec -- so the server-side rule normalisation behind
// aws-controllers-k8s/community#2822 never drives a spurious diff, while genuine
// changes still register. See canonicalizeRuleList and canonicalizeGroupPair for
// the specific normalisations.
func customPostCompare(
	delta *ackcompare.Delta,
	a *resource,
	b *resource,
) {
	if a == nil || b == nil || a.ko == nil || b.ko == nil {
		return
	}
	// Rule canonicalisation needs the SG's ID and owner account, which are nil
	// pre-create. Skip rather than emit an unreliable diff; the next reconcile
	// recomputes it once the identity is known.
	if !securityGroupIdentityKnown(a) || !securityGroupIdentityKnown(b) {
		return
	}
	aIngress, aEgress := canonicalizeCopiedRuleLists(a)
	bIngress, bEgress := canonicalizeCopiedRuleLists(b)
	if !equality.Semantic.DeepEqual(aIngress, bIngress) {
		delta.Add("Spec.IngressRules", a.ko.Spec.IngressRules, b.ko.Spec.IngressRules)
	}
	if !equality.Semantic.DeepEqual(aEgress, bEgress) {
		delta.Add("Spec.EgressRules", a.ko.Spec.EgressRules, b.ko.Spec.EgressRules)
	}
}

// securityGroupIdentityKnown reports whether the SG's ID and owner account are
// populated -- both required for a meaningful rule delta, and only set
// post-create.
func securityGroupIdentityKnown(r *resource) bool {
	return r != nil && r.ko != nil &&
		r.ko.Status.ID != nil &&
		r.ko.Status.ACKResourceMetadata != nil &&
		r.ko.Status.ACKResourceMetadata.OwnerAccountID != nil
}

// canonicalizeCopiedRuleLists returns canonical *copies* of r's rule lists; r is
// never mutated. Used only for delta comparison, never the sync path (which
// authorises/revokes the raw spec rules). Precondition (guaranteed by
// customPostCompare's identity guard): Status.ID and OwnerAccountID are non-nil.
func canonicalizeCopiedRuleLists(
	r *resource,
) (ingress []*ackec2.IPPermission, egress []*ackec2.IPPermission) {
	selfID := *r.ko.Status.ID
	ownerAccountID := string(*r.ko.Status.ACKResourceMetadata.OwnerAccountID)
	ingress = canonicalizeRuleList(deepCopyRuleList(r.ko.Spec.IngressRules), selfID, ownerAccountID)
	egress = canonicalizeRuleList(deepCopyRuleList(r.ko.Spec.EgressRules), selfID, ownerAccountID)
	return ingress, egress
}

// deepCopyRuleList deep-copies a rule list so canonicalisation never touches the
// caller's data. nil in -> nil out (absent and empty stay equal).
func deepCopyRuleList(rules []*ackec2.IPPermission) []*ackec2.IPPermission {
	if rules == nil {
		return nil
	}
	out := make([]*ackec2.IPPermission, len(rules))
	for i, r := range rules {
		out[i] = r.DeepCopy()
	}
	return out
}

// canonicalizeRuleList normalises each rule and its grants, aggregates rules
// sharing the same (protocol, fromPort, toPort) key, and returns them
// deterministically sorted. nil in -> nil out.
func canonicalizeRuleList(
	rules []*ackec2.IPPermission,
	selfID string,
	ownerAccountID string,
) []*ackec2.IPPermission {
	if rules == nil {
		return nil
	}

	// Per-rule field normalisation to match the AWS read-back form.
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		rule.IPProtocol = canonicalizeProtocol(rule.IPProtocol)
		if rule.IPProtocol != nil {
			proto := *rule.IPProtocol
			if _, carriesPorts := portCarryingProtocols[proto]; !carriesPorts {
				// AWS omits the port range for every protocol outside
				// {tcp, udp, icmp, icmpv6}: "-1" (all traffic) and any other IP
				// protocol number such as 50 (ESP) or 47 (GRE). Drop them so the
				// spec form matches the read-back form.
				rule.FromPort = nil
				rule.ToPort = nil
			} else if proto == icmpv6Protocol {
				// icmpv6 overloads fromPort/toPort as ICMP type/code and, unlike
				// icmp, allows omitting them: the spec may leave them nil while AWS
				// returns the "all types and codes" wildcard as -1/-1 on read-back.
				// Collapse that -1 wildcard to nil so the omitted and explicit
				// spellings compare equal. A real type/code (0-255) is left
				// untouched, and icmp (which requires a type/code) is unaffected.
				if rule.FromPort != nil && *rule.FromPort == -1 {
					rule.FromPort = nil
				}
				if rule.ToPort != nil && *rule.ToPort == -1 {
					rule.ToPort = nil
				}
			}
		}
		for _, r := range rule.IPRanges {
			if r != nil {
				r.CIDRIP = canonicalizeCIDR(r.CIDRIP)
			}
		}
		for _, r := range rule.IPv6Ranges {
			if r != nil {
				r.CIDRIPv6 = canonicalizeCIDR(r.CIDRIPv6)
			}
		}
		for _, pair := range rule.UserIDGroupPairs {
			canonicalizeGroupPair(pair, selfID, ownerAccountID)
		}
	}

	// AWS merges grants sharing a (protocol, fromPort, toPort) key into one
	// IpPermission; aggregate the same way, keeping first-seen order.
	byKey := map[string]*ackec2.IPPermission{}
	order := make([]string, 0, len(rules))
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		key := ruleAggregationKey(rule)
		if existing, ok := byKey[key]; ok {
			existing.IPRanges = append(existing.IPRanges, rule.IPRanges...)
			existing.IPv6Ranges = append(existing.IPv6Ranges, rule.IPv6Ranges...)
			existing.PrefixListIDs = append(existing.PrefixListIDs, rule.PrefixListIDs...)
			existing.UserIDGroupPairs = append(existing.UserIDGroupPairs, rule.UserIDGroupPairs...)
			continue
		}
		// Copy so aggregation never aliases the caller's backing arrays.
		merged := &ackec2.IPPermission{
			FromPort:         rule.FromPort,
			ToPort:           rule.ToPort,
			IPProtocol:       rule.IPProtocol,
			IPRanges:         append([]*ackec2.IPRange(nil), rule.IPRanges...),
			IPv6Ranges:       append([]*ackec2.IPv6Range(nil), rule.IPv6Ranges...),
			PrefixListIDs:    append([]*ackec2.PrefixListID(nil), rule.PrefixListIDs...),
			UserIDGroupPairs: append([]*ackec2.UserIDGroupPair(nil), rule.UserIDGroupPairs...),
		}
		byKey[key] = merged
		order = append(order, key)
	}

	out := make([]*ackec2.IPPermission, 0, len(order))
	for _, key := range order {
		rule := byKey[key]
		dedupeGrants(rule)
		sortGrants(rule)
		out = append(out, rule)
	}
	// Sort by aggregation key (unique after aggregation) so read-back ordering
	// never drives a diff.
	sort.Slice(out, func(i, j int) bool {
		return ruleAggregationKey(out[i]) < ruleAggregationKey(out[j])
	})
	return out
}

// canonicalizeGroupPair normalises a single UserIDGroupPair (on a copy).
func canonicalizeGroupPair(
	pair *ackec2.UserIDGroupPair,
	selfID string,
	ownerAccountID string,
) {
	if pair == nil {
		return
	}
	// GroupRef/VPCRef are spec-only wrappers ResolveReferences turns into
	// concrete IDs before the delta and AWS never returns; drop them so they
	// never drive a diff. GroupID is assumed resolved (referencesResolved
	// defers sync until it is).
	pair.GroupRef = nil
	pair.VPCRef = nil

	// VPCID, PeeringStatus and VPCPeeringConnectionID are read-only: EC2 derives
	// them from the referenced group and ignores any spec value (verified against
	// the live API), so clear them on the copy to keep an AWS-filled cross-VPC
	// read-back from churning against an omitted spec value.
	pair.VPCID = nil
	pair.PeeringStatus = nil
	pair.VPCPeeringConnectionID = nil

	// Self-reference: AWS returns the SG's own GroupID on read-back while the
	// spec shorthand omits the group, so strip a self GroupID to converge both
	// sides.
	if pair.GroupID != nil && *pair.GroupID == selfID {
		pair.GroupID = nil
	}

	// GroupName is left untouched: AWS never returns it on read-back (it resolves
	// names to a GroupID), and by-name references only work in a default VPC. A
	// bare-GroupName reference therefore can't converge and will diff every
	// reconcile -- an accepted trade-off for a rare, default-VPC-only pattern.

	// AWS fills UserID with the owner account for same-account grants; clear it.
	// Cross-account grants (UserID != owner) are kept -- the account identifies
	// the referenced group.
	if pair.UserID != nil && *pair.UserID == ownerAccountID {
		pair.UserID = nil
	}
}

// ruleAggregationKey returns the key AWS aggregates IpPermissions by:
// protocol plus the (from, to) port range.
func ruleAggregationKey(rule *ackec2.IPPermission) string {
	proto := ""
	if rule.IPProtocol != nil {
		proto = *rule.IPProtocol
	}
	from := "nil"
	if rule.FromPort != nil {
		from = strconv.FormatInt(*rule.FromPort, 10)
	}
	to := "nil"
	if rule.ToPort != nil {
		to = strconv.FormatInt(*rule.ToPort, 10)
	}
	return proto + "/" + from + "/" + to
}

// dedupeGrants drops each grant of rule that equals an earlier one. AWS keeps
// one rule per source, so a source listed twice is one rule.
func dedupeGrants(rule *ackec2.IPPermission) {
	rule.IPRanges = uniqueGrants(rule.IPRanges)
	rule.IPv6Ranges = uniqueGrants(rule.IPv6Ranges)
	rule.PrefixListIDs = uniqueGrants(rule.PrefixListIDs)
	rule.UserIDGroupPairs = uniqueGrants(rule.UserIDGroupPairs)
}

// uniqueGrants returns grants without each grant whose JSON form equals an
// earlier one's.
func uniqueGrants[T any](grants []T) []T {
	var unique []T
	seen := map[string]bool{}
	for _, g := range grants {
		key, _ := json.Marshal(g)
		if !seen[string(key)] {
			seen[string(key)] = true
			unique = append(unique, g)
		}
	}
	return unique
}

// sortGrants deterministically orders a rule's grant collections so read-back
// ordering never drives a diff. Sort keys are nil-safe (defensive; the schema
// and setters never emit nil elements).
func sortGrants(rule *ackec2.IPPermission) {
	if rule == nil {
		return
	}
	sort.Slice(rule.IPRanges, func(i, j int) bool {
		return ipRangeSortKey(rule.IPRanges[i]) < ipRangeSortKey(rule.IPRanges[j])
	})
	sort.Slice(rule.IPv6Ranges, func(i, j int) bool {
		return ipv6RangeSortKey(rule.IPv6Ranges[i]) < ipv6RangeSortKey(rule.IPv6Ranges[j])
	})
	sort.Slice(rule.PrefixListIDs, func(i, j int) bool {
		return prefixListIDSortKey(rule.PrefixListIDs[i]) < prefixListIDSortKey(rule.PrefixListIDs[j])
	})
	sort.Slice(rule.UserIDGroupPairs, func(i, j int) bool {
		return userIDGroupPairSortKey(rule.UserIDGroupPairs[i]) <
			userIDGroupPairSortKey(rule.UserIDGroupPairs[j])
	})
}

// ipRangeSortKey builds a nil-safe stable sort key for an IPv4 CIDR grant.
func ipRangeSortKey(r *ackec2.IPRange) string {
	if r == nil {
		return ""
	}
	return derefStr(r.CIDRIP)
}

// ipv6RangeSortKey builds a nil-safe stable sort key for an IPv6 CIDR grant.
func ipv6RangeSortKey(r *ackec2.IPv6Range) string {
	if r == nil {
		return ""
	}
	return derefStr(r.CIDRIPv6)
}

// prefixListIDSortKey builds a nil-safe stable sort key for a prefix-list grant.
func prefixListIDSortKey(p *ackec2.PrefixListID) string {
	if p == nil {
		return ""
	}
	return derefStr(p.PrefixListID)
}

// userIDGroupPairSortKey builds a stable sort key for a group pair from the
// fields that identify it.
func userIDGroupPairSortKey(pair *ackec2.UserIDGroupPair) string {
	if pair == nil {
		return ""
	}
	return derefStr(pair.GroupID) + "/" + derefStr(pair.UserID) + "/" +
		derefStr(pair.GroupName)
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// syncSGRules analyzes desired and latest (if any)
// resources and executes API calls to Create/Delete
// rules in order to achieve desired state.
func (rm *resourceManager) syncSGRules(
	ctx context.Context,
	desired *resource,
	latest *resource,
) (err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.syncSGRules")
	defer func() { exit(err) }()

	// The sync path deliberately matches and authorises/revokes the RAW spec
	// rules, never canonicalised copies. Canonicalisation lives only in the
	// delta (customPostCompare): the canonical delta gates whether sync fires
	// and converges once the state matches, so a canonicalisation defect can, at
	// worst, mis-scope which rules churn -- it can never change the rule content
	// sent to AWS. Keeping the raw spec here means the values submitted to
	// Authorize/Revoke are always exactly what the user declared (or what AWS
	// itself returned), which AWS then normalises server-side.
	toAddIngress := []*ackec2.IPPermission{}
	toAddEgress := []*ackec2.IPPermission{}
	toDeleteIngress := []*ackec2.IPPermission{}
	toDeleteEgress := []*ackec2.IPPermission{}
	for _, desiredIngress := range desired.ko.Spec.IngressRules {
		if latest == nil || !containsRule(latest.ko.Spec.IngressRules, desiredIngress) {
			// a desired rule is not in the latest resource; therefore, create
			toAddIngress = append(toAddIngress, desiredIngress)
		}
	}
	for _, desiredEgress := range desired.ko.Spec.EgressRules {
		if latest == nil || !containsRule(latest.ko.Spec.EgressRules, desiredEgress) {
			toAddEgress = append(toAddEgress, desiredEgress)
		}
	}
	if latest != nil {
		for _, latestIngress := range latest.ko.Spec.IngressRules {
			if !containsRule(desired.ko.Spec.IngressRules, latestIngress) {
				// a rule is in latest resource, but not in desired resource; therefore, delete
				toDeleteIngress = append(toDeleteIngress, latestIngress)
			}
		}
		for _, latestEgress := range latest.ko.Spec.EgressRules {
			if !containsRule(desired.ko.Spec.EgressRules, latestEgress) {
				toDeleteEgress = append(toDeleteEgress, latestEgress)
			}
		}
	}

	// Delete before create for the following reasons:
	// - Updating a rule requires that it be removed before the updated version be added.
	// - If there is an error with adding new rules, it occurs after deletion of old ones;
	//   This is safer and closer to achieving desired resource state.
	if err = rm.deleteSecurityGroupRules(ctx, latest, toDeleteIngress, toDeleteEgress); err != nil {
		return err
	}
	if err = rm.createSecurityGroupRules(ctx, desired, toAddIngress, toAddEgress); err != nil {
		return err
	}

	return nil
}

// createSecurityGroupRules takes a list of ingress and egress
// rules and attaches them to a SecurityGroup resource via
// AuthorizeSecurityGroup API calls
func (rm *resourceManager) createSecurityGroupRules(
	ctx context.Context,
	r *resource,
	ingress []*ackec2.IPPermission,
	egress []*ackec2.IPPermission,
) (err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.createSecurityGroupRules")
	defer func() { exit(err) }()

	ingressRules := []svcsdktypes.IpPermission{}

	// Authorize ingress rules
	for _, i := range ingress {
		ipInput, err := rm.newIPPermission(*i)
		if err != nil {
			return err
		}
		for j := range ipInput.UserIdGroupPairs {
			// If not provided, we need to default the security group ID and vpc ID.
			//
			// The newIPPermission function doesn't return nil UserIdGroupPair items. It is safe to
			// access them here.
			if ipInput.UserIdGroupPairs[j].GroupId == nil && ipInput.UserIdGroupPairs[j].GroupName == nil {
				ipInput.UserIdGroupPairs[j].GroupId = r.ko.Status.ID
			}
			if ipInput.UserIdGroupPairs[j].VpcId == nil {
				ipInput.UserIdGroupPairs[j].VpcId = r.ko.Spec.VPCID
			}
		}
		ingressRules = append(ingressRules, *ipInput)
	}

	// API can only handle 1000 rules at a time. Send in batches of 1000.
	for i := 0; i < len(ingressRules); i += 1000 {
		end := i + 1000
		if end > len(ingressRules) {
			end = len(ingressRules)
		}
		req := &svcsdk.AuthorizeSecurityGroupIngressInput{
			GroupId:       r.ko.Status.ID,
			IpPermissions: ingressRules[i:end],
		}
		_, err = rm.sdkapi.AuthorizeSecurityGroupIngress(ctx, req)
		rm.metrics.RecordAPICall("CREATE", "AuthorizeSecurityGroupIngress", err)
		if err != nil {
			return err
		}
	}

	egressRules := []svcsdktypes.IpPermission{}
	// Authorize egress rules
	for _, e := range egress {
		ipInput, err := rm.newIPPermission(*e)
		if err != nil {
			return err
		}
		for j := range ipInput.UserIdGroupPairs {
			// If not provided, we need to default the security group ID and vpc ID.
			//
			// The newIPPermission function doesn't return nil UserIdGroupPair items. It is safe to
			// access them here.
			if ipInput.UserIdGroupPairs[j].GroupId == nil && ipInput.UserIdGroupPairs[j].GroupName == nil {
				ipInput.UserIdGroupPairs[j].GroupId = r.ko.Status.ID
			}
			if ipInput.UserIdGroupPairs[j].VpcId == nil {
				ipInput.UserIdGroupPairs[j].VpcId = r.ko.Spec.VPCID
			}
		}
		egressRules = append(egressRules, *ipInput)
	}

	// API can only handle 1000 rules at a time. Send in batches of 1000.
	for i := 0; i < len(egressRules); i += 1000 {
		end := i + 1000
		if end > len(egressRules) {
			end = len(egressRules)
		}
		req := &svcsdk.AuthorizeSecurityGroupEgressInput{
			GroupId:       r.ko.Status.ID,
			IpPermissions: egressRules[i:end],
		}
		_, err = rm.sdkapi.AuthorizeSecurityGroupEgress(ctx, req)
		rm.metrics.RecordAPICall("CREATE", "AuthorizeSecurityGroupEgress", err)
		if err != nil {
			return err
		}
	}

	return err
}

// deleteSecurityGroupRules takes a list of ingress and egress
// rules and removes them from a SecurityGroup resource via
// RevokeSecurityGroup API calls
func (rm *resourceManager) deleteSecurityGroupRules(
	ctx context.Context,
	r *resource,
	ingress []*ackec2.IPPermission,
	egress []*ackec2.IPPermission,
) (err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.deleteSecurityGroupRules")
	defer func() { exit(err) }()

	// Revoke ingress rules
	ingressRules := []svcsdktypes.IpPermission{}
	for _, i := range ingress {
		ipInput, err := rm.newIPPermission(*i)
		if err != nil {
			return err
		}
		for j := range ipInput.UserIdGroupPairs {
			// If not provided, we need to default the security group ID and vpc ID.
			//
			// The newIPPermission function doesn't return nil UserIdGroupPair items. It is safe to
			// access them here.
			if ipInput.UserIdGroupPairs[j].GroupId == nil && ipInput.UserIdGroupPairs[j].GroupName == nil {
				ipInput.UserIdGroupPairs[j].GroupId = r.ko.Status.ID
			}
			if ipInput.UserIdGroupPairs[j].VpcId == nil {
				ipInput.UserIdGroupPairs[j].VpcId = r.ko.Spec.VPCID
			}
		}
		ingressRules = append(ingressRules, *ipInput)
	}

	// API can only handle 1000 rules at a time. Send in batches of 1000.
	for i := 0; i < len(ingressRules); i += 1000 {
		end := i + 1000
		if end > len(ingressRules) {
			end = len(ingressRules)
		}
		req := &svcsdk.RevokeSecurityGroupIngressInput{
			GroupId:       r.ko.Status.ID,
			IpPermissions: ingressRules[i:end],
		}
		_, err = rm.sdkapi.RevokeSecurityGroupIngress(ctx, req)
		rm.metrics.RecordAPICall("DELETE", "RevokeSecurityGroupIngress", err)
		if err != nil {
			return err
		}
	}

	// Revoke egress rules
	egressRules := []svcsdktypes.IpPermission{}
	for _, e := range egress {
		ipInput, err := rm.newIPPermission(*e)
		if err != nil {
			return err
		}
		for j := range ipInput.UserIdGroupPairs {
			// If not provided, we need to default the security group ID and vpc ID.
			//
			// The newIPPermission function doesn't return nil UserIdGroupPair items. It is safe to
			// access them here.
			if ipInput.UserIdGroupPairs[j].GroupId == nil && ipInput.UserIdGroupPairs[j].GroupName == nil {
				ipInput.UserIdGroupPairs[j].GroupId = r.ko.Status.ID
			}
			if ipInput.UserIdGroupPairs[j].VpcId == nil {
				ipInput.UserIdGroupPairs[j].VpcId = r.ko.Spec.VPCID
			}
		}
		egressRules = append(egressRules, *ipInput)
	}

	// API can only handle 1000 rules at a time. Send in batches of 1000.
	for i := 0; i < len(egressRules); i += 1000 {
		end := i + 1000
		if end > len(egressRules) {
			end = len(egressRules)
		}
		req := &svcsdk.RevokeSecurityGroupEgressInput{
			GroupId:       r.ko.Status.ID,
			IpPermissions: egressRules[i:end],
		}
		_, err = rm.sdkapi.RevokeSecurityGroupEgress(ctx, req)
		rm.metrics.RecordAPICall("DELETE", "RevokeSecurityGroupEgress", err)
		if err != nil {
			return err
		}
	}

	return err
}

// containsRule returns true if security group rule
// is found in the rule collection (all fields must match);
// otherwise, return false.
func containsRule(
	ruleCollection []*ackec2.IPPermission,
	rule *ackec2.IPPermission,
) bool {
	if ruleCollection == nil || rule == nil {
		return false
	}

	for _, r := range ruleCollection {
		delta := compareIPPermission(r, rule)
		if len(delta.Differences) == 0 {
			return true
		}
	}
	return false
}

func compareIPPermission(
	a *ackec2.IPPermission,
	b *ackec2.IPPermission,
) *ackcompare.Delta {
	delta := ackcompare.NewDelta()
	if ackcompare.HasNilDifference(a.FromPort, b.FromPort) {
		delta.Add("IPPermission.FromPort", a.FromPort, b.FromPort)
	} else if a.FromPort != nil && b.FromPort != nil {
		if *a.FromPort != *b.FromPort {
			delta.Add("IPPermission.FromPort", a.FromPort, b.FromPort)
		}
	}
	if ackcompare.HasNilDifference(a.IPProtocol, b.IPProtocol) {
		delta.Add("IPPermission.IPProtocol", a.IPProtocol, b.IPProtocol)
	} else if a.IPProtocol != nil && b.IPProtocol != nil {
		if *a.IPProtocol != *b.IPProtocol {
			delta.Add("IPPermission.IPProtocol", a.IPProtocol, b.IPProtocol)
		}
	}
	if len(a.IPRanges) != len(b.IPRanges) {
		delta.Add("IPPermission.IPRanges", a.IPRanges, b.IPRanges)
	} else if len(a.IPRanges) > 0 {
		if !equality.Semantic.Equalities.DeepEqual(a.IPRanges, b.IPRanges) {
			delta.Add("IPPermission.IPRanges", a.IPRanges, b.IPRanges)
		}
	}
	if len(a.IPv6Ranges) != len(b.IPv6Ranges) {
		delta.Add("IPPermission.IPv6Ranges", a.IPv6Ranges, b.IPv6Ranges)
	} else if len(a.IPv6Ranges) > 0 {
		if !equality.Semantic.Equalities.DeepEqual(a.IPv6Ranges, b.IPv6Ranges) {
			delta.Add("IPPermission.IPv6Ranges", a.IPv6Ranges, b.IPv6Ranges)
		}
	}
	if len(a.PrefixListIDs) != len(b.PrefixListIDs) {
		delta.Add("IPPermission.PrefixListIDs", a.PrefixListIDs, b.PrefixListIDs)
	} else if len(a.PrefixListIDs) > 0 {
		if !equality.Semantic.Equalities.DeepEqual(a.PrefixListIDs, b.PrefixListIDs) {
			delta.Add("IPPermission.PrefixListIDs", a.PrefixListIDs, b.PrefixListIDs)
		}
	}
	if ackcompare.HasNilDifference(a.ToPort, b.ToPort) {
		delta.Add("IPPermission.ToPort", a.ToPort, b.ToPort)
	} else if a.ToPort != nil && b.ToPort != nil {
		if *a.ToPort != *b.ToPort {
			delta.Add("IPPermission.ToPort", a.ToPort, b.ToPort)
		}
	}
	if len(a.UserIDGroupPairs) != len(b.UserIDGroupPairs) {
		delta.Add("IPPermission.UserIDGroupPairs", a.UserIDGroupPairs, b.UserIDGroupPairs)
	} else if len(a.UserIDGroupPairs) > 0 {
		if !equality.Semantic.Equalities.DeepEqual(a.UserIDGroupPairs, b.UserIDGroupPairs) {
			delta.Add("IPPermission.UserIDGroupPairs", a.UserIDGroupPairs, b.UserIDGroupPairs)
		}
	}

	return delta
}

func (rm *resourceManager) newIPPermission(
	c ackec2.IPPermission,
) (*svcsdktypes.IpPermission, error) {
	res := &svcsdktypes.IpPermission{}

	if c.FromPort != nil {
		fromPortCopy0 := *c.FromPort
		if fromPortCopy0 > math.MaxInt32 || fromPortCopy0 < math.MinInt32 {
			return nil, fmt.Errorf("error: field FromPort is of type int32")
		}
		fromPortCopy := int32(fromPortCopy0)
		res.FromPort = &fromPortCopy
	}
	if c.IPProtocol != nil {
		res.IpProtocol = c.IPProtocol
	}
	if c.IPRanges != nil {
		resf2 := []svcsdktypes.IpRange{}
		for _, resf2iter := range c.IPRanges {
			resf2elem := &svcsdktypes.IpRange{}
			if resf2iter.CIDRIP != nil {
				resf2elem.CidrIp = resf2iter.CIDRIP
			}
			if resf2iter.Description != nil {
				resf2elem.Description = resf2iter.Description
			}
			resf2 = append(resf2, *resf2elem)
		}
		res.IpRanges = resf2
	}
	if c.IPv6Ranges != nil {
		resf3 := []svcsdktypes.Ipv6Range{}
		for _, resf3iter := range c.IPv6Ranges {
			resf3elem := &svcsdktypes.Ipv6Range{}
			if resf3iter.CIDRIPv6 != nil {
				resf3elem.CidrIpv6 = resf3iter.CIDRIPv6
			}
			if resf3iter.Description != nil {
				resf3elem.Description = resf3iter.Description
			}
			resf3 = append(resf3, *resf3elem)
		}
		res.Ipv6Ranges = resf3
	}
	if c.PrefixListIDs != nil {
		resf4 := []svcsdktypes.PrefixListId{}
		for _, resf4iter := range c.PrefixListIDs {
			resf4elem := &svcsdktypes.PrefixListId{}
			if resf4iter.Description != nil {
				resf4elem.Description = resf4iter.Description
			}
			if resf4iter.PrefixListID != nil {
				resf4elem.PrefixListId = resf4iter.PrefixListID
			}
			resf4 = append(resf4, *resf4elem)
		}
		res.PrefixListIds = resf4
	}
	if c.ToPort != nil {
		toPortCopy0 := *c.ToPort
		if toPortCopy0 > math.MaxInt32 || toPortCopy0 < math.MinInt32 {
			return nil, fmt.Errorf("error: field ToPort is of type int32")
		}
		toPortCopy := int32(toPortCopy0)
		res.ToPort = &toPortCopy
	}
	if c.UserIDGroupPairs != nil {
		resf6 := []svcsdktypes.UserIdGroupPair{}
		for _, resf6iter := range c.UserIDGroupPairs {
			resf6elem := &svcsdktypes.UserIdGroupPair{}
			if resf6iter.Description != nil {
				resf6elem.Description = resf6iter.Description
			}
			if resf6iter.GroupID != nil {
				resf6elem.GroupId = resf6iter.GroupID
			}
			if resf6iter.GroupName != nil {
				resf6elem.GroupName = resf6iter.GroupName
			}
			if resf6iter.PeeringStatus != nil {
				resf6elem.PeeringStatus = resf6iter.PeeringStatus
			}
			if resf6iter.UserID != nil {
				resf6elem.UserId = resf6iter.UserID
			}
			if resf6iter.VPCID != nil {
				resf6elem.VpcId = resf6iter.VPCID
			}
			if resf6iter.VPCPeeringConnectionID != nil {
				resf6elem.VpcPeeringConnectionId = resf6iter.VPCPeeringConnectionID
			}
			resf6 = append(resf6, *resf6elem)
		}
		res.UserIdGroupPairs = resf6
	}

	return res, nil
}

// setIPPermission sets a resource IPPermission type
// given the SDK type.
func (rm *resourceManager) setResourceIPPermission(
	resp *svcsdktypes.IpPermission,
) *ackec2.IPPermission {
	res := &ackec2.IPPermission{}

	if resp.FromPort != nil {
		fromPortCopy := int64(*resp.FromPort)
		res.FromPort = &fromPortCopy
	}
	if resp.IpProtocol != nil {
		res.IPProtocol = resp.IpProtocol
	}
	if resp.IpRanges != nil {
		resf2 := []*ackec2.IPRange{}
		for _, resf2iter := range resp.IpRanges {
			resf2elem := &ackec2.IPRange{}
			if resf2iter.CidrIp != nil {
				resf2elem.CIDRIP = resf2iter.CidrIp
			}
			if resf2iter.Description != nil {
				resf2elem.Description = resf2iter.Description
			}
			resf2 = append(resf2, resf2elem)
		}
		res.IPRanges = resf2
	}
	if resp.Ipv6Ranges != nil {
		resf3 := []*ackec2.IPv6Range{}
		for _, resf3iter := range resp.Ipv6Ranges {
			resf3elem := &ackec2.IPv6Range{}
			if resf3iter.CidrIpv6 != nil {
				resf3elem.CIDRIPv6 = resf3iter.CidrIpv6
			}
			if resf3iter.Description != nil {
				resf3elem.Description = resf3iter.Description
			}
			resf3 = append(resf3, resf3elem)
		}
		res.IPv6Ranges = resf3
	}
	if resp.PrefixListIds != nil {
		resf4 := []*ackec2.PrefixListID{}
		for _, resf4iter := range resp.PrefixListIds {
			resf4elem := &ackec2.PrefixListID{}
			if resf4iter.Description != nil {
				resf4elem.Description = resf4iter.Description
			}
			if resf4iter.PrefixListId != nil {
				resf4elem.PrefixListID = resf4iter.PrefixListId
			}
			resf4 = append(resf4, resf4elem)
		}
		res.PrefixListIDs = resf4
	}
	if resp.ToPort != nil {
		toPortCopy := int64(*resp.ToPort)
		res.ToPort = &toPortCopy
	}
	if resp.UserIdGroupPairs != nil {
		resf6 := []*ackec2.UserIDGroupPair{}
		for _, resf6iter := range resp.UserIdGroupPairs {
			resf6elem := &ackec2.UserIDGroupPair{}
			if resf6iter.Description != nil {
				resf6elem.Description = resf6iter.Description
			}
			if resf6iter.GroupId != nil {
				resf6elem.GroupID = resf6iter.GroupId
			}
			if resf6iter.GroupName != nil {
				resf6elem.GroupName = resf6iter.GroupName
			}
			if resf6iter.PeeringStatus != nil {
				resf6elem.PeeringStatus = resf6iter.PeeringStatus
			}
			if resf6iter.UserId != nil {
				resf6elem.UserID = resf6iter.UserId
			}
			if resf6iter.VpcId != nil {
				resf6elem.VPCID = resf6iter.VpcId
			}
			if resf6iter.VpcPeeringConnectionId != nil {
				resf6elem.VPCPeeringConnectionID = resf6iter.VpcPeeringConnectionId
			}
			resf6 = append(resf6, resf6elem)
		}
		res.UserIDGroupPairs = resf6
	}

	return res
}
