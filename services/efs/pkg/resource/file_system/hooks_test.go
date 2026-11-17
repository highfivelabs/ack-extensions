// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package file_system

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	ackmetrics "github.com/aws-controllers-k8s/runtime/pkg/metrics"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/efs"

	"github.com/highfivelabs/ack-extensions/pkg/fakeaws"
	svcapitypes "github.com/highfivelabs/ack-extensions/services/efs/apis/v1alpha1"
)

func TestSyncPolicyBypassLockoutSafetyCheck(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bypass *bool
		want   bool
	}{
		{name: "nil defaults to false", bypass: nil, want: false},
		{name: "explicit false", bypass: aws.Bool(false), want: false},
		{name: "explicit true", bypass: aws.Bool(true), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got struct {
				Policy                         string `json:"Policy"`
				BypassPolicyLockoutSafetyCheck bool   `json:"BypassPolicyLockoutSafetyCheck"`
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &got)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()

			cfg, err := fakeaws.Config(srv.URL, "us-east-1")
			if err != nil {
				t.Fatal(err)
			}
			client := svcsdk.NewFromConfig(cfg)
			rm := &resourceManager{
				sdkapi:  client,
				metrics: ackmetrics.NewMetrics("efs"),
			}
			err = rm.syncPolicy(context.Background(), &resource{
				ko: &svcapitypes.FileSystem{
					Spec: svcapitypes.FileSystemSpec{
						Policy:                         aws.String(`{"Version":"2012-10-17"}`),
						BypassPolicyLockoutSafetyCheck: tc.bypass,
					},
					Status: svcapitypes.FileSystemStatus{
						FileSystemID: aws.String("fs-12345678"),
					},
				},
			})
			if err != nil {
				t.Fatalf("syncPolicy: %v", err)
			}
			if got.BypassPolicyLockoutSafetyCheck != tc.want {
				t.Fatalf("BypassPolicyLockoutSafetyCheck = %v, want %v", got.BypassPolicyLockoutSafetyCheck, tc.want)
			}
		})
	}
}
