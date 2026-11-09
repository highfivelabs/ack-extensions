// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package db_role_association

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackmetrics "github.com/aws-controllers-k8s/runtime/pkg/metrics"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/rds"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/rds/apis/v1alpha1"
)

func TestAssociatedExactlyOne(t *testing.T) {
	for _, c := range []struct {
		clus, inst string
		wantErr    bool
	}{{"cl", "", false}, {"", "db", false}, {"cl", "db", true}, {"", "", true}} {
		ko := &svcapitypes.DBRoleAssociation{}
		if c.clus != "" {
			ko.Spec.DBClusterIdentifier = aws.String(c.clus)
		}
		if c.inst != "" {
			ko.Spec.DBInstanceIdentifier = aws.String(c.inst)
		}
		if _, _, err := associated(ko); (err != nil) != c.wantErr {
			t.Errorf("cluster %q instance %q: err %v", c.clus, c.inst, err)
		}
	}
}

// RDS identifies an association by role and feature: one role may serve
// s3Import and s3Export on one database, and Aurora MySQL associates a
// role with no feature.
func TestMatchingRoleAndFeature(t *testing.T) {
	roles := []observedRole{
		{"arn:aws:iam::123456789012:role/s3", "s3Import", "ACTIVE"},
		{"arn:aws:iam::123456789012:role/s3", "s3Export", "PENDING"},
		{"arn:aws:iam::123456789012:role/mysql", "", "ACTIVE"},
	}
	for _, c := range []struct {
		role, feature, wantStatus string
		want                      bool
	}{
		{"arn:aws:iam::123456789012:role/s3", "s3Export", "PENDING", true},
		{"arn:aws:iam::123456789012:role/s3", "s3Import", "ACTIVE", true},
		{"arn:aws:iam::123456789012:role/s3", "", "", false},
		{"arn:aws:iam::123456789012:role/mysql", "", "ACTIVE", true},
		{"arn:aws:iam::123456789012:role/mysql", "Lambda", "", false},
		{"arn:aws:iam::123456789012:role/other", "s3Import", "", false},
	} {
		got, ok := matching(roles, c.role, c.feature)
		if ok != c.want || got.status != c.wantStatus {
			t.Errorf("%s %q: got %v %q, want %v %q", c.role, c.feature, ok, got.status, c.want, c.wantStatus)
		}
	}
}

// The runtime decides ACK.ResourceSynced with IsSynced only when the
// resource manager has not set it (runtime pkg/runtime/reconciler.go,
// ensureConditions); onSuccess must leave it unset, and IsSynced must
// follow the association's status.
func TestSyncedFollowsAssociationStatus(t *testing.T) {
	rm := &resourceManager{}
	r := &resource{ko: &svcapitypes.DBRoleAssociation{}}
	res, err := rm.onSuccess(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.(*resource).ko.Status.Conditions {
		if c.Type == ackv1alpha1.ConditionTypeResourceSynced {
			t.Fatalf("onSuccess set ACK.ResourceSynced (%s); the runtime would never ask IsSynced", c.Status)
		}
	}
	for status, want := range map[string]bool{"": false, "PENDING": false, "INVALID": false, "ACTIVE": true} {
		r.ko.Status.Status = aws.String(status)
		if ok, _ := rm.IsSynced(context.Background(), r); ok != want {
			t.Errorf("IsSynced with status %q = %v, want %v", status, ok, want)
		}
	}
}

// rdsStandIn answers RemoveRoleFromDBCluster and RemoveRoleFromDBInstance,
// which moto does not implement (docs/plan.md, section 4.4), with the
// responses the RDS query API documents: an empty result, or an
// ErrorResponse with the fault's code. It records each request's form.
type rdsStandIn struct {
	mu       sync.Mutex
	requests []url.Values
	status   int
	code     string
}

func (s *rdsStandIn) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if err := req.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, req.PostForm)
	status, code := s.status, s.code
	s.mu.Unlock()
	action := req.PostForm.Get("Action")
	w.Header().Set("Content-Type", "text/xml")
	if code != "" {
		w.WriteHeader(status)
		fmt.Fprintf(w, `<ErrorResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><Error><Type>Sender</Type><Code>%s</Code><Message>stand-in fault</Message></Error><RequestId>r</RequestId></ErrorResponse>`, code)
		return
	}
	fmt.Fprintf(w, `<%[1]sResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><ResponseMetadata><RequestId>r</RequestId></ResponseMetadata></%[1]sResponse>`, action)
}

func standInManager(t *testing.T, s *rdsStandIn) *resourceManager {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	cfg := aws.Config{
		Region:       "us-west-2",
		Credentials:  credentials.NewStaticCredentialsProvider("testing", "testing", ""),
		BaseEndpoint: aws.String(srv.URL),
		// One attempt: the faults below are not retried by the SDK anyway,
		// and a 5xx would only slow the test.
		RetryMaxAttempts: 1,
	}
	return &resourceManager{
		metrics:      ackmetrics.NewMetrics("rds"),
		awsRegion:    "us-west-2",
		awsAccountID: "123456789012",
		awsPartition: "aws",
		sdkapi:       svcsdk.NewFromConfig(cfg),
	}
}

func TestDeleteRemovesTheRole(t *testing.T) {
	for _, c := range []struct {
		name                 string
		cluster, inst        string
		feature              string
		wantAction, wantID   string
		wantFeatureSent      bool
		status               int
		code                 string
		wantErr, wantRequest bool
	}{
		{name: "cluster", cluster: "app-aurora", feature: "s3Import", wantAction: "RemoveRoleFromDBCluster", wantID: "DBClusterIdentifier", wantFeatureSent: true},
		{name: "cluster without a feature (Aurora MySQL)", cluster: "app-aurora", wantAction: "RemoveRoleFromDBCluster", wantID: "DBClusterIdentifier"},
		{name: "instance", inst: "app-db", feature: "s3Export", wantAction: "RemoveRoleFromDBInstance", wantID: "DBInstanceIdentifier", wantFeatureSent: true},
		{name: "association already gone", cluster: "app-aurora", feature: "s3Import", wantAction: "RemoveRoleFromDBCluster", wantID: "DBClusterIdentifier", wantFeatureSent: true, status: 404, code: "DBClusterRoleNotFound"},
		{name: "cluster already gone", cluster: "app-aurora", feature: "s3Import", wantAction: "RemoveRoleFromDBCluster", wantID: "DBClusterIdentifier", wantFeatureSent: true, status: 404, code: "DBClusterNotFoundFault"},
		{name: "instance association already gone", inst: "app-db", feature: "s3Export", wantAction: "RemoveRoleFromDBInstance", wantID: "DBInstanceIdentifier", wantFeatureSent: true, status: 404, code: "DBInstanceRoleNotFound"},
		{name: "cluster busy", cluster: "app-aurora", feature: "s3Import", wantAction: "RemoveRoleFromDBCluster", wantID: "DBClusterIdentifier", wantFeatureSent: true, status: 400, code: "InvalidDBClusterStateFault", wantErr: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &rdsStandIn{status: c.status, code: c.code}
			rm := standInManager(t, s)
			ko := &svcapitypes.DBRoleAssociation{}
			ko.Spec.RoleARN = aws.String("arn:aws:iam::123456789012:role/s3")
			if c.cluster != "" {
				ko.Spec.DBClusterIdentifier = aws.String(c.cluster)
			}
			if c.inst != "" {
				ko.Spec.DBInstanceIdentifier = aws.String(c.inst)
			}
			if c.feature != "" {
				ko.Spec.FeatureName = aws.String(c.feature)
			}
			_, err := rm.sdkDelete(context.Background(), &resource{ko})
			if (err != nil) != c.wantErr {
				t.Fatalf("sdkDelete: err %v, want error %v", err, c.wantErr)
			}
			if len(s.requests) != 1 {
				t.Fatalf("%d requests, want 1", len(s.requests))
			}
			req := s.requests[0]
			if got := req.Get("Action"); got != c.wantAction {
				t.Errorf("Action %q, want %q", got, c.wantAction)
			}
			if got := req.Get(c.wantID); got != c.cluster+c.inst {
				t.Errorf("%s %q", c.wantID, got)
			}
			if got := req.Get("RoleArn"); got != "arn:aws:iam::123456789012:role/s3" {
				t.Errorf("RoleArn %q", got)
			}
			if _, sent := req["FeatureName"]; sent != c.wantFeatureSent || (sent && req.Get("FeatureName") != c.feature) {
				t.Errorf("FeatureName sent %v (%q), want sent %v (%q)", sent, req.Get("FeatureName"), c.wantFeatureSent, c.feature)
			}
		})
	}
}
