// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// ---- AWS::ControlTower::LandingZone, a test double ----

// LandingZone is a test double for Control Tower's landing zone: moto
// 5.2.5 has no controltower and no other emulator has it, so this backend
// keeps the landing zone itself, in memory (decided 2026-11-10: a test
// double or recordings for Control Tower). What it models, from Control
// Tower's API reference, unverified on real AWS:
//
//   - one landing zone per account: a second create fails AlreadyExists;
//   - its ARN is arn:aws:controltower:<region>:<account>:landingzone/<id>,
//     and LandingZoneIdentifier is the <id>;
//   - Status ACTIVE, DriftStatus IN_SYNC and LatestAvailableVersion are
//     read-only and set here; Version, Manifest, RemediationTypes and Tags
//     are what the caller wrote;
//   - the long-running operations of the real API (an hour for a create)
//     are the fake server's polls.
//
// Delete works, so a test can tell a delete that was never asked for from
// one that was: Deletes counts them.
type LandingZone struct {
	Account, Region string

	mu      sync.Mutex
	zones   map[string]map[string]any // identifier -> model
	n       int
	deletes int
}

// NewLandingZone makes the double for an account and region.
func NewLandingZone(account, region string) *LandingZone {
	return &LandingZone{Account: account, Region: region, zones: map[string]map[string]any{}}
}

// LatestLandingZoneVersion is what the double reports as the latest
// available version.
const LatestLandingZoneVersion = "3.3"

var landingZoneWritable = []string{"Version", "Manifest", "RemediationTypes", "Tags"}

func copyModel(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func (b *LandingZone) Create(_ context.Context, m map[string]any) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.zones) > 0 {
		return "", &HandlerError{"AlreadyExists", "the account already has a landing zone"}
	}
	if str(m, "Version") == "" {
		return "", InvalidRequest("Version is required")
	}
	if _, ok := m["Manifest"].(map[string]any); !ok {
		return "", InvalidRequest("Manifest must be an object")
	}
	b.n++
	id := fmt.Sprintf("LZ%014d", b.n)
	z := map[string]any{}
	for _, k := range landingZoneWritable {
		if v, ok := m[k]; ok {
			z[k] = v
		}
	}
	b.zones[id] = copyModel(z)
	return id, nil
}

func (b *LandingZone) Read(_ context.Context, id string) (map[string]any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	z, ok := b.zones[id]
	if !ok {
		return nil, NotFound("landing zone %s", id)
	}
	out := copyModel(z)
	out["LandingZoneIdentifier"] = id
	out["Arn"] = b.arn(id)
	out["Status"] = "ACTIVE"
	out["DriftStatus"] = "IN_SYNC"
	out["LatestAvailableVersion"] = LatestLandingZoneVersion
	return out, nil
}

func (b *LandingZone) Update(_ context.Context, id string, _, next map[string]any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.zones[id]; !ok {
		return NotFound("landing zone %s", id)
	}
	z := map[string]any{}
	for _, k := range landingZoneWritable {
		if v, ok := next[k]; ok {
			z[k] = v
		}
	}
	b.zones[id] = copyModel(z)
	return nil
}

func (b *LandingZone) Delete(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deletes++
	if _, ok := b.zones[id]; !ok {
		return NotFound("landing zone %s", id)
	}
	delete(b.zones, id)
	return nil
}

func (b *LandingZone) arn(id string) string {
	return fmt.Sprintf("arn:aws:controltower:%s:%s:landingzone/%s", b.Region, b.Account, id)
}

// Zone is the landing zone's model as the double holds it (the writable
// properties), or nil.
func (b *LandingZone) Zone(id string) map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	if z, ok := b.zones[id]; ok {
		return copyModel(z)
	}
	return nil
}

// Deletes is how many deletes the double was asked for.
func (b *LandingZone) Deletes() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.deletes
}
