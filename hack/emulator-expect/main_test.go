// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

const expectFixture = `release: 1.0.338
expected:
  - test: TestB
    missing: Demo DoThing (501)
    doc: docs/aws-emulator.md#the-14-failures-candidate-upstream-contributions
`

const listFixture = `TestA
TestB
TestC
ok  	example.com/tests/e2e	0.012s
`

// events is go test -json output: one line per event.
func events(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

const (
	runA  = `{"Action":"run","Package":"p","Test":"TestA"}`
	passA = `{"Action":"pass","Package":"p","Test":"TestA","Elapsed":1}`
	failA = `{"Action":"fail","Package":"p","Test":"TestA","Elapsed":1}`
	skipA = `{"Action":"skip","Package":"p","Test":"TestA","Elapsed":0}`
	runB  = `{"Action":"run","Package":"p","Test":"TestB"}`
	failB = `{"Action":"fail","Package":"p","Test":"TestB","Elapsed":1}`
	passB = `{"Action":"pass","Package":"p","Test":"TestB","Elapsed":1}`
	// A subtest's failure is its parent's, not a test of its own.
	failBsub = `{"Action":"fail","Package":"p","Test":"TestB/step","Elapsed":1}`
	runC     = `{"Action":"run","Package":"p","Test":"TestC"}`
	passC    = `{"Action":"pass","Package":"p","Test":"TestC","Elapsed":1}`
	failPkg  = `{"Action":"fail","Package":"p","Elapsed":3}`
	passPkg  = `{"Action":"pass","Package":"p","Elapsed":3}`
)

func TestCheck(t *testing.T) {
	for _, tc := range []struct {
		name    string
		expect  string
		list    string
		json    string
		version string
		want    []string // substrings of the problems; none means it passes
	}{
		{
			name: "the expected failure fails and the rest pass",
			json: events(runA, passA, runB, failBsub, failB, runC, passC, failPkg),
		},
		{
			name: "an expected failure passes",
			json: events(runA, passA, runB, passB, runC, passC, passPkg),
			want: []string{"TestB passes on the emulator now"},
		},
		{
			name: "another test fails",
			json: events(runA, failA, runB, failB, runC, passC, failPkg),
			want: []string{"TestA failed"},
		},
		{
			name: "another test skips",
			json: events(runA, skipA, runB, failB, runC, passC, failPkg),
			want: []string{"TestA skipped"},
		},
		{
			name: "a test did not run (a panic or the binary's timeout)",
			json: events(runA, passA, runB, failB, failPkg),
			want: []string{"TestC did not finish"},
		},
		{
			name: "the package did not build",
			json: events(`{"Action":"build-fail","ImportPath":"p"}`, failPkg),
			want: []string{"TestA did not finish", "TestB did not finish", "TestC did not finish"},
		},
		{
			name: "no tests listed",
			list: "ok  	p	0.01s\n",
			json: events(passPkg),
			want: []string{"no tests"},
		},
		{
			name:   "a listed test does not exist",
			expect: expectFixture + "  - test: TestGone\n    missing: Demo Gone\n    doc: docs/aws-emulator.md#x\n",
			json:   events(runA, passA, runB, failB, runC, passC, failPkg),
			want:   []string{"TestGone is not a test"},
		},
		{
			name:   "an entry without its missing operation",
			expect: "release: 1.0.338\nexpected:\n  - test: TestB\n    doc: docs/aws-emulator.md#x\n",
			json:   events(runA, passA, runB, failB, runC, passC, failPkg),
			want:   []string{"TestB: no missing operation"},
		},
		{
			name:   "an entry that does not link the emulator's doc",
			expect: "release: 1.0.338\nexpected:\n  - test: TestB\n    missing: Demo DoThing\n    doc: https://example.com\n",
			json:   events(runA, passA, runB, failB, runC, passC, failPkg),
			want:   []string{"TestB: doc"},
		},
		{
			name:   "a test listed twice",
			expect: expectFixture + "  - test: TestB\n    missing: Demo DoThing\n    doc: docs/aws-emulator.md#x\n",
			json:   events(runA, passA, runB, failB, runC, passC, failPkg),
			want:   []string{"TestB is listed twice"},
		},
		{
			name:    "another emulator release",
			version: "1.0.340",
			json:    events(runA, passA, runB, failB, runC, passC, failPkg),
			want:    []string{"emulator 1.0.340 is installed, the list is for 1.0.338"},
		},
		{
			name: "a test that ran but is not in the list of tests",
			json: events(runA, passA, runB, failB, runC, passC, `{"Action":"run","Package":"p","Test":"TestD"}`, `{"Action":"fail","Package":"p","Test":"TestD"}`, failPkg),
			want: []string{"TestD failed"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expect, list, version := tc.expect, tc.list, tc.version
			if expect == "" {
				expect = expectFixture
			}
			if list == "" {
				list = listFixture
			}
			if version == "" {
				version = "1.0.338"
			}
			got, err := check([]byte(expect), list, tc.json, version)
			if err != nil {
				t.Fatal(err)
			}
			if len(tc.want) == 0 && len(got) > 0 {
				t.Fatalf("problems %q, want none", got)
			}
			all := strings.Join(got, "\n")
			for _, w := range tc.want {
				if !strings.Contains(all, w) {
					t.Errorf("problems %q, want one with %q", got, w)
				}
			}
		})
	}
}
