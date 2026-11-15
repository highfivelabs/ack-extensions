// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

//go:build live

package e2e

import (
	"context"
	"sort"
	"testing"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/test/live"
	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
)

// On a live run of the ACK-kind tests (ACK_TEST_BACKEND=live, built with
// -tags live by task test-live SUITE=ack; docs/live-runs.md), a forwarder
// in front of the sandbox account takes moto's place: the managers and
// the tests' SDK clients keep pkg/fakeaws's loopback endpoint and fake
// keys, and only this process holds the run's credentials.
func init() {
	harness.Live = func(t *testing.T) (string, string, []string) {
		t.Helper()
		run, err := live.Check(context.Background(), harness.RepoRoot(t))
		if err != nil {
			t.Fatal(err)
		}
		if run.Region != harness.Region {
			t.Fatalf("live: LIVE_REGION %s, but the tests are in %s", run.Region, harness.Region)
		}
		f := live.NewForwarder(run)
		url := f.Start()
		t.Cleanup(func() {
			calls := f.Calls()
			keys := make([]string, 0, len(calls))
			for k := range calls {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				t.Logf("live call %s x%d", k, calls[k])
			}
			f.Close()
		})
		t.Logf("live run %s in account %s, %s", run.ID, run.Account, run.Region)
		return url, run.Account, []string{"--resource-tags=" + live.ResourceTags(run.ID)}
	}
}
