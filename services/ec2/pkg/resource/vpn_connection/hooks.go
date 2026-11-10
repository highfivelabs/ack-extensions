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
// Modified by High Five Labs for ack-extensions: customUpdateVpnConnection
// and updateTagSpecificationsInCreateRequest copied from
// aws-controllers-k8s/ec2-controller v1.23.1 (9303654c),
// pkg/resource/internet_gateway/hooks.go, through this repository's
// vpn_gateway/hooks.go. Changes: the resource is a VPN connection; the
// update also syncs static routes (CreateVpnConnectionRoute,
// DeleteVpnConnectionRoute), the connection's CIDRs
// (ModifyVpnConnectionOptions) and each tunnel's options
// (ModifyVpnTunnelOptions); everything else in the options is refused.
// The options' read (readOptions) and the static routes are written here.

package vpn_connection

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
	"github.com/highfivelabs/ack-extensions/services/ec2/pkg/tags"
)

func (rm *resourceManager) customUpdateVpnConnection(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta *ackcompare.Delta,
) (updated *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.customUpdateVpnConnection")
	defer func(err error) {
		exit(err)
	}(err)

	// EC2's modify calls return no resource: when they succeed, desired's
	// spec is the connection's.
	updated = rm.concreteResource(desired.DeepCopy())
	id := latest.ko.Status.VPNConnectionID

	if delta.DifferentAt("Spec.Options") {
		if err = rm.updateOptions(ctx, *id, desired.ko.Spec.Options, latest.ko.Spec.Options); err != nil {
			return nil, err
		}
	}
	if delta.DifferentAt("Spec.StaticRoutes") {
		if err = rm.syncStaticRoutes(ctx, latest.ko, desired.ko.Spec.StaticRoutes, latest.ko.Spec.StaticRoutes); err != nil {
			return nil, err
		}
	}
	if delta.DifferentAt("Spec.Tags") {
		if err = tags.Sync(
			ctx, rm.sdkapi, rm.metrics, *id,
			desired.ko.Spec.Tags, latest.ko.Spec.Tags,
		); err != nil {
			return nil, err
		}
	}
	return updated, nil
}

// updateTagSpecificationsInCreateRequest adds the spec's tags to
// CreateVpnConnectionInput.TagSpecifications, of resource type
// vpn-connection.
func updateTagSpecificationsInCreateRequest(r *resource,
	input *svcsdk.CreateVpnConnectionInput) {
	input.TagSpecifications = nil
	if r.ko.Spec.Tags != nil {
		requestedTags := []svcsdktypes.Tag{}
		for _, desiredTag := range r.ko.Spec.Tags {
			tag := svcsdktypes.Tag{}
			if desiredTag.Key != nil && desiredTag.Value != nil {
				tag.Key = desiredTag.Key
				tag.Value = desiredTag.Value
			}
			requestedTags = append(requestedTags, tag)
		}
		input.TagSpecifications = []svcsdktypes.TagSpecification{{
			ResourceType: "vpn-connection",
			Tags:         requestedTags,
		}}
	}
}

// awsOptionsOf is the connection's options as EC2 returns them, with the
// tunnels' pre-shared keys cleared: they are never kept (the spec names
// their Secrets). nil when EC2 returns none (moto).
func awsOptionsOf(resp *svcsdk.DescribeVpnConnectionsOutput, id *string) *svcsdktypes.VpnConnectionOptions {
	if resp == nil || id == nil {
		return nil
	}
	for i := range resp.VpnConnections {
		c := &resp.VpnConnections[i]
		c.CustomerGatewayConfiguration = nil // it holds the keys too
		for j := range optionsTunnels(c.Options) {
			c.Options.TunnelOptions[j].PreSharedKey = nil
		}
		if c.VpnConnectionId != nil && *c.VpnConnectionId == *id {
			return c.Options
		}
	}
	return nil
}

func optionsTunnels(o *svcsdktypes.VpnConnectionOptions) []svcsdktypes.TunnelOption {
	if o == nil {
		return nil
	}
	return o.TunnelOptions
}

// readOptions is the options as EC2 has them, in the spec's shape, for
// the fields the spec sets and no others: EC2 fills a default for every
// tunnel setting left out, which is no drift. Tunnels are matched by
// position. The pre-shared keys stay the spec's references (EC2's keys are
// never read back, so a changed Secret is pushed only when its reference
// changes). With nothing from EC2 (moto returns no options), the spec's.
func readOptions(spec *svcapitypes.VPNConnectionOptionsSpecification, got *svcsdktypes.VpnConnectionOptions) (*svcapitypes.VPNConnectionOptionsSpecification, error) {
	if spec == nil || got == nil {
		return spec, nil
	}
	var specMap, gotMap map[string]any
	if err := roundTrip(spec, &specMap); err != nil {
		return nil, err
	}
	if err := roundTrip(got, &gotMap); err != nil {
		return nil, err
	}
	masked, _ := mask(specMap, lowerKeys(gotMap)).(map[string]any)
	out := &svcapitypes.VPNConnectionOptionsSpecification{}
	if err := roundTrip(masked, out); err != nil {
		return nil, err
	}
	for i, t := range spec.TunnelOptions {
		if t == nil || i >= len(out.TunnelOptions) {
			continue
		}
		if out.TunnelOptions[i] == nil {
			out.TunnelOptions[i] = &svcapitypes.VPNTunnelOptionsSpecification{}
		}
		out.TunnelOptions[i].PreSharedKey = t.PreSharedKey
	}
	return out, nil
}

// roundTrip converts between the SDK's shapes and ours through JSON, whose
// keys match without regard to case (IkeVersions and iKEVersions).
func roundTrip(in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// lowerKeys lower-cases every map key, so EC2's names (LocalIpv4NetworkCidr)
// meet ours (localIPv4NetworkCIDR).
func lowerKeys(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[lower(k)] = lowerKeys(x)
		}
		return m
	case []any:
		l := make([]any, len(t))
		for i, x := range t {
			l[i] = lowerKeys(x)
		}
		return l
	}
	return v
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// mask keeps from got (lower-cased keys) the fields spec has, by spec's
// keys; a list of objects is matched by position, and a list of values
// (IKE versions, algorithms, DH groups) is taken whole.
func mask(spec, got any) any {
	switch s := spec.(type) {
	case map[string]any:
		g, _ := got.(map[string]any)
		out := map[string]any{}
		for k, v := range s {
			gv, ok := g[lower(k)]
			if !ok {
				continue
			}
			out[k] = mask(v, gv)
		}
		return out
	case []any:
		g, _ := got.([]any)
		if len(s) > 0 {
			if m, isMap := s[0].(map[string]any); isMap && !valueWrapper(m) {
				out := make([]any, 0, len(s))
				for i, v := range s {
					var gv any
					if i < len(g) {
						gv = g[i]
					}
					out = append(out, mask(v, gv))
				}
				return out
			}
		}
		return g
	}
	return got
}

// valueWrapper says whether a list element is one of EC2's value objects
// ({"value": "ikev2"}), whose lists are taken whole.
func valueWrapper(m map[string]any) bool {
	_, ok := m["value"]
	return ok && len(m) == 1
}

// updateOptions applies a change to the options: the connection's CIDRs
// with ModifyVpnConnectionOptions, each changed tunnel with
// ModifyVpnTunnelOptions (by the tunnel's outside IP address, read from
// EC2), and refuses a change to anything else.
func (rm *resourceManager) updateOptions(
	ctx context.Context,
	id string,
	desired, latest *svcapitypes.VPNConnectionOptionsSpecification,
) error {
	d, l := orEmpty(desired), orEmpty(latest)
	if !reflect.DeepEqual(d.EnableAcceleration, l.EnableAcceleration) ||
		!reflect.DeepEqual(d.StaticRoutesOnly, l.StaticRoutesOnly) ||
		!reflect.DeepEqual(d.TunnelInsideIPVersion, l.TunnelInsideIPVersion) ||
		!reflect.DeepEqual(d.OutsideIPAddressType, l.OutsideIPAddressType) ||
		!reflect.DeepEqual(d.TransportTransitGatewayAttachmentID, l.TransportTransitGatewayAttachmentID) ||
		len(d.TunnelOptions) != len(l.TunnelOptions) {
		return ackerr.NewTerminalError(fmt.Errorf(
			"a VPN connection's acceleration, static-routes-only, inside IP version, outside IP address type, transport attachment and number of tunnels cannot change: make a new VPNConnection"))
	}
	if !reflect.DeepEqual(d.LocalIPv4NetworkCIDR, l.LocalIPv4NetworkCIDR) ||
		!reflect.DeepEqual(d.LocalIPv6NetworkCIDR, l.LocalIPv6NetworkCIDR) ||
		!reflect.DeepEqual(d.RemoteIPv4NetworkCIDR, l.RemoteIPv4NetworkCIDR) ||
		!reflect.DeepEqual(d.RemoteIPv6NetworkCIDR, l.RemoteIPv6NetworkCIDR) {
		_, err := rm.sdkapi.ModifyVpnConnectionOptions(ctx, &svcsdk.ModifyVpnConnectionOptionsInput{
			VpnConnectionId:       &id,
			LocalIpv4NetworkCidr:  d.LocalIPv4NetworkCIDR,
			LocalIpv6NetworkCidr:  d.LocalIPv6NetworkCIDR,
			RemoteIpv4NetworkCidr: d.RemoteIPv4NetworkCIDR,
			RemoteIpv6NetworkCidr: d.RemoteIPv6NetworkCIDR,
		})
		rm.metrics.RecordAPICall("UPDATE", "ModifyVpnConnectionOptions", err)
		if err != nil {
			return err
		}
	}
	var outside []string
	for i := range d.TunnelOptions {
		if reflect.DeepEqual(d.TunnelOptions[i], l.TunnelOptions[i]) {
			continue
		}
		if outside == nil {
			var err error
			if outside, err = rm.tunnelOutsideIPs(ctx, id); err != nil {
				return err
			}
		}
		if i >= len(outside) || outside[i] == "" {
			return fmt.Errorf("tunnel %d of VPN connection %s has no outside IP address yet", i, id)
		}
		spec, err := rm.tunnelModification(ctx, d.TunnelOptions[i])
		if err != nil {
			return err
		}
		_, err = rm.sdkapi.ModifyVpnTunnelOptions(ctx, &svcsdk.ModifyVpnTunnelOptionsInput{
			VpnConnectionId:           &id,
			VpnTunnelOutsideIpAddress: &outside[i],
			TunnelOptions:             spec,
		})
		rm.metrics.RecordAPICall("UPDATE", "ModifyVpnTunnelOptions", err)
		if err != nil {
			return err
		}
	}
	return nil
}

func orEmpty(o *svcapitypes.VPNConnectionOptionsSpecification) *svcapitypes.VPNConnectionOptionsSpecification {
	if o == nil {
		return &svcapitypes.VPNConnectionOptionsSpecification{}
	}
	return o
}

// tunnelOutsideIPs is each tunnel's outside IP address, in tunnel order.
func (rm *resourceManager) tunnelOutsideIPs(ctx context.Context, id string) ([]string, error) {
	resp, err := rm.sdkapi.DescribeVpnConnections(ctx, &svcsdk.DescribeVpnConnectionsInput{VpnConnectionIds: []string{id}})
	rm.metrics.RecordAPICall("READ_MANY", "DescribeVpnConnections", err)
	if err != nil {
		return nil, err
	}
	o := awsOptionsOf(resp, &id)
	var ips []string
	for _, t := range optionsTunnels(o) {
		if t.OutsideIpAddress == nil {
			ips = append(ips, "")
			continue
		}
		ips = append(ips, *t.OutsideIpAddress)
	}
	return ips, nil
}

// tunnelModification is a tunnel's spec as ModifyVpnTunnelOptions takes
// it, its pre-shared key read from the Secret. The key is never logged
// and never in an error.
func (rm *resourceManager) tunnelModification(ctx context.Context, t *svcapitypes.VPNTunnelOptionsSpecification) (*svcsdktypes.ModifyVpnTunnelOptionsSpecification, error) {
	out := &svcsdktypes.ModifyVpnTunnelOptionsSpecification{}
	if t == nil {
		return out, nil
	}
	plain := *t
	plain.PreSharedKey = nil
	if err := roundTrip(&plain, out); err != nil {
		return nil, err
	}
	if t.PreSharedKey != nil {
		key, err := rm.rr.SecretValueFromReference(ctx, t.PreSharedKey)
		if err != nil {
			return nil, ackrequeueOrTerminal(err)
		}
		if key != "" {
			out.PreSharedKey = &key
		}
	}
	return out, nil
}

// ackrequeueOrTerminal makes a missing Secret terminal, as the generated
// create does.
func ackrequeueOrTerminal(err error) error {
	if err == ackerr.SecretNotFound || err == ackerr.SecretTypeNotSupported {
		return ackerr.NewTerminalError(err)
	}
	return err
}

// staticRoutesOf is the connection's static routes, from its status
// (EC2's routes of source Static that are not being deleted).
func staticRoutesOf(ko *svcapitypes.VPNConnection) []*string {
	have := map[string]bool{}
	for _, r := range ko.Status.Routes {
		if r == nil || r.DestinationCIDRBlock == nil {
			continue
		}
		if r.Source != nil && *r.Source != string(svcsdktypes.VpnStaticRouteSourceStatic) {
			continue
		}
		if r.State != nil && (*r.State == string(svcsdktypes.VpnStateDeleting) || *r.State == string(svcsdktypes.VpnStateDeleted)) {
			continue
		}
		have[*r.DestinationCIDRBlock] = true
	}
	return ordered(ko.Spec.StaticRoutes, have)
}

// ordered lists have's CIDRs in the spec's order, then the rest sorted,
// so an order EC2 chooses is no drift.
func ordered(spec []*string, have map[string]bool) []*string {
	if len(have) == 0 {
		return nil
	}
	var out []*string
	seen := map[string]bool{}
	for _, s := range spec {
		if s != nil && have[*s] && !seen[*s] {
			v := *s
			out = append(out, &v)
			seen[v] = true
		}
	}
	var rest []string
	for c := range have {
		if !seen[c] {
			rest = append(rest, c)
		}
	}
	sort.Strings(rest)
	for _, c := range rest {
		v := c
		out = append(out, &v)
	}
	return out
}

// syncStaticRoutes adds the routes desired has and current lacks, and
// deletes the ones current has and desired lacks.
func (rm *resourceManager) syncStaticRoutes(ctx context.Context, ko *svcapitypes.VPNConnection, desired, current []*string) error {
	add, remove := routeChanges(desired, current)
	for _, cidr := range add {
		_, err := rm.sdkapi.CreateVpnConnectionRoute(ctx, &svcsdk.CreateVpnConnectionRouteInput{
			VpnConnectionId: ko.Status.VPNConnectionID, DestinationCidrBlock: &cidr,
		})
		rm.metrics.RecordAPICall("UPDATE", "CreateVpnConnectionRoute", err)
		if err != nil {
			return err
		}
	}
	for _, cidr := range remove {
		_, err := rm.sdkapi.DeleteVpnConnectionRoute(ctx, &svcsdk.DeleteVpnConnectionRouteInput{
			VpnConnectionId: ko.Status.VPNConnectionID, DestinationCidrBlock: &cidr,
		})
		rm.metrics.RecordAPICall("UPDATE", "DeleteVpnConnectionRoute", err)
		if err != nil {
			return err
		}
	}
	return nil
}

// routeChanges is what to add and what to delete, each once.
func routeChanges(desired, current []*string) (add, remove []string) {
	want, have := set(desired), set(current)
	for _, c := range desired {
		if c != nil && !have[*c] {
			add = append(add, *c)
			have[*c] = true
		}
	}
	for _, c := range current {
		if c != nil && !want[*c] {
			remove = append(remove, *c)
			want[*c] = true
		}
	}
	return add, remove
}

func set(l []*string) map[string]bool {
	m := map[string]bool{}
	for _, s := range l {
		if s != nil {
			m[*s] = true
		}
	}
	return m
}
