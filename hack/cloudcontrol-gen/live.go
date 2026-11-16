// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"path/filepath"
	"sort"

	"sigs.k8s.io/yaml"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/iamgen"
)

// maxLivePolicies is IAM's default quota of managed policies per role.
const maxLivePolicies = 10

// liveKinds checks types.yaml's liveKinds (each a served type, named
// once, not neverDelete) and returns them as a set.
func liveKinds(cfg config) (map[string]bool, error) {
	served := map[string]bool{}
	for _, t := range cfg.Types {
		served[t.TypeName] = !t.NeverDelete
	}
	live := map[string]bool{}
	for _, k := range cfg.LiveKinds {
		deletable, ok := served[k]
		switch {
		case !ok:
			return nil, fmt.Errorf("liveKinds: %s is not a served type", k)
		case !deletable:
			return nil, fmt.Errorf("liveKinds: %s is neverDelete; no live test may make one", k)
		case live[k]:
			return nil, fmt.Errorf("liveKinds: %s is listed twice", k)
		}
		live[k] = true
	}
	if len(live) == 0 {
		return nil, fmt.Errorf("liveKinds: empty; the sandbox role needs at least one kind")
	}
	return live, nil
}

// liveSplitChars is the size the live-test policies are split to, short of
// IAM's 6,144 for the chart's longer rendering (writeLiveTemplate).
const liveSplitChars = 5900

// The live-test role's managed policies and the sandbox account's
// CloudFormation template (docs/live-runs.md); nothing here deploys either.
//
// The role ack-extensions-live-tests is an instance of the catalog's
// google-federated-role chart (its tests/ack-extensions-live-tests.values.yaml),
// deployed through ACK, which adopted the role the first stack made
// (2026-11-15, AWSROLE-ADOPT). What it may do follows
// cloudcontrol/types.yaml, so its policies are generated here, in that
// chart's values shape (config/live/role-policies.yaml, the instance's
// policies value):
//
//   - the handler permissions and Cloud Control actions of the kinds
//     sampled on live runs (liveKinds in types.yaml, as task
//     cloudcontrol:iam writes for an environment), their list handlers'
//     and cloudformation:ListResources for the sweeper, the tests' own
//     fixtures (liveTestActions), and the ACK-kind suite's calls
//     (statement LiveRunACKKinds, liveACKActions), one statement per service
//     (iamgen.Compact), split into managed policies of at most 6,144
//     characters, ten at most (IAM's default per role); every other kind
//     is proven on the fakes only (plan question 21);
//   - no permissions for a type marked neverDelete (a landing zone): no
//     live test makes one, since a test cannot delete what it made, and
//     the sweeper never deletes one (liveKinds may not name one).
//
// The CloudFormation template (config/live/sandbox-account.yaml) keeps
// only the monthly cost budget with e-mail alerts at 80% of actual and
// 100% of forecast spend, until an ACK controller or an ack-extensions
// kind serves AWS Budgets.
func writeLiveTemplate(root, out string, cfg config, gens []generated) error {
	var results []iamgen.Result
	extra := map[string]bool{"cloudformation:ListResources": true}
	for _, g := range gens {
		if !g.Live || g.NeverDelete {
			continue
		}
		results = append(results, g.IAM)
		for _, a := range g.IAM.ListOnly {
			extra[a] = true
		}
	}
	for _, a := range cfg.LiveTestActions {
		extra[a] = true
	}
	var acts []string
	for a := range extra {
		acts = append(acts, a)
	}
	sort.Strings(acts)
	// Appended after Merge, not passed to it as a result: Merge keeps a
	// result's statements after the first (the first is its Cloud Control
	// statement, merged into one), so a one-statement result was dropped,
	// and the first deployed role had no sweeper or fixture permissions.
	merged := iamgen.Merge(results)
	merged.Statement = append(merged.Statement, iamgen.Statement{Sid: "LiveRunSweeperAndFixtures", Effect: "Allow", Action: acts, Resource: "*"})
	// The ACK-kind tests' (task test-live SUITE=ack): the kinds' calls and
	// the tests' fixtures (liveACKActions).
	ack, err := liveACKActions(root)
	if err != nil {
		return err
	}
	merged.Statement = append(merged.Statement, iamgen.Statement{Sid: "LiveRunACKKinds", Effect: "Allow", Action: ack, Resource: "*"})
	// Compacted (one statement per service, the same actions): every type's
	// handler statements kept apart would pass IAM's ten managed policies
	// per role at about 70 types (docs/throughput.md).
	// Split short of IAM's 6,144 characters: the catalog chart renders each
	// statement's Resource as a list (["*"], not "*") and so writes a longer
	// document than iamgen measures; part-1 came to 6,162 in the chart when
	// split at 6,144 here (2026-11-16).
	policies, err := iamgen.Split(iamgen.Compact(merged), liveSplitChars)
	if err != nil {
		return err
	}
	if len(policies) > maxLivePolicies {
		return fmt.Errorf("the live-test role needs %d managed policies, over IAM's default of %d per role: sample fewer liveKinds in cloudcontrol/types.yaml", len(policies), maxLivePolicies)
	}
	if err := writeLivePolicies(out, policies); err != nil {
		return err
	}
	ref := func(n string) map[string]any { return map[string]any{"Ref": n} }
	tpl := map[string]any{
		"AWSTemplateFormatVersion": "2010-09-09",
		"Description":              "ack-extensions live tests: the sandbox account's monthly budget alarm. The live-test role is a catalog google-federated-role instance on ACK (docs/live-runs.md). Generated by hack/cloudcontrol-gen; deployed in the AWS test account only.",
		"Parameters": map[string]any{
			"MonthlyBudgetUSD": map[string]any{"Type": "Number", "Default": 50, "MinValue": 1},
			"AlertEmail":       map[string]any{"Type": "String", "Description": "Where the budget alerts go."},
		},
		"Resources": map[string]any{
			"BudgetAlarm": map[string]any{
				"Type": "AWS::Budgets::Budget",
				"Properties": map[string]any{
					"Budget": map[string]any{
						"BudgetName": "ack-extensions-live-tests", "BudgetType": "COST", "TimeUnit": "MONTHLY",
						"BudgetLimit": map[string]any{"Amount": ref("MonthlyBudgetUSD"), "Unit": "USD"},
					},
					"NotificationsWithSubscribers": []any{
						map[string]any{
							"Notification": map[string]any{"NotificationType": "ACTUAL", "ComparisonOperator": "GREATER_THAN", "Threshold": 80, "ThresholdType": "PERCENTAGE"},
							"Subscribers":  []any{map[string]any{"SubscriptionType": "EMAIL", "Address": ref("AlertEmail")}},
						},
						map[string]any{
							"Notification": map[string]any{"NotificationType": "FORECASTED", "ComparisonOperator": "GREATER_THAN", "Threshold": 100, "ThresholdType": "PERCENTAGE"},
							"Subscribers":  []any{map[string]any{"SubscriptionType": "EMAIL", "Address": ref("AlertEmail")}},
						},
					},
				},
			},
		},
	}
	y, err := yaml.Marshal(tpl)
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(out, "config/live/sandbox-account.yaml"), append([]byte("# "+header+"\n"), y...))
}

// livePolicy is one managed policy in the google-federated-role chart's
// values shape: policies.<key>.{description, statements}, each statement
// in the iam module's shape.
type livePolicy struct {
	Description string          `json:"description"`
	Statements  []liveStatement `json:"statements"`
}

type liveStatement struct {
	Sid       string   `json:"sid"`
	Effect    string   `json:"effect"`
	Actions   []string `json:"actions"`
	Resources []string `json:"resources"`
}

// writeLivePolicies writes config/live/role-policies.yaml: the live-test
// role's managed policies as the catalog chart instance's policies value
// (keys part-1, part-2, ...; the chart names each policy
// ack-extensions-live-tests-<key>).
func writeLivePolicies(out string, policies []iamgen.Policy) error {
	vals := map[string]livePolicy{}
	for i, p := range policies {
		lp := livePolicy{Description: fmt.Sprintf("ack-extensions live tests, part %d", i+1)}
		for _, st := range p.Statement {
			lp.Statements = append(lp.Statements, liveStatement{Sid: st.Sid, Effect: st.Effect, Actions: st.Action, Resources: []string{st.Resource}})
		}
		vals[fmt.Sprintf("part-%d", i+1)] = lp
	}
	y, err := yaml.Marshal(map[string]any{"policies": vals})
	if err != nil {
		return err
	}
	head := "# " + header + "\n" +
		"# The live-test role's managed policies (ack-extensions-live-tests), as the\n" +
		"# policies value of the catalog's google-federated-role chart: its instance\n" +
		"# tests/ack-extensions-live-tests.values.yaml takes these statements.\n" +
		"# When liveKinds or liveTestActions change, copy them there and apply the\n" +
		"# instance (docs/live-runs.md).\n"
	return writeFile(filepath.Join(out, "config/live/role-policies.yaml"), append([]byte(head), y...))
}
