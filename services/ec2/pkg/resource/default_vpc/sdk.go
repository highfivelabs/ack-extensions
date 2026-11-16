// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package default_vpc

import (
	"context"
	"fmt"
	"strings"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
	"github.com/highfivelabs/ack-extensions/services/ec2/pkg/tags"
)

// The region's default VPC is found, never made, and never deleted
// (Terraform's aws_default_vpc). Until an object has found it (no
// status.vpcID), its read finds nothing, so the runtime's create path takes
// the VPC over: the account-wide singleton pattern (docs/handoff.md).

func (rm *resourceManager) requiredFieldsMissingFromReadOneInput(r *resource) bool {
	return aws.ToString(r.ko.Status.VPCID) == ""
}

// describeVPC reads one VPC: by ID, or the region's default.
func (rm *resourceManager) describeVPC(ctx context.Context, vpcID string) (*svcsdktypes.Vpc, error) {
	input := &svcsdk.DescribeVpcsInput{}
	if vpcID != "" {
		input.VpcIds = []string{vpcID}
	} else {
		input.Filters = []svcsdktypes.Filter{{Name: aws.String("is-default"), Values: []string{"true"}}}
	}
	resp, err := rm.sdkapi.DescribeVpcs(ctx, input)
	rm.metrics.RecordAPICall("READ_ONE", "DescribeVpcs", err)
	if err != nil {
		return nil, err
	}
	for i := range resp.Vpcs {
		v := resp.Vpcs[i]
		if vpcID != "" && aws.ToString(v.VpcId) == vpcID {
			return &v, nil
		}
		if vpcID == "" && aws.ToBool(v.IsDefault) {
			return &v, nil
		}
	}
	return nil, nil
}

// attribute reads one of the VPC's boolean attributes.
func (rm *resourceManager) attribute(ctx context.Context, vpcID string, name svcsdktypes.VpcAttributeName) (*bool, error) {
	resp, err := rm.sdkapi.DescribeVpcAttribute(ctx, &svcsdk.DescribeVpcAttributeInput{VpcId: aws.String(vpcID), Attribute: name})
	rm.metrics.RecordAPICall("READ_ONE", "DescribeVpcAttribute", err)
	if err != nil {
		return nil, err
	}
	var v *svcsdktypes.AttributeBooleanValue
	switch name {
	case svcsdktypes.VpcAttributeNameEnableDnsSupport:
		v = resp.EnableDnsSupport
	case svcsdktypes.VpcAttributeNameEnableDnsHostnames:
		v = resp.EnableDnsHostnames
	case svcsdktypes.VpcAttributeNameEnableNetworkAddressUsageMetrics:
		v = resp.EnableNetworkAddressUsageMetrics
	}
	if v == nil {
		return aws.Bool(false), nil
	}
	return aws.Bool(aws.ToBool(v.Value)), nil
}

// observed is r with the VPC's settings and tags as EC2 has them. The
// Network Address Usage metrics setting is read only when the spec manages
// it; AWS's own tags (aws:) are left out.
func (rm *resourceManager) observed(ctx context.Context, r *resource, v *svcsdktypes.Vpc) (*resource, error) {
	ko := r.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	ko.Status.VPCID, ko.Status.CIDRBlock, ko.Status.DHCPOptionsID, ko.Status.OwnerID = v.VpcId, v.CidrBlock, v.DhcpOptionsId, v.OwnerId
	id := aws.ToString(v.VpcId)
	var err error
	if ko.Spec.EnableDNSSupport, err = rm.attribute(ctx, id, svcsdktypes.VpcAttributeNameEnableDnsSupport); err != nil {
		return nil, err
	}
	if ko.Spec.EnableDNSHostnames, err = rm.attribute(ctx, id, svcsdktypes.VpcAttributeNameEnableDnsHostnames); err != nil {
		return nil, err
	}
	if r.ko.Spec.EnableNetworkAddressUsageMetrics != nil {
		if ko.Spec.EnableNetworkAddressUsageMetrics, err = rm.attribute(ctx, id, svcsdktypes.VpcAttributeNameEnableNetworkAddressUsageMetrics); err != nil {
			return nil, err
		}
	}
	var t []*svcapitypes.Tag
	for _, tag := range v.Tags {
		if strings.HasPrefix(aws.ToString(tag.Key), "aws:") {
			continue
		}
		t = append(t, &svcapitypes.Tag{Key: tag.Key, Value: tag.Value})
	}
	ko.Spec.Tags = t
	rm.setARN(ko)
	return &resource{ko}, nil
}

// sdkFind reads the VPC taken over. A VPC gone (deleted outside
// Kubernetes) is not found, and the create path looks for the region's
// default again.
func (rm *resourceManager) sdkFind(ctx context.Context, r *resource) (latest *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkFind")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, ackerr.NotFound
	}
	v, err := rm.describeVPC(ctx, aws.ToString(r.ko.Status.VPCID))
	if err != nil {
		if isAPIError(err, "InvalidVpcID.NotFound") {
			return nil, ackerr.NotFound
		}
		return nil, err
	}
	if v == nil || !aws.ToBool(v.IsDefault) {
		return nil, ackerr.NotFound
	}
	return rm.observed(ctx, r, v)
}

// setARN names the VPC as EC2 does (arn:aws:ec2:region:account:vpc/vpc-...).
func (rm *resourceManager) setARN(ko *svcapitypes.DefaultVPC) {
	arn := ackv1alpha1.AWSResourceName(rm.ARNFromName("vpc/" + aws.ToString(ko.Status.VPCID)))
	ko.Status.ACKResourceMetadata.ARN = &arn
}

// sync makes the VPC's settings and tags the spec's.
func (rm *resourceManager) sync(ctx context.Context, want *svcapitypes.DefaultVPC, current *resource) error {
	id := aws.ToString(want.Status.VPCID)
	for _, a := range []struct {
		want, have *bool
		set        func(*svcsdk.ModifyVpcAttributeInput, *svcsdktypes.AttributeBooleanValue)
	}{
		{want.Spec.EnableDNSSupport, current.ko.Spec.EnableDNSSupport, func(in *svcsdk.ModifyVpcAttributeInput, v *svcsdktypes.AttributeBooleanValue) {
			in.EnableDnsSupport = v
		}},
		{want.Spec.EnableDNSHostnames, current.ko.Spec.EnableDNSHostnames, func(in *svcsdk.ModifyVpcAttributeInput, v *svcsdktypes.AttributeBooleanValue) {
			in.EnableDnsHostnames = v
		}},
		{want.Spec.EnableNetworkAddressUsageMetrics, current.ko.Spec.EnableNetworkAddressUsageMetrics, func(in *svcsdk.ModifyVpcAttributeInput, v *svcsdktypes.AttributeBooleanValue) {
			in.EnableNetworkAddressUsageMetrics = v
		}},
	} {
		if a.want == nil || aws.ToBool(a.want) == aws.ToBool(a.have) {
			continue
		}
		// ModifyVpcAttribute takes one attribute per call.
		in := &svcsdk.ModifyVpcAttributeInput{VpcId: aws.String(id)}
		a.set(in, &svcsdktypes.AttributeBooleanValue{Value: a.want})
		_, err := rm.sdkapi.ModifyVpcAttribute(ctx, in)
		rm.metrics.RecordAPICall("UPDATE", "ModifyVpcAttribute", err)
		if err != nil {
			return err
		}
	}
	if !tags.Equal(want.Spec.Tags, current.ko.Spec.Tags) {
		return tags.Sync(ctx, rm.sdkapi, rm.metrics, id, want.Spec.Tags, current.ko.Spec.Tags)
	}
	return nil
}

// sdkCreate takes the region's default VPC over: finds it, then makes its
// settings and tags the spec's. No default VPC is retried.
func (rm *resourceManager) sdkCreate(ctx context.Context, desired *resource) (created *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkCreate")
	defer func() { exit(err) }()
	v, err := rm.describeVPC(ctx, "")
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, fmt.Errorf("region %s has no default VPC; make one (CreateDefaultVpc) and this object takes it over", rm.awsRegion)
	}
	current, err := rm.observed(ctx, desired, v)
	if err != nil {
		return nil, err
	}
	want := desired.ko.DeepCopy()
	rm.setStatusDefaults(want)
	want.Status = current.ko.Status
	if err := rm.sync(ctx, want, current); err != nil {
		return nil, err
	}
	return &resource{want}, nil
}

// sdkUpdate makes the settings and tags the spec's again.
func (rm *resourceManager) sdkUpdate(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta interface{},
) (updated *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkUpdate")
	defer func() { exit(err) }()
	want := desired.ko.DeepCopy()
	rm.setStatusDefaults(want)
	want.Status.VPCID, want.Status.CIDRBlock, want.Status.DHCPOptionsID, want.Status.OwnerID =
		latest.ko.Status.VPCID, latest.ko.Status.CIDRBlock, latest.ko.Status.DHCPOptionsID, latest.ko.Status.OwnerID
	rm.setARN(want)
	if err := rm.sync(ctx, want, latest); err != nil {
		return nil, err
	}
	return &resource{want}, nil
}

// sdkDelete leaves the VPC as it is: a default VPC is not Kubernetes' to
// delete (Terraform's aws_default_vpc deletes it only with force_destroy,
// which is not offered here).
func (rm *resourceManager) sdkDelete(ctx context.Context, r *resource) (deleted *resource, err error) {
	return nil, nil
}

// terminalAWSError: errors no retry fixes. No default VPC yet is not
// terminal (one may be made after this object).
func (rm *resourceManager) terminalAWSError(err error) bool {
	return isAPIError(err, "InvalidParameterValue", "InvalidParameter", "InvalidParameterCombination", "TagLimitExceeded", "UnsupportedOperation")
}
