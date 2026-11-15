// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/highfivelabs/ack-extensions/pkg/ackiam"
)

// liveACKExtra is tests/e2e/live-actions.yaml: what a live run of the
// ACK-kind tests needs beyond the kinds' own calls and the tests' SDK
// calls, which are read from the code: ACK's own controllers that the
// extension tests run (their kinds are not ours, so pkg/ackiam does not
// scan them), and what a fixture's call needs besides its own action.
type liveACKExtra struct {
	Actions []string `json:"actions"`
}

// sdkPrefixes are the IAM namespaces of SDK packages whose name is not
// their namespace and that pkg/ackiam does not know.
var sdkPrefixes = map[string]string{
	"elasticloadbalancingv2": "elasticloadbalancing",
	"cloudwatchlogs":         "logs",
}

// liveACKActions are the live-test role's actions for the ACK-kind tests
// (task test-live SUITE=ack, docs/live-runs.md): every ACK-generated kind's
// calls (pkg/ackiam, as task ack:iam writes for an environment) but the
// Organizations kinds' (they need the organization's management account,
// which the sandbox account is not), the SDK operations the tests call
// themselves (each &<service>.<Operation>Input literal in tests/e2e, the
// Organizations tests' left out), and tests/e2e/live-actions.yaml.
func liveACKActions(root string) ([]string, error) {
	kinds, err := ackiam.Scan(root)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, a := range ackiam.RuntimeActions {
		set[a] = true
	}
	for _, k := range kinds {
		if k.Service == "organizations" {
			continue
		}
		for _, a := range k.Actions() {
			set[a] = true
		}
	}
	files, err := filepath.Glob(filepath.Join(root, "tests/e2e/*.go"))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasPrefix(filepath.Base(f), "organizations_") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		pkgs := map[string]string{} // local name -> SDK service
		for _, imp := range file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			const sdk = "github.com/aws/aws-sdk-go-v2/service/"
			if !strings.HasPrefix(p, sdk) || strings.Contains(p[len(sdk):], "/") {
				continue
			}
			name := filepath.Base(p)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			pkgs[name] = filepath.Base(p)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := cl.Type.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok || pkgs[id.Name] == "" || !strings.HasSuffix(sel.Sel.Name, "Input") {
				return true
			}
			svc, op := pkgs[id.Name], strings.TrimSuffix(sel.Sel.Name, "Input")
			if p, ok := sdkPrefixes[svc]; ok {
				set[p+":"+op] = true
			} else {
				set[ackiam.Action(svc, op)] = true
			}
			return true
		})
	}
	b, err := os.ReadFile(filepath.Join(root, "tests/e2e/live-actions.yaml"))
	if err != nil {
		return nil, err
	}
	var extra liveACKExtra
	if err := yaml.UnmarshalStrict(b, &extra); err != nil {
		return nil, err
	}
	for _, a := range extra.Actions {
		set[a] = true
	}
	out := make([]string, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Strings(out)
	return out, nil
}
