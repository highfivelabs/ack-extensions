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
// aws-controllers-k8s/ec2-controller v1.23.1, pkg/resource/network_acl/hooks.go
// (syncEntries, createEntry, updateEntry, deleteEntry, the 32767 filter) and
// sdk.go (reading entries).
// Changes: package; the entries compared by a canonical key (protocol names
// and numbers, rule action case, ports and ICMP fields only where the
// protocol has them) instead of the generated compareNetworkACLEntry; one
// builder for the create and replace inputs.

package default_network_acl

import (
	"context"
	"errors"
	"fmt"
	"strings"

	ackec2 "github.com/aws-controllers-k8s/ec2-controller/apis/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// DefaultRuleNumber is EC2's deny-all entry in each direction, which
// cannot be changed or deleted.
const DefaultRuleNumber = 32767

// protocolNumber gives a protocol as EC2 reads it back (a number; -1 for
// all).
func protocolNumber(p string) string {
	switch strings.ToLower(p) {
	case "tcp":
		return "6"
	case "udp":
		return "17"
	case "icmp":
		return "1"
	case "icmpv6":
		return "58"
	case "all", "":
		return "-1"
	}
	return p
}

// entryKey is an entry in a canonical form: two entries with one key are
// the same entry.
func entryKey(e *ackec2.NetworkACLEntry) string {
	proto := protocolNumber(aws.ToString(e.Protocol))
	ports, icmp := "", ""
	if (proto == "6" || proto == "17") && e.PortRange != nil {
		ports = fmt.Sprintf("%d-%d", aws.ToInt64(e.PortRange.From), aws.ToInt64(e.PortRange.To))
	}
	if (proto == "1" || proto == "58") && e.ICMPTypeCode != nil {
		icmp = fmt.Sprintf("%d/%d", aws.ToInt64(e.ICMPTypeCode.Type), aws.ToInt64(e.ICMPTypeCode.Code))
	}
	return fmt.Sprintf("%t|%d|%s|%s|%s|%s|%s|%s", aws.ToBool(e.Egress), aws.ToInt64(e.RuleNumber), proto,
		strings.ToLower(aws.ToString(e.RuleAction)), aws.ToString(e.CIDRBlock), aws.ToString(e.IPv6CIDRBlock), ports, icmp)
}

// slotKey is an entry's place: its direction and rule number.
func slotKey(e *ackec2.NetworkACLEntry) string {
	return fmt.Sprintf("%t|%d", aws.ToBool(e.Egress), aws.ToInt64(e.RuleNumber))
}

// filterDefaultRules leaves out EC2's deny-all entries (32767).
func filterDefaultRules(entries []*ackec2.NetworkACLEntry) []*ackec2.NetworkACLEntry {
	out := []*ackec2.NetworkACLEntry{}
	for _, e := range entries {
		if e != nil && aws.ToInt64(e.RuleNumber) == DefaultRuleNumber {
			continue
		}
		out = append(out, e)
	}
	return out
}

// entriesEqual: the same entries, whatever the order.
func entriesEqual(a, b []*ackec2.NetworkACLEntry) bool {
	a, b = filterDefaultRules(a), filterDefaultRules(b)
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, e := range a {
		seen[entryKey(e)]++
	}
	for _, e := range b {
		k := entryKey(e)
		if seen[k] == 0 {
			return false
		}
		seen[k]--
	}
	return true
}

// entryFromSDK reads an entry as ACK's NetworkACL does.
func entryFromSDK(e svcsdktypes.NetworkAclEntry) *ackec2.NetworkACLEntry {
	out := &ackec2.NetworkACLEntry{
		CIDRBlock:     e.CidrBlock,
		Egress:        e.Egress,
		IPv6CIDRBlock: e.Ipv6CidrBlock,
		Protocol:      e.Protocol,
	}
	if e.RuleAction != "" {
		out.RuleAction = aws.String(string(e.RuleAction))
	}
	if e.RuleNumber != nil {
		out.RuleNumber = aws.Int64(int64(*e.RuleNumber))
	}
	if e.PortRange != nil {
		out.PortRange = &ackec2.PortRange{}
		if e.PortRange.From != nil {
			out.PortRange.From = aws.Int64(int64(*e.PortRange.From))
		}
		if e.PortRange.To != nil {
			out.PortRange.To = aws.Int64(int64(*e.PortRange.To))
		}
	}
	if e.IcmpTypeCode != nil {
		out.ICMPTypeCode = &ackec2.ICMPTypeCode{}
		if e.IcmpTypeCode.Code != nil {
			out.ICMPTypeCode.Code = aws.Int64(int64(*e.IcmpTypeCode.Code))
		}
		if e.IcmpTypeCode.Type != nil {
			out.ICMPTypeCode.Type = aws.Int64(int64(*e.IcmpTypeCode.Type))
		}
	}
	return out
}

// entryInput is the fields CreateNetworkAclEntry and ReplaceNetworkAclEntry
// share.
type entryInput struct {
	cidr, ipv6, protocol *string
	egress               *bool
	icmp                 *svcsdktypes.IcmpTypeCode
	ports                *svcsdktypes.PortRange
	action               svcsdktypes.RuleAction
	number               *int32
}

func newEntryInput(c *ackec2.NetworkACLEntry) entryInput {
	in := entryInput{cidr: c.CIDRBlock, ipv6: c.IPv6CIDRBlock, protocol: c.Protocol, egress: aws.Bool(aws.ToBool(c.Egress))}
	if c.ICMPTypeCode != nil {
		in.icmp = &svcsdktypes.IcmpTypeCode{}
		if c.ICMPTypeCode.Code != nil {
			in.icmp.Code = aws.Int32(int32(*c.ICMPTypeCode.Code))
		}
		if c.ICMPTypeCode.Type != nil {
			in.icmp.Type = aws.Int32(int32(*c.ICMPTypeCode.Type))
		}
	}
	if c.PortRange != nil {
		in.ports = &svcsdktypes.PortRange{}
		if c.PortRange.From != nil {
			in.ports.From = aws.Int32(int32(*c.PortRange.From))
		}
		if c.PortRange.To != nil {
			in.ports.To = aws.Int32(int32(*c.PortRange.To))
		}
	}
	if c.RuleAction != nil {
		in.action = svcsdktypes.RuleAction(strings.ToLower(*c.RuleAction))
	}
	if c.RuleNumber != nil {
		in.number = aws.Int32(int32(*c.RuleNumber))
	}
	return in
}

// syncEntries makes the ACL's entries the spec's: an entry at a place
// (direction and rule number) the spec does not use is deleted, one the
// spec changes is replaced, a new one is created.
func (rm *resourceManager) syncEntries(ctx context.Context, aclID string, desired, latest []*ackec2.NetworkACLEntry) error {
	desired, latest = filterDefaultRules(desired), filterDefaultRules(latest)
	want := map[string]*ackec2.NetworkACLEntry{}
	for _, e := range desired {
		if e == nil || e.RuleNumber == nil {
			return errors.New("every entry needs a ruleNumber")
		}
		k := slotKey(e)
		if _, dup := want[k]; dup {
			return fmt.Errorf("two entries with egress %t and rule number %d", aws.ToBool(e.Egress), aws.ToInt64(e.RuleNumber))
		}
		want[k] = e
	}
	have := map[string]*ackec2.NetworkACLEntry{}
	for _, e := range latest {
		have[slotKey(e)] = e
	}
	for k, e := range have {
		if _, ok := want[k]; ok {
			continue
		}
		_, err := rm.sdkapi.DeleteNetworkAclEntry(ctx, &svcsdk.DeleteNetworkAclEntryInput{
			NetworkAclId: aws.String(aclID), Egress: aws.Bool(aws.ToBool(e.Egress)), RuleNumber: aws.Int32(int32(aws.ToInt64(e.RuleNumber))),
		})
		rm.metrics.RecordAPICall("UPDATE", "DeleteNetworkAclEntry", err)
		if err != nil {
			return err
		}
	}
	for k, e := range want {
		old, ok := have[k]
		if ok && entryKey(old) == entryKey(e) {
			continue
		}
		in := newEntryInput(e)
		var err error
		if ok {
			_, err = rm.sdkapi.ReplaceNetworkAclEntry(ctx, &svcsdk.ReplaceNetworkAclEntryInput{
				NetworkAclId: aws.String(aclID), CidrBlock: in.cidr, Ipv6CidrBlock: in.ipv6, Protocol: in.protocol, Egress: in.egress,
				IcmpTypeCode: in.icmp, PortRange: in.ports, RuleAction: in.action, RuleNumber: in.number,
			})
			rm.metrics.RecordAPICall("UPDATE", "ReplaceNetworkAclEntry", err)
		} else {
			_, err = rm.sdkapi.CreateNetworkAclEntry(ctx, &svcsdk.CreateNetworkAclEntryInput{
				NetworkAclId: aws.String(aclID), CidrBlock: in.cidr, Ipv6CidrBlock: in.ipv6, Protocol: in.protocol, Egress: in.egress,
				IcmpTypeCode: in.icmp, PortRange: in.ports, RuleAction: in.action, RuleNumber: in.number,
			})
			rm.metrics.RecordAPICall("UPDATE", "CreateNetworkAclEntry", err)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
