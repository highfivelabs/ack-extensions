// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package db_proxy_target

import (
	"context"
	"testing"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/rds/apis/v1alpha1"
)

func target(state string) *svcapitypes.DBProxyTargetObservation {
	return &svcapitypes.DBProxyTargetObservation{TargetHealth: &svcapitypes.DBProxyTargetHealth{State: aws.String(state)}}
}

func TestAllTargetsAvailable(t *testing.T) {
	cases := []struct {
		name    string
		targets []*svcapitypes.DBProxyTargetObservation
		want    bool
	}{
		{"none reported", nil, false},
		{"registering", []*svcapitypes.DBProxyTargetObservation{target("REGISTERING")}, false},
		{"available", []*svcapitypes.DBProxyTargetObservation{target("AVAILABLE")}, true},
		{"a cluster with one member still registering", []*svcapitypes.DBProxyTargetObservation{target("AVAILABLE"), target("REGISTERING")}, false},
		{"unavailable", []*svcapitypes.DBProxyTargetObservation{target("UNAVAILABLE")}, false},
		{"no health", []*svcapitypes.DBProxyTargetObservation{{}}, false},
	}
	for _, c := range cases {
		if got := allTargetsAvailable(c.targets); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRegisteredExactlyOne(t *testing.T) {
	for _, c := range []struct {
		inst, clus string
		wantErr    bool
	}{{"db", "", false}, {"", "cl", false}, {"db", "cl", true}, {"", "", true}} {
		ko := &svcapitypes.DBProxyTarget{}
		if c.inst != "" {
			ko.Spec.DBInstanceIdentifier = aws.String(c.inst)
		}
		if c.clus != "" {
			ko.Spec.DBClusterIdentifier = aws.String(c.clus)
		}
		if _, _, err := registered(ko); (err != nil) != c.wantErr {
			t.Errorf("instance %q cluster %q: err %v", c.inst, c.clus, err)
		}
	}
}

// The runtime decides ACK.ResourceSynced with IsSynced only when the
// resource manager has not set it (runtime pkg/runtime/reconciler.go,
// ensureConditions); onSuccess must leave it unset, and IsSynced must
// follow the targets' health.
func TestSyncedFollowsTargetHealth(t *testing.T) {
	rm := &resourceManager{}
	r := &resource{ko: &svcapitypes.DBProxyTarget{}}
	r.ko.Status.Targets = []*svcapitypes.DBProxyTargetObservation{target("REGISTERING")}
	res, err := rm.onSuccess(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.(*resource).ko.Status.Conditions {
		if c.Type == ackv1alpha1.ConditionTypeResourceSynced {
			t.Fatalf("onSuccess set ACK.ResourceSynced (%s); the runtime would never ask IsSynced", c.Status)
		}
	}
	if ok, _ := rm.IsSynced(context.Background(), r); ok {
		t.Fatal("IsSynced with a REGISTERING target")
	}
	r.ko.Status.Targets = []*svcapitypes.DBProxyTargetObservation{target("AVAILABLE")}
	if ok, _ := rm.IsSynced(context.Background(), r); !ok {
		t.Fatal("not IsSynced with every target AVAILABLE")
	}
}
