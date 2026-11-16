// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package default_network_acl

import (
	"context"
	"fmt"
	"sort"
	"strings"

	ackec2 "github.com/aws-controllers-k8s/ec2-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
	"github.com/highfivelabs/ack-extensions/services/ec2/pkg/tags"
)

// A VPC's default network ACL is found, never made, and never deleted
// (Terraform's aws_default_network_acl). Until an object has found it (no
// status.id), its read finds nothing, so the runtime's create path takes
// the ACL over: the account-wide singleton pattern (docs/handoff.md).

func (rm *resourceManager) requiredFieldsMissingFromReadOneInput(r *resource) bool {
	return aws.ToString(r.ko.Status.ID) == ""
}

// describeACL reads one network ACL: by ID, or the VPC's default.
func (rm *resourceManager) describeACL(ctx context.Context, aclID, vpcID string) (*svcsdktypes.NetworkAcl, error) {
	input := &svcsdk.DescribeNetworkAclsInput{}
	if aclID != "" {
		input.NetworkAclIds = []string{aclID}
	} else {
		input.Filters = []svcsdktypes.Filter{
			{Name: aws.String("vpc-id"), Values: []string{vpcID}},
			{Name: aws.String("default"), Values: []string{"true"}},
		}
	}
	resp, err := rm.sdkapi.DescribeNetworkAcls(ctx, input)
	rm.metrics.RecordAPICall("READ_ONE", "DescribeNetworkAcls", err)
	if err != nil {
		return nil, err
	}
	for i := range resp.NetworkAcls {
		a := resp.NetworkAcls[i]
		if aclID != "" && aws.ToString(a.NetworkAclId) == aclID {
			return &a, nil
		}
		if aclID == "" && aws.ToBool(a.IsDefault) && aws.ToString(a.VpcId) == vpcID {
			return &a, nil
		}
	}
	return nil, nil
}

// observed is r with the ACL's entries and tags as EC2 has them, the
// listed subnets that are on it, and the subnets on it the spec does not
// list (status). AWS's own tags (aws:) are left out.
func (rm *resourceManager) observed(r *resource, a *svcsdktypes.NetworkAcl) *resource {
	ko := r.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	ko.Status.ID = a.NetworkAclId
	var entries []*ackec2.NetworkACLEntry
	for _, e := range a.Entries {
		entries = append(entries, entryFromSDK(e))
	}
	ko.Spec.Entries = filterDefaultRules(entries)
	on := map[string]bool{}
	for _, as := range a.Associations {
		on[aws.ToString(as.SubnetId)] = true
	}
	listed := map[string]bool{}
	var subnets []*string
	for _, s := range r.ko.Spec.SubnetIDs {
		listed[aws.ToString(s)] = true
		if on[aws.ToString(s)] {
			subnets = append(subnets, s)
		}
	}
	ko.Spec.SubnetIDs = subnets
	var unlisted []string
	for s := range on {
		if s != "" && !listed[s] {
			unlisted = append(unlisted, s)
		}
	}
	sort.Strings(unlisted)
	ko.Status.UnlistedSubnetIDs = aws.StringSlice(unlisted)
	var t []*svcapitypes.Tag
	for _, tag := range a.Tags {
		if strings.HasPrefix(aws.ToString(tag.Key), "aws:") {
			continue
		}
		t = append(t, &svcapitypes.Tag{Key: tag.Key, Value: tag.Value})
	}
	ko.Spec.Tags = t
	rm.setARN(ko)
	return &resource{ko}
}

// sdkFind reads the ACL taken over. An ACL gone (its VPC deleted) is not
// found.
func (rm *resourceManager) sdkFind(ctx context.Context, r *resource) (latest *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkFind")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, ackerr.NotFound
	}
	a, err := rm.describeACL(ctx, aws.ToString(r.ko.Status.ID), "")
	if err != nil {
		if isAPIError(err, "InvalidNetworkAclID.NotFound") {
			return nil, ackerr.NotFound
		}
		return nil, err
	}
	if a == nil {
		return nil, ackerr.NotFound
	}
	return rm.observed(r, a), nil
}

// setARN names the ACL as EC2 does
// (arn:aws:ec2:region:account:network-acl/acl-...).
func (rm *resourceManager) setARN(ko *svcapitypes.DefaultNetworkACL) {
	arn := ackv1alpha1.AWSResourceName(rm.ARNFromName("network-acl/" + aws.ToString(ko.Status.ID)))
	ko.Status.ACKResourceMetadata.ARN = &arn
}

// associate moves each listed subnet not yet on the ACL onto it, from
// whatever ACL it is on (ReplaceNetworkAclAssociation).
func (rm *resourceManager) associate(ctx context.Context, aclID string, want, have []*string) error {
	on := map[string]bool{}
	for _, s := range have {
		on[aws.ToString(s)] = true
	}
	for _, s := range want {
		subnet := aws.ToString(s)
		if on[subnet] {
			continue
		}
		resp, err := rm.sdkapi.DescribeNetworkAcls(ctx, &svcsdk.DescribeNetworkAclsInput{Filters: []svcsdktypes.Filter{
			{Name: aws.String("association.subnet-id"), Values: []string{subnet}},
		}})
		rm.metrics.RecordAPICall("READ_MANY", "DescribeNetworkAcls", err)
		if err != nil {
			return err
		}
		associationID := ""
		for _, acl := range resp.NetworkAcls {
			for _, as := range acl.Associations {
				if aws.ToString(as.SubnetId) == subnet {
					associationID = aws.ToString(as.NetworkAclAssociationId)
				}
			}
		}
		if associationID == "" {
			// The subnet is not there yet (or not in this VPC): retried.
			return fmt.Errorf("subnet %s has no network ACL association", subnet)
		}
		_, err = rm.sdkapi.ReplaceNetworkAclAssociation(ctx, &svcsdk.ReplaceNetworkAclAssociationInput{
			AssociationId: aws.String(associationID), NetworkAclId: aws.String(aclID),
		})
		rm.metrics.RecordAPICall("UPDATE", "ReplaceNetworkAclAssociation", err)
		if err != nil {
			return err
		}
	}
	return nil
}

// sync makes the ACL's entries, subnets and tags the spec's.
func (rm *resourceManager) sync(ctx context.Context, want *svcapitypes.DefaultNetworkACL, current *resource) error {
	id := aws.ToString(want.Status.ID)
	if !entriesEqual(want.Spec.Entries, current.ko.Spec.Entries) {
		if err := rm.syncEntries(ctx, id, want.Spec.Entries, current.ko.Spec.Entries); err != nil {
			return err
		}
	}
	if err := rm.associate(ctx, id, want.Spec.SubnetIDs, current.ko.Spec.SubnetIDs); err != nil {
		return err
	}
	if !tags.Equal(want.Spec.Tags, current.ko.Spec.Tags) {
		return tags.Sync(ctx, rm.sdkapi, rm.metrics, id, want.Spec.Tags, current.ko.Spec.Tags)
	}
	return nil
}

// sdkCreate takes the VPC's default ACL over: finds it, then makes its
// entries, subnets and tags the spec's, deleting every other entry (EC2's
// default allow-all entries too) and tag.
func (rm *resourceManager) sdkCreate(ctx context.Context, desired *resource) (created *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkCreate")
	defer func() { exit(err) }()
	vpcID := aws.ToString(desired.ko.Spec.VPCID)
	if vpcID == "" {
		return nil, ackerr.NewTerminalError(fmt.Errorf("spec.vpcID or spec.vpcRef is required"))
	}
	a, err := rm.describeACL(ctx, "", vpcID)
	if err != nil {
		return nil, err
	}
	if a == nil {
		// The VPC is not there yet: retried.
		return nil, fmt.Errorf("VPC %s has no default network ACL", vpcID)
	}
	current := rm.observed(desired, a)
	want := desired.ko.DeepCopy()
	rm.setStatusDefaults(want)
	want.Status.ID, want.Status.UnlistedSubnetIDs = a.NetworkAclId, current.ko.Status.UnlistedSubnetIDs
	rm.setARN(want)
	if err := rm.sync(ctx, want, current); err != nil {
		return nil, err
	}
	return &resource{want}, nil
}

// sdkUpdate makes the entries, subnets and tags the spec's again. The VPC
// is immutable (the CRD refuses a change).
func (rm *resourceManager) sdkUpdate(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta interface{},
) (updated *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkUpdate")
	defer func() { exit(err) }()
	if aws.ToString(desired.ko.Spec.VPCID) != aws.ToString(latest.ko.Spec.VPCID) {
		return nil, ackerr.NewTerminalError(fmt.Errorf("a DefaultNetworkACL's VPC cannot change; delete it and create another"))
	}
	want := desired.ko.DeepCopy()
	rm.setStatusDefaults(want)
	want.Status.ID, want.Status.UnlistedSubnetIDs = latest.ko.Status.ID, latest.ko.Status.UnlistedSubnetIDs
	rm.setARN(want)
	if err := rm.sync(ctx, want, latest); err != nil {
		return nil, err
	}
	return &resource{want}, nil
}

// sdkDelete leaves the ACL, its entries and its subnets as they are: EC2
// deletes a default ACL only with its VPC.
func (rm *resourceManager) sdkDelete(ctx context.Context, r *resource) (deleted *resource, err error) {
	return nil, nil
}

// terminalAWSError: errors no retry fixes. A VPC or subnet not there yet
// is not terminal; a malformed entry, or an ACL past its entry limit,
// needs a person.
func (rm *resourceManager) terminalAWSError(err error) bool {
	return isAPIError(err, "InvalidParameterValue", "InvalidParameter", "InvalidParameterCombination", "NetworkAclEntryLimitExceeded",
		"NetworkAclEntryAlreadyExists", "TagLimitExceeded")
}
