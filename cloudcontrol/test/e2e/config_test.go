// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/configservice"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// F14, AWS Config: the recorder and its delivery channel (the channel's
// create starts the recorder), a managed rule, an aggregator, the
// authorization another account's aggregator needs, and an organization
// conformance pack; each made, left alone through resyncs, changed where
// it can be, refused a create-only change, and deleted.
func TestConfig(t *testing.T) {
	e := start(t)
	cs := configservice.NewFromConfig(e.moto)
	ic := iam.NewFromConfig(e.moto)
	sc := s3.NewFromConfig(e.moto, func(o *s3.Options) { o.UsePathStyle = true })

	// Fixtures: the recorder's role and the channel's bucket.
	const role, bucket = "config-e2e-recorder", "config-e2e-delivery"
	r, err := ic.CreateRole(e.ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(role),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"config.amazonaws.com"},"Action":"sts:AssumeRole"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	roleARN := aws.ToString(r.Role.Arn)
	if _, err := sc.CreateBucket(e.ctx, &s3.CreateBucketInput{
		Bucket:                    aws.String(bucket),
		CreateBucketConfiguration: &s3types.CreateBucketConfiguration{LocationConstraint: s3types.BucketLocationConstraint(e.moto.Region)},
	}); err != nil {
		t.Fatal(err)
	}
	// The fixtures go at the end, after the objects (a live run leaves
	// nothing behind).
	t.Cleanup(func() {
		_, _ = sc.DeleteBucket(e.ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
		_, _ = ic.DeleteRole(e.ctx, &iam.DeleteRoleInput{RoleName: aws.String(role)})
	})
	recording := func() bool {
		out, err := cs.DescribeConfigurationRecorderStatus(e.ctx, &configservice.DescribeConfigurationRecorderStatusInput{})
		if err != nil {
			t.Fatal(err)
		}
		return len(out.ConfigurationRecordersStatus) == 1 && out.ConfigurationRecordersStatus[0].Recording
	}

	// The recorder: not recording until it has a channel.
	const (
		recType  = "AWS::Config::ConfigurationRecorder"
		dcType   = "AWS::Config::DeliveryChannel"
		ruleType = "AWS::Config::ConfigRule"
		aggType  = "AWS::Config::ConfigurationAggregator"
		authType = "AWS::Config::AggregationAuthorization"
		ocpType  = "AWS::Config::OrganizationConformancePack"
	)
	rec := obj("ConfigurationRecorder", "config-e2e-recorder", map[string]any{
		"name": "default", "roleARN": roleARN,
		"recordingGroup": map[string]any{"allSupported": true, "includeGlobalResourceTypes": false},
		"recordingMode":  map[string]any{"recordingFrequency": "CONTINUOUS"},
	})
	if err := e.c.Create(e.ctx, rec); err != nil {
		t.Fatal(err)
	}
	got := e.waitSynced(t, rec)
	if id, _, _ := unstructured.NestedString(got.Object, "status", "cloudControl", "identifier"); id != "default" {
		t.Errorf("recorder identifier %q, want default", id)
	}
	if recording() {
		t.Error("the recorder records before it has a delivery channel")
	}

	dc := obj("DeliveryChannel", "config-e2e-channel", map[string]any{
		"name": "default", "s3BucketName": bucket, "s3KeyPrefix": "config",
		"configSnapshotDeliveryProperties": map[string]any{"deliveryFrequency": "Six_Hours"},
	})
	if err := e.c.Create(e.ctx, dc); err != nil {
		t.Fatal(err)
	}
	e.waitSynced(t, dc)
	if !recording() {
		t.Error("the recorder does not record once its delivery channel exists")
	}

	// A managed rule, its parameters an object.
	rule := obj("ConfigRule", "config-e2e-rule", map[string]any{
		"configRuleName":  "config-e2e-s3-versioning",
		"description":     "S3 buckets keep versions",
		"source":          map[string]any{"owner": "AWS", "sourceIdentifier": "S3_BUCKET_VERSIONING_ENABLED"},
		"scope":           map[string]any{"complianceResourceTypes": []any{"AWS::S3::Bucket"}},
		"inputParameters": map[string]any{"isMfaDeleteEnabled": "false"},
		"evaluationModes": []any{map[string]any{"mode": "DETECTIVE"}},
	})
	if err := e.c.Create(e.ctx, rule); err != nil {
		t.Fatal(err)
	}
	got = e.waitSynced(t, rule)
	ruleARN, _, _ := unstructured.NestedString(got.Object, "status", "arn")
	ruleID, _, _ := unstructured.NestedString(got.Object, "status", "configRuleId")
	if !strings.Contains(ruleARN, ":config-rule/") || !strings.HasPrefix(ruleID, "config-rule-") {
		t.Errorf("rule status.arn %q, status.configRuleId %q", ruleARN, ruleID)
	}
	describeRule := func() (*configservice.DescribeConfigRulesOutput, error) {
		return cs.DescribeConfigRules(e.ctx, &configservice.DescribeConfigRulesInput{ConfigRuleNames: []string{"config-e2e-s3-versioning"}})
	}
	rules, err := describeRule()
	if err != nil || len(rules.ConfigRules) != 1 || aws.ToString(rules.ConfigRules[0].InputParameters) != `{"isMfaDeleteEnabled":"false"}` {
		t.Fatalf("moto rule: %v %+v", err, rules)
	}

	// An aggregator of this account's regions, and an authorization for
	// another account's aggregator, both tagged.
	agg := obj("ConfigurationAggregator", "config-e2e-aggregator", map[string]any{
		"configurationAggregatorName": "config-e2e-aggregator",
		"accountAggregationSources":   []any{map[string]any{"accountIds": []any{"123456789012"}, "allAwsRegions": true}},
		"tags":                        []any{map[string]any{"key": "team", "value": "platform"}},
	})
	auth := obj("AggregationAuthorization", "config-e2e-authorization", map[string]any{
		"authorizedAccountId": "210987654321", "authorizedAwsRegion": "us-east-1",
		"tags": []any{map[string]any{"key": "team", "value": "security"}},
	})
	// An organization conformance pack from a template in S3.
	ocp := obj("OrganizationConformancePack", "config-e2e-pack", map[string]any{
		"organizationConformancePackName": "config-e2e-pack",
		"templateS3Uri":                   "s3://" + bucket + "/pack.yaml",
		"deliveryS3Bucket":                "awsconfigconforms-config-e2e",
		"excludedAccounts":                []any{"111111111111"},
	})
	for _, u := range []*unstructured.Unstructured{agg, auth, ocp} {
		if err := e.c.Create(e.ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	got = e.waitSynced(t, agg)
	aggARN, _, _ := unstructured.NestedString(got.Object, "status", "configurationAggregatorArn")
	if !strings.Contains(aggARN, ":config-aggregator/") {
		t.Errorf("aggregator status.configurationAggregatorArn %q", aggARN)
	}
	got = e.waitSynced(t, auth)
	if id, _, _ := unstructured.NestedString(got.Object, "status", "cloudControl", "identifier"); id != "210987654321|us-east-1" {
		t.Errorf("authorization identifier %q, want 210987654321|us-east-1", id)
	}
	got = e.waitSynced(t, ocp)
	if a, _, _ := unstructured.NestedString(got.Object, "status", "organizationConformancePackArn"); !strings.Contains(a, ":organization-conformance-pack/config-e2e-pack") {
		t.Errorf("pack status.organizationConformancePackArn %q", a)
	}
	tags := func(arn string) map[string]string {
		out, err := cs.ListTagsForResource(e.ctx, &configservice.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		for _, tg := range out.Tags {
			m[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
		}
		return m
	}
	if tg := tags(aggARN); tg["team"] != "platform" {
		t.Errorf("aggregator tags in moto %v", tg)
	}

	// Resyncs make no update.
	all := []string{recType, dcType, ruleType, aggType, authType, ocpType}
	time.Sleep(12 * time.Second)
	for _, ty := range all {
		if n := len(e.calls("UpdateResource", ty)); n != 0 {
			t.Errorf("%d update(s) of an unchanged %s", n, ty)
		}
	}

	// Changes: the channel's frequency, the rule's description, a tag on
	// the aggregator, the pack's excluded accounts.
	if err := e.update(t, dc, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, "TwentyFour_Hours", "spec", "configSnapshotDeliveryProperties", "deliveryFrequency")
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.update(t, rule, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, "S3 buckets keep every version", "spec", "description")
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.update(t, agg, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedSlice(o.Object, []any{
			map[string]any{"key": "team", "value": "platform"},
			map[string]any{"key": "tier", "value": "foundation"},
		}, "spec", "tags")
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.update(t, ocp, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedSlice(o.Object, []any{"111111111111", "222222222222"}, "spec", "excludedAccounts")
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "the changes in moto", func() error {
		dcs, err := cs.DescribeDeliveryChannels(e.ctx, &configservice.DescribeDeliveryChannelsInput{DeliveryChannelNames: []string{"default"}})
		if err != nil {
			return err
		}
		if f := string(dcs.DeliveryChannels[0].ConfigSnapshotDeliveryProperties.DeliveryFrequency); f != "TwentyFour_Hours" {
			return fmt.Errorf("channel frequency %q", f)
		}
		rules, err := describeRule()
		if err != nil {
			return err
		}
		if d := aws.ToString(rules.ConfigRules[0].Description); d != "S3 buckets keep every version" {
			return fmt.Errorf("rule description %q", d)
		}
		if tg := tags(aggARN); tg["tier"] != "foundation" {
			return fmt.Errorf("aggregator tags %v", tg)
		}
		packs, err := cs.DescribeOrganizationConformancePacks(e.ctx, &configservice.DescribeOrganizationConformancePacksInput{OrganizationConformancePackNames: []string{"config-e2e-pack"}})
		if err != nil {
			return err
		}
		if x := packs.OrganizationConformancePacks[0].ExcludedAccounts; len(x) != 2 {
			return fmt.Errorf("pack excluded accounts %v", x)
		}
		return nil
	})

	// Names are create-only: the API server refuses a change.
	err = e.update(t, rule, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, "config-e2e-renamed", "spec", "configRuleName")
	})
	if err == nil || !strings.Contains(err.Error(), "create-only") {
		t.Errorf("a new rule name was not refused as create-only: %v", err)
	}

	// Deleted, the recorder before its channel: AWS keeps the last channel
	// while a recorder runs.
	for _, u := range []*unstructured.Unstructured{rule, agg, auth, ocp, rec, dc} {
		if err := e.c.Delete(e.ctx, u); err != nil {
			t.Fatal(err)
		}
		e.waitGone(t, u)
	}
	// moto answers a name it lacks with NoSuchConfigRuleException, or with
	// no rules when it has none at all.
	if out, err := describeRule(); err == nil && len(out.ConfigRules) != 0 {
		t.Error("the rule is still in moto after delete")
	}
	if _, err := cs.DescribeConfigurationRecorders(e.ctx, &configservice.DescribeConfigurationRecordersInput{ConfigurationRecorderNames: []string{"default"}}); err == nil {
		t.Error("the recorder is still in moto after delete")
	}
	if _, err := cs.DescribeDeliveryChannels(e.ctx, &configservice.DescribeDeliveryChannelsInput{DeliveryChannelNames: []string{"default"}}); err == nil {
		t.Error("the delivery channel is still in moto after delete")
	}
	if out, err := cs.DescribeAggregationAuthorizations(e.ctx, &configservice.DescribeAggregationAuthorizationsInput{}); err != nil || len(out.AggregationAuthorizations) != 0 {
		t.Errorf("authorizations in moto after delete: %v %+v", err, out)
	}
	for _, ty := range all {
		if n := len(e.calls("DeleteResource", ty)); n != 1 {
			t.Errorf("%d DeleteResource calls for %s, want 1", n, ty)
		}
	}
}
