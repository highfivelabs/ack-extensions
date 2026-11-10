// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

//go:build live

package e2e

import (
	"context"
	"testing"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/test/live"
)

// On a live run (CLOUDCONTROL_E2E_TARGET=real, built with -tags live by
// task test-live), the recorder in front of real Cloud Control takes the
// fake's place, and the tests check the services in the real account.
func init() {
	startReal = func(t *testing.T) liveRun {
		t.Helper()
		run, err := live.Check(context.Background(), repoRoot(t))
		if err != nil {
			t.Fatal(err)
		}
		if run.Region != region {
			t.Fatalf("live: LIVE_REGION %s, but the tests' contexts are in %s", run.Region, region)
		}
		rec := live.NewRecorder(run)
		url := rec.Start()
		t.Cleanup(rec.Close)
		t.Logf("live run %s in account %s, %s", run.ID, run.Account, run.Region)
		return liveRun{cc: rec, url: url, cfg: run.Config, account: run.Account,
			extraArgs: []string{"--resource-tags=" + live.ResourceTags(run.ID)}}
	}
}
