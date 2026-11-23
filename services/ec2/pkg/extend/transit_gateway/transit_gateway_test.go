// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package transitgateway

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// fakeEC2 is one transit gateway; it records the ModifyTransitGateway
// calls and applies them as AWS does (encryption by way of "enabling").
type fakeEC2 struct {
	state svcsdktypes.TransitGatewayState
	opts  svcsdktypes.TransitGatewayOptions
	calls []string
}

func (f *fakeEC2) DescribeTransitGateways(_ context.Context, in *svcsdk.DescribeTransitGatewaysInput, _ ...func(*svcsdk.Options)) (*svcsdk.DescribeTransitGatewaysOutput, error) {
	o := f.opts
	return &svcsdk.DescribeTransitGatewaysOutput{TransitGateways: []svcsdktypes.TransitGateway{{
		TransitGatewayId: aws.String(in.TransitGatewayIds[0]), State: f.state, Options: &o,
	}}}, nil
}

func (f *fakeEC2) ModifyTransitGateway(_ context.Context, in *svcsdk.ModifyTransitGatewayInput, _ ...func(*svcsdk.Options)) (*svcsdk.ModifyTransitGatewayOutput, error) {
	o := in.Options
	var parts []string
	if o.SecurityGroupReferencingSupport != "" {
		parts = append(parts, "sgref="+string(o.SecurityGroupReferencingSupport))
		f.opts.SecurityGroupReferencingSupport = o.SecurityGroupReferencingSupport
	}
	if o.EncryptionSupport != "" {
		parts = append(parts, "encryption="+string(o.EncryptionSupport))
		st := svcsdktypes.EncryptionStateValueEnabling
		if o.EncryptionSupport == disable {
			st = svcsdktypes.EncryptionStateValueDisabling
		}
		f.opts.EncryptionSupport = &svcsdktypes.EncryptionSupport{EncryptionState: st}
	}
	if o.AssociationDefaultRouteTableId != nil {
		parts = append(parts, "assoc="+*o.AssociationDefaultRouteTableId)
		f.opts.AssociationDefaultRouteTableId = o.AssociationDefaultRouteTableId
	}
	if o.PropagationDefaultRouteTableId != nil {
		parts = append(parts, "prop="+*o.PropagationDefaultRouteTableId)
		f.opts.PropagationDefaultRouteTableId = o.PropagationDefaultRouteTableId
	}
	// Only the asked options are sent: nothing else of ACK's.
	if o.DnsSupport != "" || o.VpnEcmpSupport != "" || o.AutoAcceptSharedAttachments != "" || o.AmazonSideAsn != nil ||
		o.DefaultRouteTableAssociation != "" || o.DefaultRouteTablePropagation != "" || len(o.AddTransitGatewayCidrBlocks) != 0 {
		parts = append(parts, "OTHER")
	}
	f.calls = append(f.calls, aws.ToString(in.TransitGatewayId)+" "+strings.Join(parts, " "))
	return &svcsdk.ModifyTransitGatewayOutput{}, nil
}

func TestParse(t *testing.T) {
	cases := []struct {
		ann     map[string]string
		want    Wanted
		wantErr string
	}{
		{nil, Wanted{}, ""},
		{map[string]string{AnnotationSecurityGroupReferencing: "enable"}, Wanted{SecurityGroupReferencing: "enable"}, ""},
		{map[string]string{AnnotationSecurityGroupReferencing: " Disable "}, Wanted{SecurityGroupReferencing: "disable"}, ""},
		{map[string]string{AnnotationSecurityGroupReferencing: "true"}, Wanted{}, "neither enable nor disable"},
		{map[string]string{AnnotationEncryption: "enable"}, Wanted{Encryption: "enable"}, ""},
		{map[string]string{AnnotationAssociationDefaultRouteTable: "tgw-rtb-0123"}, Wanted{AssociationDefaultRouteTable: "tgw-rtb-0123"}, ""},
		{map[string]string{AnnotationPropagationDefaultRouteTable: "rtb-0123"}, Wanted{}, "not a transit gateway route table ID"},
		{map[string]string{AnnotationPropagationDefaultRouteTable: "tgw-rtb-"}, Wanted{}, "not a transit gateway route table ID"},
		// One bad annotation does not drop the others.
		{map[string]string{AnnotationEncryption: "on", AnnotationPropagationDefaultRouteTable: "tgw-rtb-9"}, Wanted{PropagationDefaultRouteTable: "tgw-rtb-9"}, "neither enable nor disable"},
	}
	for _, c := range cases {
		got, err := Parse(c.ann)
		if c.wantErr == "" && err != nil || c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("Parse(%v) error %v, want %q", c.ann, err, c.wantErr)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Parse(%v) = %+v, want %+v", c.ann, got, c.want)
		}
	}
}

func defaults() svcsdktypes.TransitGatewayOptions {
	return svcsdktypes.TransitGatewayOptions{
		DefaultRouteTableAssociation:   svcsdktypes.DefaultRouteTableAssociationValueEnable,
		DefaultRouteTablePropagation:   svcsdktypes.DefaultRouteTablePropagationValueEnable,
		AssociationDefaultRouteTableId: aws.String("tgw-rtb-default"),
		PropagationDefaultRouteTableId: aws.String("tgw-rtb-default"),
	}
}

func TestApply(t *testing.T) {
	ctx := context.Background()
	t.Run("the differing options go in one call, then nothing", func(t *testing.T) {
		f := &fakeEC2{state: svcsdktypes.TransitGatewayStateAvailable, opts: defaults()}
		w := Wanted{SecurityGroupReferencing: "enable", AssociationDefaultRouteTable: "tgw-rtb-a", PropagationDefaultRouteTable: "tgw-rtb-default"}
		done, retry, err := Apply(ctx, f, "tgw-1", w)
		if err != nil || retry {
			t.Fatal(err, retry)
		}
		if want := []string{"tgw-1 sgref=enable assoc=tgw-rtb-a"}; !reflect.DeepEqual(f.calls, want) {
			t.Fatalf("calls %v, want %v", f.calls, want)
		}
		if len(done) != 1 || done[0].Reason != ReasonOptionsSet || done[0].Warning {
			t.Fatalf("done %+v", done)
		}
		f.calls = nil
		if done, _, err := Apply(ctx, f, "tgw-1", w); err != nil || len(f.calls) != 0 || len(done) != 0 {
			t.Fatalf("second pass: calls %v, done %+v (%v)", f.calls, done, err)
		}
	})
	t.Run("a gateway that reports no referencing support has it disabled", func(t *testing.T) {
		f := &fakeEC2{state: svcsdktypes.TransitGatewayStateAvailable, opts: defaults()}
		if _, _, err := Apply(ctx, f, "tgw-1", Wanted{SecurityGroupReferencing: "disable"}); err != nil || len(f.calls) != 0 {
			t.Fatalf("calls %v (%v)", f.calls, err)
		}
	})
	t.Run("encryption is asked once and waited out", func(t *testing.T) {
		f := &fakeEC2{state: svcsdktypes.TransitGatewayStateAvailable, opts: defaults()}
		_, retry, err := Apply(ctx, f, "tgw-1", Wanted{Encryption: "enable"})
		if err != nil || !retry {
			t.Fatal(err, retry)
		}
		// enabling: no second call, still waiting.
		if _, retry, err := Apply(ctx, f, "tgw-1", Wanted{Encryption: "enable"}); err != nil || !retry || len(f.calls) != 1 {
			t.Fatalf("calls %v retry %v (%v)", f.calls, retry, err)
		}
		// disable asked while enabling: waits for enabled first.
		if _, retry, err := Apply(ctx, f, "tgw-1", Wanted{Encryption: "disable"}); err != nil || !retry || len(f.calls) != 1 {
			t.Fatalf("calls %v retry %v (%v)", f.calls, retry, err)
		}
		f.opts.EncryptionSupport.EncryptionState = svcsdktypes.EncryptionStateValueEnabled
		if _, retry, err := Apply(ctx, f, "tgw-1", Wanted{Encryption: "enable"}); err != nil || retry || len(f.calls) != 1 {
			t.Fatalf("calls %v retry %v (%v)", f.calls, retry, err)
		}
		if _, _, err := Apply(ctx, f, "tgw-1", Wanted{Encryption: "disable"}); err != nil {
			t.Fatal(err)
		}
		if want := []string{"tgw-1 encryption=enable", "tgw-1 encryption=disable"}; !reflect.DeepEqual(f.calls, want) {
			t.Fatalf("calls %v, want %v", f.calls, want)
		}
	})
	t.Run("a default route table is not set while the default is off", func(t *testing.T) {
		o := defaults()
		o.DefaultRouteTablePropagation = svcsdktypes.DefaultRouteTablePropagationValueDisable
		f := &fakeEC2{state: svcsdktypes.TransitGatewayStateAvailable, opts: o}
		done, _, err := Apply(ctx, f, "tgw-1", Wanted{AssociationDefaultRouteTable: "tgw-rtb-a", PropagationDefaultRouteTable: "tgw-rtb-a"})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"tgw-1 assoc=tgw-rtb-a"}; !reflect.DeepEqual(f.calls, want) {
			t.Fatalf("calls %v, want %v", f.calls, want)
		}
		if len(done) != 2 || done[0].Reason != ReasonOptionIgnored || !done[0].Warning || !strings.Contains(done[0].Message, AnnotationPropagationDefaultRouteTable) {
			t.Fatalf("done %+v", done)
		}
	})
	t.Run("a gateway being made is retried; one being deleted is left alone", func(t *testing.T) {
		for st, wantRetry := range map[svcsdktypes.TransitGatewayState]bool{
			svcsdktypes.TransitGatewayStatePending:   true,
			svcsdktypes.TransitGatewayStateModifying: true,
			svcsdktypes.TransitGatewayStateDeleting:  false,
			svcsdktypes.TransitGatewayStateDeleted:   false,
		} {
			f := &fakeEC2{state: st, opts: defaults()}
			if _, retry, err := Apply(ctx, f, "tgw-1", Wanted{SecurityGroupReferencing: "enable"}); err != nil || retry != wantRetry || len(f.calls) != 0 {
				t.Fatalf("%s: calls %v retry %v (%v)", st, f.calls, retry, err)
			}
		}
	})
}
