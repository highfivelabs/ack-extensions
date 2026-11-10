// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package network_interface_permission

// moto has none of the network interface permission API, so the generated
// calls are checked against a stand-in that answers as the EC2 API
// reference documents (docs/plan.md, the NetworkInterfacePermission
// section): what is sent, and how the answers are read.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackmetrics "github.com/aws-controllers-k8s/runtime/pkg/metrics"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/ec2"

	"github.com/highfivelabs/ack-extensions/pkg/fakeaws"
	svcapitypes "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
)

const ec2NS = "http://ec2.amazonaws.com/doc/2016-11-15/"

// ec2StandIn answers each action with a canned body, or an EC2 error with
// code; it records each request's form.
type ec2StandIn struct {
	mu       sync.Mutex
	requests []url.Values
	state    string // the permission's state in answers
	code     string // an error code to answer with
}

func (s *ec2StandIn) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if err := req.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, req.PostForm)
	state, code := s.state, s.code
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/xml")
	if code != "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `<Response><Errors><Error><Code>%s</Code><Message>stand-in</Message></Error></Errors><RequestID>r</RequestID></Response>`, code)
		return
	}
	perm := fmt.Sprintf(`<networkInterfacePermissionId>eni-perm-0123</networkInterfacePermissionId><networkInterfaceId>eni-0abc</networkInterfaceId><awsAccountId>111122223333</awsAccountId><permission>INSTANCE-ATTACH</permission><permissionState><state>%s</state></permissionState>`, state)
	switch action := req.PostForm.Get("Action"); action {
	case "CreateNetworkInterfacePermission":
		fmt.Fprintf(w, `<%[1]sResponse xmlns="%[2]s"><requestId>r</requestId><interfacePermission>%[3]s</interfacePermission></%[1]sResponse>`, action, ec2NS, perm)
	case "DescribeNetworkInterfacePermissions":
		items := ""
		if state != "" {
			items = "<item>" + perm + "</item>"
		}
		fmt.Fprintf(w, `<%[1]sResponse xmlns="%[2]s"><requestId>r</requestId><networkInterfacePermissions>%[3]s</networkInterfacePermissions></%[1]sResponse>`, action, ec2NS, items)
	case "DeleteNetworkInterfacePermission":
		fmt.Fprintf(w, `<%[1]sResponse xmlns="%[2]s"><requestId>r</requestId><return>true</return></%[1]sResponse>`, action, ec2NS)
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

func permission() *svcapitypes.NetworkInterfacePermission {
	ko := &svcapitypes.NetworkInterfacePermission{}
	ko.Spec.NetworkInterfaceID = aws.String("eni-0abc")
	ko.Spec.AWSAccountID = aws.String("111122223333")
	ko.Spec.Permission = aws.String("INSTANCE-ATTACH")
	return ko
}

func TestCreateSendsThePermissionAndReadsItsID(t *testing.T) {
	s := &ec2StandIn{state: "pending"}
	rm := standInManager(t, s)
	created, err := rm.sdkCreate(context.Background(), &resource{permission()})
	if err != nil {
		t.Fatal(err)
	}
	req := s.requests[0]
	if req.Get("Action") != "CreateNetworkInterfacePermission" || req.Get("NetworkInterfaceId") != "eni-0abc" ||
		req.Get("AwsAccountId") != "111122223333" || req.Get("Permission") != "INSTANCE-ATTACH" {
		t.Errorf("request %v", req)
	}
	if _, sent := req["AwsService"]; sent {
		t.Error("AwsService sent though the spec has none")
	}
	st := created.ko.Status
	if aws.ToString(st.NetworkInterfacePermissionID) != "eni-perm-0123" || st.PermissionState == nil || aws.ToString(st.PermissionState.State) != "pending" {
		t.Errorf("status %+v", st)
	}
}

func TestReadFollowsThePermissionsState(t *testing.T) {
	for _, c := range []struct {
		state, code string
		wantGone    bool
		wantErr     bool
	}{
		{state: "granted"},
		{state: "pending"},
		{state: "revoking", wantGone: true},
		{state: "revoked", wantGone: true},
		{state: "", wantGone: true}, // an empty list
		{code: "InvalidPermissionID.NotFound", wantGone: true},
		{code: "UnauthorizedOperation", wantErr: true},
	} {
		t.Run(c.state+c.code, func(t *testing.T) {
			s := &ec2StandIn{state: c.state, code: c.code}
			rm := standInManager(t, s)
			ko := permission()
			ko.Status.NetworkInterfacePermissionID = aws.String("eni-perm-0123")
			latest, err := rm.sdkFind(context.Background(), &resource{ko})
			if c.wantGone {
				if err != ackerr.NotFound {
					t.Fatalf("err %v, want NotFound", err)
				}
				return
			}
			if c.wantErr {
				if err == nil || err == ackerr.NotFound {
					t.Fatalf("err %v, want the API's", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := s.requests[0].Get("NetworkInterfacePermissionId.1"); got != "eni-perm-0123" {
				t.Errorf("asked for %q", got)
			}
			if aws.ToString(latest.ko.Status.PermissionState.State) != c.state {
				t.Errorf("state %q", aws.ToString(latest.ko.Status.PermissionState.State))
			}
		})
	}
}

func TestReadBeforeTheCreateFindsNothing(t *testing.T) {
	s := &ec2StandIn{state: "granted"}
	rm := standInManager(t, s)
	if _, err := rm.sdkFind(context.Background(), &resource{permission()}); err != ackerr.NotFound {
		t.Fatalf("err %v, want NotFound", err)
	}
	if len(s.requests) != 0 {
		t.Errorf("%d requests before the create", len(s.requests))
	}
}

func TestDeleteSendsTheID(t *testing.T) {
	s := &ec2StandIn{}
	rm := standInManager(t, s)
	ko := permission()
	ko.Status.NetworkInterfacePermissionID = aws.String("eni-perm-0123")
	if _, err := rm.sdkDelete(context.Background(), &resource{ko}); err != nil {
		t.Fatal(err)
	}
	req := s.requests[0]
	if req.Get("Action") != "DeleteNetworkInterfacePermission" || req.Get("NetworkInterfacePermissionId") != "eni-perm-0123" {
		t.Errorf("request %v", req)
	}
}
