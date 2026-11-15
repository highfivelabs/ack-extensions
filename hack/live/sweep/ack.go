// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

//go:build live

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/test/live"
)

// ackRegions are where the ACK-kind tests make things: the run's region,
// the only one the sandbox account's organization allows (live.Region).
var ackRegions = []string{live.Region()}

// leftover is one thing an ACK-kind live run made.
type leftover struct {
	kind, region, id, run string
	del                   func(context.Context) error
}

// ackSweep is the ACK-kind tests' pass (task test-live SUITE=ack): what the
// managers make carries the run tag (ACK's --resource-tags), and the
// billable or lasting kinds are found by it and deleted here: EC2 volumes,
// customer gateways, VPN gateways and connections, and KMS keys (scheduled
// for deletion, 7 days). Every other kind is deleted by its test; the
// tests' own fixtures are untagged and deleted by the tests too. The IAM
// things the tests make are listed (IAM's lists carry no tags) so that a
// leftover shows in left.txt.
func ackSweep(ctx context.Context, cfg aws.Config, run string, list bool, older time.Duration) (found, deleted, failed int) {
	now := time.Now().UTC()
	var all []leftover
	for _, region := range ackRegions {
		c := cfg.Copy()
		c.Region = region
		got, err := ackLeftovers(ctx, c, region)
		if err != nil {
			fmt.Printf("sweep: ack %s: %v\n", region, err)
			failed++
		}
		all = append(all, got...)
	}
	for _, l := range all {
		found++
		due := run != "" && l.run == run
		if run == "" {
			if t, ok := started(l.run, now); ok && now.Sub(t) > older {
				due = true
			}
		}
		fmt.Printf("sweep: %s %s %s (run %s)%s\n", l.kind, l.region, l.id, l.run, map[bool]string{true: ": deleting", false: ""}[due && !list])
		if !due || list {
			continue
		}
		if err := l.del(ctx); err != nil {
			fmt.Printf("sweep: %s %s: %v\n", l.kind, l.id, err)
			failed++
			continue
		}
		deleted++
	}
	if list {
		listIAM(ctx, cfg)
	}
	return found, deleted, failed
}

func ec2Run(tags []ec2types.Tag) string {
	for _, t := range tags {
		if aws.ToString(t.Key) == live.RunTag {
			return aws.ToString(t.Value)
		}
	}
	return ""
}

func ackLeftovers(ctx context.Context, cfg aws.Config, region string) ([]leftover, error) {
	e := ec2.NewFromConfig(cfg)
	byTag := []ec2types.Filter{{Name: aws.String("tag-key"), Values: []string{live.RunTag}}}
	var out []leftover
	var errs []error
	vols, err := e.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{Filters: byTag})
	errs = append(errs, err)
	if err == nil {
		for _, v := range vols.Volumes {
			id := aws.ToString(v.VolumeId)
			out = append(out, leftover{"ec2 Volume", region, id, ec2Run(v.Tags), func(ctx context.Context) error {
				for _, a := range v.Attachments {
					_, _ = e.DetachVolume(ctx, &ec2.DetachVolumeInput{VolumeId: aws.String(id), InstanceId: a.InstanceId, Force: aws.Bool(true)})
				}
				return retry(ctx, func() error {
					_, err := e.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(id)})
					return err
				})
			}})
		}
	}
	vpns, err := e.DescribeVpnConnections(ctx, &ec2.DescribeVpnConnectionsInput{Filters: byTag})
	errs = append(errs, err)
	if err == nil {
		for _, v := range vpns.VpnConnections {
			if v.State == ec2types.VpnStateDeleted {
				continue
			}
			id := aws.ToString(v.VpnConnectionId)
			out = append(out, leftover{"ec2 VPNConnection", region, id, ec2Run(v.Tags), func(ctx context.Context) error {
				_, err := e.DeleteVpnConnection(ctx, &ec2.DeleteVpnConnectionInput{VpnConnectionId: aws.String(id)})
				return err
			}})
		}
	}
	vgws, err := e.DescribeVpnGateways(ctx, &ec2.DescribeVpnGatewaysInput{Filters: byTag})
	errs = append(errs, err)
	if err == nil {
		for _, g := range vgws.VpnGateways {
			if g.State == ec2types.VpnStateDeleted {
				continue
			}
			id := aws.ToString(g.VpnGatewayId)
			out = append(out, leftover{"ec2 VPNGateway", region, id, ec2Run(g.Tags), func(ctx context.Context) error {
				for _, a := range g.VpcAttachments {
					if a.State == ec2types.AttachmentStatusAttached || a.State == ec2types.AttachmentStatusAttaching {
						_, _ = e.DetachVpnGateway(ctx, &ec2.DetachVpnGatewayInput{VpnGatewayId: aws.String(id), VpcId: a.VpcId})
					}
				}
				return retry(ctx, func() error {
					_, err := e.DeleteVpnGateway(ctx, &ec2.DeleteVpnGatewayInput{VpnGatewayId: aws.String(id)})
					return err
				})
			}})
		}
	}
	cgws, err := e.DescribeCustomerGateways(ctx, &ec2.DescribeCustomerGatewaysInput{Filters: byTag})
	errs = append(errs, err)
	if err == nil {
		for _, g := range cgws.CustomerGateways {
			if aws.ToString(g.State) == "deleted" {
				continue
			}
			id := aws.ToString(g.CustomerGatewayId)
			out = append(out, leftover{"ec2 CustomerGateway", region, id, ec2Run(g.Tags), func(ctx context.Context) error {
				return retry(ctx, func() error {
					_, err := e.DeleteCustomerGateway(ctx, &ec2.DeleteCustomerGatewayInput{CustomerGatewayId: aws.String(id)})
					return err
				})
			}})
		}
	}
	k := kms.NewFromConfig(cfg)
	p := kms.NewListKeysPaginator(k, &kms.ListKeysInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			errs = append(errs, err)
			break
		}
		for _, key := range page.Keys {
			id := aws.ToString(key.KeyId)
			tags, err := k.ListResourceTags(ctx, &kms.ListResourceTagsInput{KeyId: aws.String(id)})
			if err != nil {
				continue // AWS-managed keys and keys of other principals
			}
			run := ""
			for _, t := range tags.Tags {
				if aws.ToString(t.TagKey) == live.RunTag {
					run = aws.ToString(t.TagValue)
				}
			}
			if run == "" {
				continue
			}
			d, err := k.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(id)})
			if err != nil || d.KeyMetadata.KeyState == kmstypes.KeyStatePendingDeletion || d.KeyMetadata.KeyState == kmstypes.KeyStatePendingReplicaDeletion {
				continue
			}
			out = append(out, leftover{"kms Key", cfg.Region, id, run, func(ctx context.Context) error {
				_, err := k.ScheduleKeyDeletion(ctx, &kms.ScheduleKeyDeletionInput{KeyId: aws.String(id), PendingWindowInDays: aws.Int32(7)})
				return err
			}})
		}
	}
	return out, errors.Join(errs...)
}

// retry retries a delete that EC2 refuses while a dependency is going
// away (a detaching volume or gateway).
func retry(ctx context.Context, f func() error) error {
	var err error
	for i := 0; i < 30; i++ {
		if err = f(); err == nil || strings.Contains(err.Error(), "NotFound") {
			return nil
		}
		time.Sleep(10 * time.Second)
	}
	return err
}

// listIAM prints the IAM users, groups, customer-managed policies,
// virtual MFA devices, account alias and password policy: the sandbox
// account has none of its own, so any is a test's leftover. Roles are
// left out (the account's own roles live there).
func listIAM(ctx context.Context, cfg aws.Config) {
	c := iam.NewFromConfig(cfg)
	if out, err := c.ListUsers(ctx, &iam.ListUsersInput{}); err == nil {
		for _, u := range out.Users {
			fmt.Printf("sweep: iam User %s (made by a test?)\n", aws.ToString(u.UserName))
		}
	} else {
		fmt.Printf("sweep: iam ListUsers: %v\n", err)
	}
	if out, err := c.ListGroups(ctx, &iam.ListGroupsInput{}); err == nil {
		for _, g := range out.Groups {
			fmt.Printf("sweep: iam Group %s (made by a test?)\n", aws.ToString(g.GroupName))
		}
	}
	if out, err := c.ListPolicies(ctx, &iam.ListPoliciesInput{Scope: "Local"}); err == nil {
		for _, p := range out.Policies {
			if !strings.HasPrefix(aws.ToString(p.PolicyName), "ack-extensions-live-tests-") {
				fmt.Printf("sweep: iam Policy %s (made by a test?)\n", aws.ToString(p.PolicyName))
			}
		}
	}
	if out, err := c.ListVirtualMFADevices(ctx, &iam.ListVirtualMFADevicesInput{}); err == nil {
		for _, d := range out.VirtualMFADevices {
			fmt.Printf("sweep: iam VirtualMFADevice %s (made by a test?)\n", aws.ToString(d.SerialNumber))
		}
	}
	if out, err := c.ListAccountAliases(ctx, &iam.ListAccountAliasesInput{}); err == nil {
		for _, a := range out.AccountAliases {
			fmt.Printf("sweep: iam AccountAlias %s (made by a test?)\n", a)
		}
	}
	if _, err := c.GetAccountPasswordPolicy(ctx, &iam.GetAccountPasswordPolicyInput{}); err == nil {
		fmt.Printf("sweep: iam AccountPasswordPolicy is set (made by a test?)\n")
	}
}
