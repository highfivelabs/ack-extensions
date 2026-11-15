// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package default_route_table

import (
	"context"
	"fmt"
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

// A VPC's main route table is found, never made, and never deleted
// (Terraform's aws_default_route_table). Until an object has found it (no
// status.id), its read finds nothing, so the runtime's create path takes
// the table over: the account-wide singleton pattern (docs/handoff.md).

func (rm *resourceManager) requiredFieldsMissingFromReadOneInput(r *resource) bool {
	return aws.ToString(r.ko.Status.ID) == ""
}

// describeTable reads one route table: by ID, or the VPC's main table.
func (rm *resourceManager) describeTable(ctx context.Context, tableID, vpcID string) (*svcsdktypes.RouteTable, error) {
	input := &svcsdk.DescribeRouteTablesInput{}
	if tableID != "" {
		input.RouteTableIds = []string{tableID}
	} else {
		input.Filters = []svcsdktypes.Filter{
			{Name: aws.String("vpc-id"), Values: []string{vpcID}},
			{Name: aws.String("association.main"), Values: []string{"true"}},
		}
	}
	resp, err := rm.sdkapi.DescribeRouteTables(ctx, input)
	rm.metrics.RecordAPICall("READ_ONE", "DescribeRouteTables", err)
	if err != nil {
		return nil, err
	}
	for i := range resp.RouteTables {
		t := resp.RouteTables[i]
		if tableID != "" && aws.ToString(t.RouteTableId) == tableID {
			return &t, nil
		}
		if tableID != "" || aws.ToString(t.VpcId) != vpcID {
			continue
		}
		for _, a := range t.Associations {
			if aws.ToBool(a.Main) {
				return &t, nil
			}
		}
	}
	return nil, nil
}

// observed is r with the table's routes, propagating gateways and tags as
// EC2 has them. A route the spec lists keeps the spec's form (its
// references); AWS's own tags (aws:) are left out.
func (rm *resourceManager) observed(r *resource, t *svcsdktypes.RouteTable) *resource {
	ko := r.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	ko.Status.ID = t.RouteTableId
	var routes []*ackec2.CreateRouteInput
	for _, sr := range t.Routes {
		route := routeFromSDK(sr)
		if route == nil {
			continue
		}
		for _, want := range r.ko.Spec.Routes {
			if want != nil && len(compareCreateRouteInput(want, route).Differences) == 0 {
				route = want.DeepCopy()
				break
			}
		}
		routes = append(routes, route)
	}
	ko.Spec.Routes = routes
	var vgws []*string
	for _, p := range t.PropagatingVgws {
		vgws = append(vgws, p.GatewayId)
	}
	ko.Spec.PropagatingVGWs = vgws
	var tg []*svcapitypes.Tag
	for _, tag := range t.Tags {
		if strings.HasPrefix(aws.ToString(tag.Key), "aws:") {
			continue
		}
		tg = append(tg, &svcapitypes.Tag{Key: tag.Key, Value: tag.Value})
	}
	ko.Spec.Tags = tg
	rm.setARN(ko)
	return &resource{ko}
}

// sdkFind reads the table taken over. A table gone (its VPC deleted) is not
// found.
func (rm *resourceManager) sdkFind(ctx context.Context, r *resource) (latest *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkFind")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, ackerr.NotFound
	}
	t, err := rm.describeTable(ctx, aws.ToString(r.ko.Status.ID), "")
	if err != nil {
		if isAPIError(err, "InvalidRouteTableID.NotFound") {
			return nil, ackerr.NotFound
		}
		return nil, err
	}
	if t == nil {
		return nil, ackerr.NotFound
	}
	return rm.observed(r, t), nil
}

// setARN names the table as EC2 does
// (arn:aws:ec2:region:account:route-table/rtb-...).
func (rm *resourceManager) setARN(ko *svcapitypes.DefaultRouteTable) {
	arn := ackv1alpha1.AWSResourceName(rm.ARNFromName("route-table/" + aws.ToString(ko.Status.ID)))
	ko.Status.ACKResourceMetadata.ARN = &arn
}

// routesDiffer: the spec's routes and the table's are not the same set.
func routesDiffer(a, b []*ackec2.CreateRouteInput) bool {
	add, del := getRoutesDifference(a, b)
	return len(add) > 0 || len(del) > 0
}

// sync makes the table's routes, propagating gateways and tags the spec's.
// Routes are deleted before they are created, so a route whose target
// changes is replaced.
func (rm *resourceManager) sync(ctx context.Context, want *svcapitypes.DefaultRouteTable, current *resource) error {
	id := aws.ToString(want.Status.ID)
	toAdd, toDelete := getRoutesDifference(want.Spec.Routes, current.ko.Spec.Routes)
	for _, route := range toDelete {
		input := newDeleteRouteInput(*route)
		input.RouteTableId = aws.String(id)
		_, err := rm.sdkapi.DeleteRoute(ctx, input)
		rm.metrics.RecordAPICall("DELETE", "DeleteRoute", err)
		if err != nil && !isAPIError(err, "InvalidRoute.NotFound") {
			return err
		}
	}
	for _, route := range toAdd {
		input := newCreateRouteInput(*route)
		input.RouteTableId = aws.String(id)
		_, err := rm.sdkapi.CreateRoute(ctx, input)
		rm.metrics.RecordAPICall("CREATE", "CreateRoute", err)
		if err != nil {
			return err
		}
	}
	have := map[string]bool{}
	for _, g := range current.ko.Spec.PropagatingVGWs {
		have[aws.ToString(g)] = true
	}
	wanted := map[string]bool{}
	for _, g := range want.Spec.PropagatingVGWs {
		wanted[aws.ToString(g)] = true
		if have[aws.ToString(g)] {
			continue
		}
		_, err := rm.sdkapi.EnableVgwRoutePropagation(ctx, &svcsdk.EnableVgwRoutePropagationInput{GatewayId: g, RouteTableId: aws.String(id)})
		rm.metrics.RecordAPICall("UPDATE", "EnableVgwRoutePropagation", err)
		if err != nil {
			return err
		}
	}
	for g := range have {
		if wanted[g] {
			continue
		}
		_, err := rm.sdkapi.DisableVgwRoutePropagation(ctx, &svcsdk.DisableVgwRoutePropagationInput{GatewayId: aws.String(g), RouteTableId: aws.String(id)})
		rm.metrics.RecordAPICall("UPDATE", "DisableVgwRoutePropagation", err)
		if err != nil {
			return err
		}
	}
	if !tags.Equal(want.Spec.Tags, current.ko.Spec.Tags) {
		return tags.Sync(ctx, rm.sdkapi, rm.metrics, id, want.Spec.Tags, current.ko.Spec.Tags)
	}
	return nil
}

// sdkCreate takes the VPC's main route table over: finds it, then makes its
// routes, propagating gateways and tags the spec's, deleting every other
// route (but the local route) and tag.
func (rm *resourceManager) sdkCreate(ctx context.Context, desired *resource) (created *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkCreate")
	defer func() { exit(err) }()
	vpcID := aws.ToString(desired.ko.Spec.VPCID)
	if vpcID == "" {
		return nil, ackerr.NewTerminalError(fmt.Errorf("spec.vpcID or spec.vpcRef is required"))
	}
	t, err := rm.describeTable(ctx, "", vpcID)
	if err != nil {
		return nil, err
	}
	if t == nil {
		// The VPC is not there yet: retried.
		return nil, fmt.Errorf("VPC %s has no main route table", vpcID)
	}
	current := rm.observed(desired, t)
	want := desired.ko.DeepCopy()
	rm.setStatusDefaults(want)
	want.Status.ID = t.RouteTableId
	rm.setARN(want)
	if err := rm.sync(ctx, want, current); err != nil {
		return nil, err
	}
	return &resource{want}, nil
}

// sdkUpdate makes the routes, propagating gateways and tags the spec's
// again. The VPC is immutable (the CRD refuses a change).
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
		return nil, ackerr.NewTerminalError(fmt.Errorf("a DefaultRouteTable's VPC cannot change; delete it and create another"))
	}
	want := desired.ko.DeepCopy()
	rm.setStatusDefaults(want)
	want.Status.ID = latest.ko.Status.ID
	rm.setARN(want)
	if err := rm.sync(ctx, want, latest); err != nil {
		return nil, err
	}
	return &resource{want}, nil
}

// sdkDelete leaves the table, its routes and propagations as they are: EC2
// deletes a main route table only with its VPC.
func (rm *resourceManager) sdkDelete(ctx context.Context, r *resource) (deleted *resource, err error) {
	return nil, nil
}

// terminalAWSError: errors no retry fixes. A gateway or VPC not there yet
// is not terminal; a malformed route, a route that exists already with
// another target outside the spec's knowledge, or a table past its route
// limit needs a person.
func (rm *resourceManager) terminalAWSError(err error) bool {
	return isAPIError(err, "InvalidParameterValue", "InvalidParameter", "InvalidParameterCombination", "RouteLimitExceeded", "TagLimitExceeded")
}
