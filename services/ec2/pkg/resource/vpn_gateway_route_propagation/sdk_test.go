// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package vpn_gateway_route_propagation

// moto has neither EnableVgwRoutePropagation nor
// DisableVgwRoutePropagation, so the generated calls are checked against
// a stand-in that answers as the EC2 API reference documents
// (docs/plan.md, the VPNGatewayRoutePropagation section).

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackmetrics "github.com/aws-controllers-k8s/runtime/pkg/metrics"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/ec2"

	"github.com/highfivelabs/ack-extensions/pkg/fakeaws"
	svcapitypes "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
)

// ec2StandIn answers DescribeRouteTables with one table propagating the
// gateways listed, Enable/Disable with success, or any action with an EC2
// error code; it records each request's form.
type ec2StandIn struct {
	mu          sync.Mutex
	requests    []url.Values
	propagating []string
	code        string
}

func (s *ec2StandIn) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if err := req.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, req.PostForm)
	gws, code := s.propagating, s.code
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/xml")
	if code != "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `<Response><Errors><Error><Code>%s</Code><Message>stand-in</Message></Error></Errors><RequestID>r</RequestID></Response>`, code)
		return
	}
	switch action := req.PostForm.Get("Action"); action {
	case "DescribeRouteTables":
		var b strings.Builder
		for _, g := range gws {
			fmt.Fprintf(&b, "<item><gatewayId>%s</gatewayId></item>", g)
		}
		fmt.Fprintf(w, `<%[1]sResponse><requestId>r</requestId><routeTableSet><item><routeTableId>rtb-0abc</routeTableId><vpcId>vpc-1</vpcId><propagatingVgwSet>%[2]s</propagatingVgwSet></item></routeTableSet></%[1]sResponse>`, action, b.String())
	case "EnableVgwRoutePropagation", "DisableVgwRoutePropagation":
		fmt.Fprintf(w, `<%[1]sResponse><requestId>r</requestId><return>true</return></%[1]sResponse>`, action)
	default:
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `<Response><Errors><Error><Code>InvalidAction</Code><Message>%s</Message></Error></Errors><RequestID>r</RequestID></Response>`, action)
	}
}

func standInManager(t *testing.T, s *ec2StandIn) *resourceManager {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	// The stand-in on loopback, through the guarded client (pkg/fakeaws).
	cfg, err := fakeaws.Config(srv.URL, "us-west-2")
	if err != nil {
		t.Fatal(err)
	}
	cfg.RetryMaxAttempts = 1
	return &resourceManager{
		metrics:      ackmetrics.NewMetrics("ec2"),
		awsRegion:    "us-west-2",
		awsAccountID: "123456789012",
		awsPartition: "aws",
		sdkapi:       svcsdk.NewFromConfig(cfg),
	}
}

func propagation() *svcapitypes.VPNGatewayRoutePropagation {
	ko := &svcapitypes.VPNGatewayRoutePropagation{}
	ko.Spec.GatewayID = aws.String("vgw-0abc")
	ko.Spec.RouteTableID = aws.String("rtb-0abc")
	return ko
}

func TestReadFindsThePropagationOnlyWhenTheTableHasTheGateway(t *testing.T) {
	for _, c := range []struct {
		name        string
		propagating []string
		code        string
		wantGone    bool
		wantErr     bool
	}{
		{name: "propagating", propagating: []string{"vgw-other", "vgw-0abc"}},
		{name: "another gateway only", propagating: []string{"vgw-other"}, wantGone: true},
		{name: "none", wantGone: true},
		{name: "table gone", code: "InvalidRouteTableID.NotFound", wantGone: true},
		{name: "refused", code: "UnauthorizedOperation", wantErr: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &ec2StandIn{propagating: c.propagating, code: c.code}
			rm := standInManager(t, s)
			latest, err := rm.sdkFind(context.Background(), &resource{propagation()})
			switch {
			case c.wantGone:
				if err != ackerr.NotFound {
					t.Fatalf("err %v, want NotFound", err)
				}
			case c.wantErr:
				if err == nil || err == ackerr.NotFound {
					t.Fatalf("err %v, want the API's", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				if aws.ToString(latest.ko.Spec.RouteTableID) != "rtb-0abc" || aws.ToString(latest.ko.Spec.GatewayID) != "vgw-0abc" {
					t.Errorf("spec %+v", latest.ko.Spec)
				}
			}
			req := s.requests[0]
			if req.Get("Action") != "DescribeRouteTables" || req.Get("RouteTableId.1") != "rtb-0abc" {
				t.Errorf("request %v", req)
			}
		})
	}
}

func TestCreateAndDeleteSendTheGatewayAndTable(t *testing.T) {
	s := &ec2StandIn{}
	rm := standInManager(t, s)
	if _, err := rm.sdkCreate(context.Background(), &resource{propagation()}); err != nil {
		t.Fatal(err)
	}
	if _, err := rm.sdkDelete(context.Background(), &resource{propagation()}); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"EnableVgwRoutePropagation", "DisableVgwRoutePropagation"} {
		req := s.requests[i]
		if req.Get("Action") != want || req.Get("GatewayId") != "vgw-0abc" || req.Get("RouteTableId") != "rtb-0abc" {
			t.Errorf("request %d: %v", i, req)
		}
	}
}

func TestPropagating(t *testing.T) {
	if propagating(nil, nil) != nil {
		t.Error("tables for no gateway")
	}
}
