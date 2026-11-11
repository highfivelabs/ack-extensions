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

// The sandbox account's CloudFormation template (docs/live-runs.md), for
// the builder to deploy; nothing here deploys it. Generated so its
// permissions follow cloudcontrol/types.yaml:
//
//   - an IAM OIDC provider for accounts.google.com, whose client ID list
//     is the audience the offload VM asks Google's metadata server for;
//   - the live-test role, assumable only with AssumeRoleWithWebIdentity
//     by that one Google service account (its unique ID, the token's sub),
//     for an hour at most; its permissions: every served type's handler
//     permissions and Cloud Control actions (as task cloudcontrol:iam
//     writes for an environment, here for all types), the list handlers'
//     and cloudformation:ListResources for the sweeper, and the tests' own
//     fixtures (liveTestActions in types.yaml), one statement per
//     service (iamgen.Compact), split into managed policies of at most
//     6,144 characters;
//   - no permissions for a type marked neverDelete (a landing zone): no
//     live test makes one, since a test cannot delete what it made, and
//     the sweeper never deletes one;
//   - a monthly cost budget with e-mail alerts at 80% of actual and 100%
//     of forecast spend.
func writeLiveTemplate(out string, cfg config, gens []generated) error {
	var results []iamgen.Result
	extra := map[string]bool{"cloudformation:ListResources": true}
	for _, g := range gens {
		if g.NeverDelete {
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
	results = append(results, iamgen.Result{Policy: iamgen.Policy{Statement: []iamgen.Statement{{Sid: "LiveRunSweeperAndFixtures", Effect: "Allow", Action: acts, Resource: "*"}}}})
	// Compacted (one statement per service, the same actions): every type's
	// handler statements kept apart would pass IAM's ten managed policies
	// per role at about 70 types (docs/throughput.md).
	policies, err := iamgen.Split(iamgen.Compact(iamgen.Merge(results)), 6144)
	if err != nil {
		return err
	}
	if len(policies) > 10 {
		return fmt.Errorf("the live-test role needs %d managed policies, over IAM's default of 10 per role", len(policies))
	}
	ref := func(n string) map[string]any { return map[string]any{"Ref": n} }
	resources := map[string]any{
		"GoogleOIDCProvider": map[string]any{
			"Type": "AWS::IAM::OIDCProvider",
			"Properties": map[string]any{
				"Url": "https://accounts.google.com",
				// IAM checks the token's audience against this list. Google
				// ID tokens from a metadata server carry the requested
				// audience in aud and the service account's unique ID in
				// azp; both are listed, since which one IAM compares for
				// accounts.google.com is to be confirmed on the first deploy.
				"ClientIdList": []any{ref("Audience"), ref("GoogleServiceAccountUniqueId")},
			},
		},
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
	}
	var arns []any
	for i, p := range policies {
		name := fmt.Sprintf("LiveTestPolicy%d", i+1)
		resources[name] = map[string]any{
			"Type":       "AWS::IAM::ManagedPolicy",
			"Properties": map[string]any{"Description": "ack-extensions live tests, part " + fmt.Sprint(i+1), "PolicyDocument": p},
		}
		arns = append(arns, ref(name))
	}
	resources["LiveTestRole"] = map[string]any{
		"Type": "AWS::IAM::Role",
		"Properties": map[string]any{
			"RoleName":           "ack-extensions-live-tests",
			"Description":        "Assumed only by the offload VMs' Google service account, through web identity federation, for task test-live (ack-extensions docs/live-runs.md).",
			"MaxSessionDuration": 3600,
			"ManagedPolicyArns":  arns,
			"Tags":               []any{map[string]any{"Key": "purpose", "Value": "ack-extensions-live-tests"}},
			"AssumeRolePolicyDocument": map[string]any{
				"Version": "2012-10-17",
				"Statement": []any{map[string]any{
					"Sid":       "OnlyTheOffloadServiceAccount",
					"Effect":    "Allow",
					"Principal": map[string]any{"Federated": ref("GoogleOIDCProvider")},
					"Action":    "sts:AssumeRoleWithWebIdentity",
					"Condition": map[string]any{"StringEquals": map[string]any{
						// sub is the service account's unique ID, which no
						// other principal's token carries.
						"accounts.google.com:sub": ref("GoogleServiceAccountUniqueId"),
					}},
				}},
			},
		},
	}
	tpl := map[string]any{
		"AWSTemplateFormatVersion": "2010-09-09",
		"Description":              "ack-extensions live tests: Google federation for the offload VMs' service account, the test role, and a budget alarm. Generated by hack/cloudcontrol-gen from cloudcontrol/types.yaml; deployed by the builder, never by an agent (docs/live-runs.md).",
		"Parameters": map[string]any{
			"GoogleServiceAccountUniqueId": map[string]any{"Type": "String", "AllowedPattern": "^[0-9]{21}$", "Description": "The offload VMs' Google service account's unique ID (gcloud iam service-accounts describe EMAIL --format='value(uniqueId)')."},
			"Audience":                     map[string]any{"Type": "String", "Default": "ack-extensions-live-tests", "Description": "The audience hack/live/credentials.sh asks Google's metadata server for."},
			"MonthlyBudgetUSD":             map[string]any{"Type": "Number", "Default": 50, "MinValue": 1},
			"AlertEmail":                   map[string]any{"Type": "String", "Description": "Where the budget alerts go."},
		},
		"Resources": resources,
		"Outputs": map[string]any{
			"RoleArn":         map[string]any{"Value": map[string]any{"Fn::GetAtt": []any{"LiveTestRole", "Arn"}}, "Description": "LIVE_ROLE_ARN for task test-live"},
			"OIDCProviderArn": map[string]any{"Value": ref("GoogleOIDCProvider")},
		},
	}
	y, err := yaml.Marshal(tpl)
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(out, "config/live/sandbox-account.yaml"), append([]byte("# "+header+"\n"), y...))
}
