// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/configservice"
	cstypes "github.com/aws/aws-sdk-go-v2/service/configservice/types"
)

// F14, AWS Config, on moto's Config. moto 5.2.5 has the recorder, delivery
// channel, rule, aggregator, aggregation authorization and organization
// conformance pack calls, and none for organization config rules,
// conformance packs, remediation configurations, stored queries or
// connectors (those types are unproven on the fake). Where moto differs
// from AWS (probed 2026-11-09), the backends keep the property themselves:
//   - a recorder's RecordingMode and a rule's EvaluationModes are dropped
//     by moto's Put and never returned;
//   - an organization conformance pack has no tags in moto (TagResource
//     answers ResourceNotFoundException for its ARN);
//   - DescribeOrganizationConformancePacks fails without names, so the
//     backend always passes one;
//   - DescribeConfigRules answers a missing name with no rules, not an
//     error, when there are no rules at all; the read treats both as gone.
//
// Config's API and CloudFormation name most fields alike (RoleARN,
// RecordingGroup.AllSupported, Scope.ComplianceResourceTypes), so the
// properties go to and from the SDK's structs through encoding/json, which
// matches keys to Go field names without regard to case.

// toSDK fills an SDK struct from CloudFormation-cased properties.
func toSDK(v any, out any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// fromSDK is an SDK value as CloudFormation-cased properties (the Go field
// names), without nulls, empty lists or empty objects.
func fromSDK(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return prune(out)
}

func prune(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, x := range t {
			if p := prune(x); p != nil {
				out[k] = p
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		if len(t) == 0 {
			return nil
		}
		out := make([]any, 0, len(t))
		for _, x := range t {
			out = append(out, prune(x))
		}
		return out
	}
	return v
}

// pick copies the named properties that m has.
func pick(m map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			out[k] = v
		}
	}
	return out
}

// put sets the properties of src (from fromSDK) named in keys on dst.
func put(dst map[string]any, src any, keys ...string) {
	sm, _ := src.(map[string]any)
	for _, k := range keys {
		if v, ok := sm[k]; ok {
			dst[k] = v
		}
	}
}

// configTags is the Config API's tag list from CloudFormation's Tags.
func configTags(m map[string]any) []cstypes.Tag {
	tags := tagsOf(m["Tags"])
	var out []cstypes.Tag
	for _, k := range sortedKeys(tags) {
		out = append(out, cstypes.Tag{Key: aws.String(k), Value: aws.String(tags[k])})
	}
	return out
}

// readConfigTags sets m's Tags from ListTagsForResource.
func readConfigTags(ctx context.Context, c *configservice.Client, arn string, m map[string]any) error {
	out, err := c.ListTagsForResource(ctx, &configservice.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
	if err != nil {
		return err
	}
	tags := map[string]string{}
	for _, t := range out.Tags {
		tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	if len(tags) > 0 {
		m["Tags"] = tagList(tags)
	}
	return nil
}

// syncConfigTags changes a Config resource's tags from prev's to next's.
func syncConfigTags(ctx context.Context, c *configservice.Client, arn string, prev, next map[string]any) error {
	old, now := tagsOf(prev["Tags"]), tagsOf(next["Tags"])
	var gone []string
	for k := range old {
		if _, ok := now[k]; !ok {
			gone = append(gone, k)
		}
	}
	if len(gone) > 0 {
		if _, err := c.UntagResource(ctx, &configservice.UntagResourceInput{ResourceArn: aws.String(arn), TagKeys: gone}); err != nil {
			return err
		}
	}
	var changed []cstypes.Tag
	for _, k := range sortedKeys(now) {
		if old[k] != now[k] {
			changed = append(changed, cstypes.Tag{Key: aws.String(k), Value: aws.String(now[k])})
		}
	}
	if len(changed) > 0 {
		if _, err := c.TagResource(ctx, &configservice.TagResourceInput{ResourceArn: aws.String(arn), Tags: changed}); err != nil {
			return err
		}
	}
	return nil
}

// configErr maps a Config error: a NoSuch* code is NotFound, anything
// else InvalidRequest.
func configErr(id string, err error) error {
	if strings.HasPrefix(code(err), "NoSuch") {
		return NotFound("%s", id)
	}
	return InvalidRequest("%v", err)
}

// named is a backend's name for an object whose name property is unset:
// CloudFormation generates one.
type named struct {
	mu sync.Mutex
	n  int
}

func (b *named) name(m map[string]any, prop, prefix string) string {
	if n := str(m, prop); n != "" {
		return n
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.n++
	return fmt.Sprintf("%s-%d", prefix, b.n)
}

// ---- AWS::Config::ConfigurationRecorder ----

// ConfigurationRecorder records resource configuration changes. AWS allows
// one per region. StartedOnCreate (write-only, default true) starts it on
// create; AWS cannot start a recorder before it has a delivery channel,
// and the channel needs the recorder first, so a start without a channel
// is left to the channel's create ("CloudFormation starts the recorder as
// soon as the delivery channel is available", the AWS::Config documentation;
// the real handlers' order is to be checked on the first live run).
type ConfigurationRecorder struct {
	C    *configservice.Client
	mu   sync.Mutex
	mode map[string]any // name -> RecordingMode, which moto drops
}

// NewConfigurationRecorder makes the backend.
func NewConfigurationRecorder(cfg aws.Config) *ConfigurationRecorder {
	return &ConfigurationRecorder{C: configservice.NewFromConfig(cfg), mode: map[string]any{}}
}

func (b *ConfigurationRecorder) put(ctx context.Context, name string, m map[string]any) error {
	var rec cstypes.ConfigurationRecorder
	if err := toSDK(pick(m, "RoleARN", "RecordingGroup", "RecordingMode"), &rec); err != nil {
		return InvalidRequest("%v", err)
	}
	rec.Name = aws.String(name)
	if _, err := b.C.PutConfigurationRecorder(ctx, &configservice.PutConfigurationRecorderInput{ConfigurationRecorder: &rec}); err != nil {
		return InvalidRequest("%v", err)
	}
	b.mu.Lock()
	if v, ok := m["RecordingMode"]; ok {
		b.mode[name] = v
	} else {
		delete(b.mode, name)
	}
	b.mu.Unlock()
	return nil
}

func (b *ConfigurationRecorder) Create(ctx context.Context, m map[string]any) (string, error) {
	name := str(m, "Name")
	if name == "" {
		name = "default" // the name AWS gives the region's recorder
	}
	if _, err := b.Read(ctx, name); err == nil {
		return "", &HandlerError{"AlreadyExists", "configuration recorder " + name + " exists"}
	}
	if err := b.put(ctx, name, m); err != nil {
		return "", err
	}
	if start, ok := m["StartedOnCreate"].(bool); !ok || start {
		_, err := b.C.StartConfigurationRecorder(ctx, &configservice.StartConfigurationRecorderInput{ConfigurationRecorderName: aws.String(name)})
		if err != nil && code(err) != "NoAvailableDeliveryChannelException" {
			return "", InvalidRequest("%v", err)
		}
	}
	return name, nil
}

func (b *ConfigurationRecorder) Read(ctx context.Context, name string) (map[string]any, error) {
	out, err := b.C.DescribeConfigurationRecorders(ctx, &configservice.DescribeConfigurationRecordersInput{ConfigurationRecorderNames: []string{name}})
	if err != nil {
		return nil, configErr(name, err)
	}
	if len(out.ConfigurationRecorders) != 1 {
		return nil, NotFound("%s", name)
	}
	r := out.ConfigurationRecorders[0]
	m := map[string]any{"Name": name, "RoleARN": aws.ToString(r.RoleARN)}
	put(m, fromSDK(r), "RecordingGroup")
	if a := aws.ToString(r.Arn); a != "" {
		m["ResourceARN"] = a
	}
	b.mu.Lock()
	if v, ok := b.mode[name]; ok {
		m["RecordingMode"] = v
	}
	b.mu.Unlock()
	return m, nil
}

func (b *ConfigurationRecorder) Update(ctx context.Context, name string, _, next map[string]any) error {
	return b.put(ctx, name, next)
}

func (b *ConfigurationRecorder) Delete(ctx context.Context, name string) error {
	if _, err := b.C.DeleteConfigurationRecorder(ctx, &configservice.DeleteConfigurationRecorderInput{ConfigurationRecorderName: aws.String(name)}); err != nil {
		return configErr(name, err)
	}
	b.mu.Lock()
	delete(b.mode, name)
	b.mu.Unlock()
	return nil
}

// ---- AWS::Config::DeliveryChannel ----

// DeliveryChannel is where Config delivers snapshots and history: an S3
// bucket and optionally an SNS topic. It needs the region's recorder, and
// its create starts that recorder if it is not recording (see
// ConfigurationRecorder). AWS refuses to delete the last channel while the
// recorder runs, as moto does: delete the recorder first.
type DeliveryChannel struct{ C *configservice.Client }

// NewDeliveryChannel makes the backend.
func NewDeliveryChannel(cfg aws.Config) *DeliveryChannel {
	return &DeliveryChannel{C: configservice.NewFromConfig(cfg)}
}

func (b *DeliveryChannel) put(ctx context.Context, name string, m map[string]any) error {
	var dc cstypes.DeliveryChannel
	if err := toSDK(pick(m, "S3BucketName", "S3KeyPrefix", "S3KmsKeyArn", "SnsTopicARN", "ConfigSnapshotDeliveryProperties"), &dc); err != nil {
		return InvalidRequest("%v", err)
	}
	dc.Name = aws.String(name)
	if _, err := b.C.PutDeliveryChannel(ctx, &configservice.PutDeliveryChannelInput{DeliveryChannel: &dc}); err != nil {
		return InvalidRequest("%v", err)
	}
	return nil
}

func (b *DeliveryChannel) Create(ctx context.Context, m map[string]any) (string, error) {
	name := str(m, "Name")
	if name == "" {
		name = "default"
	}
	if _, err := b.Read(ctx, name); err == nil {
		return "", &HandlerError{"AlreadyExists", "delivery channel " + name + " exists"}
	}
	if err := b.put(ctx, name, m); err != nil {
		return "", err
	}
	st, err := b.C.DescribeConfigurationRecorderStatus(ctx, &configservice.DescribeConfigurationRecorderStatusInput{})
	if err != nil {
		return "", err
	}
	for _, s := range st.ConfigurationRecordersStatus {
		if !s.Recording {
			if _, err := b.C.StartConfigurationRecorder(ctx, &configservice.StartConfigurationRecorderInput{ConfigurationRecorderName: s.Name}); err != nil {
				return "", InvalidRequest("%v", err)
			}
		}
	}
	return name, nil
}

func (b *DeliveryChannel) Read(ctx context.Context, name string) (map[string]any, error) {
	out, err := b.C.DescribeDeliveryChannels(ctx, &configservice.DescribeDeliveryChannelsInput{DeliveryChannelNames: []string{name}})
	if err != nil {
		return nil, configErr(name, err)
	}
	if len(out.DeliveryChannels) != 1 {
		return nil, NotFound("%s", name)
	}
	m := map[string]any{"Name": name}
	put(m, fromSDK(out.DeliveryChannels[0]), "S3BucketName", "S3KeyPrefix", "S3KmsKeyArn", "SnsTopicARN", "ConfigSnapshotDeliveryProperties")
	return m, nil
}

func (b *DeliveryChannel) Update(ctx context.Context, name string, _, next map[string]any) error {
	return b.put(ctx, name, next)
}

func (b *DeliveryChannel) Delete(ctx context.Context, name string) error {
	if _, err := b.C.DeleteDeliveryChannel(ctx, &configservice.DeleteDeliveryChannelInput{DeliveryChannelName: aws.String(name)}); err != nil {
		return configErr(name, err)
	}
	return nil
}

// ---- AWS::Config::ConfigRule ----

// ConfigRule is a managed or custom rule. InputParameters is a JSON string
// in the API and a string or an object in CloudFormation; the backend
// returns it in the form it was given. The identifier is the rule's name.
type ConfigRule struct {
	C *configservice.Client
	named
	mu    sync.Mutex
	extra map[string]map[string]any // name -> InputParameters as given, EvaluationModes
}

// NewConfigRule makes the backend.
func NewConfigRule(cfg aws.Config) *ConfigRule {
	return &ConfigRule{C: configservice.NewFromConfig(cfg), extra: map[string]map[string]any{}}
}

func (b *ConfigRule) put(ctx context.Context, name string, m map[string]any) error {
	var r cstypes.ConfigRule
	if err := toSDK(pick(m, "Description", "Scope", "Source", "MaximumExecutionFrequency", "EvaluationModes"), &r); err != nil {
		return InvalidRequest("%v", err)
	}
	r.ConfigRuleName = aws.String(name)
	switch p := m["InputParameters"].(type) {
	case string:
		r.InputParameters = aws.String(p)
	case map[string]any:
		s, _ := json.Marshal(p)
		r.InputParameters = aws.String(string(s))
	}
	if _, err := b.C.PutConfigRule(ctx, &configservice.PutConfigRuleInput{ConfigRule: &r}); err != nil {
		return InvalidRequest("%v", err)
	}
	b.mu.Lock()
	b.extra[name] = pick(m, "InputParameters", "EvaluationModes")
	b.mu.Unlock()
	return nil
}

func (b *ConfigRule) Create(ctx context.Context, m map[string]any) (string, error) {
	name := b.name(m, "ConfigRuleName", "config-rule")
	if _, err := b.Read(ctx, name); err == nil {
		return "", &HandlerError{"AlreadyExists", "config rule " + name + " exists"}
	}
	if err := b.put(ctx, name, m); err != nil {
		return "", err
	}
	return name, nil
}

func (b *ConfigRule) Read(ctx context.Context, name string) (map[string]any, error) {
	out, err := b.C.DescribeConfigRules(ctx, &configservice.DescribeConfigRulesInput{ConfigRuleNames: []string{name}})
	if err != nil {
		return nil, configErr(name, err)
	}
	if len(out.ConfigRules) != 1 {
		return nil, NotFound("%s", name)
	}
	r := out.ConfigRules[0]
	m := map[string]any{"ConfigRuleName": name, "Arn": aws.ToString(r.ConfigRuleArn), "ConfigRuleId": aws.ToString(r.ConfigRuleId)}
	sdk := fromSDK(r)
	put(m, sdk, "Description", "Scope", "Source", "MaximumExecutionFrequency", "EvaluationModes")
	// The policy text is write-only: never returned.
	if src, ok := m["Source"].(map[string]any); ok {
		if cp, ok := src["CustomPolicyDetails"].(map[string]any); ok {
			delete(cp, "PolicyText")
		}
	}
	if p := aws.ToString(r.InputParameters); p != "" {
		m["InputParameters"] = p
	}
	b.mu.Lock()
	for k, v := range b.extra[name] {
		m[k] = v
	}
	b.mu.Unlock()
	return m, nil
}

func (b *ConfigRule) Update(ctx context.Context, name string, _, next map[string]any) error {
	return b.put(ctx, name, next)
}

func (b *ConfigRule) Delete(ctx context.Context, name string) error {
	if _, err := b.C.DeleteConfigRule(ctx, &configservice.DeleteConfigRuleInput{ConfigRuleName: aws.String(name)}); err != nil {
		return configErr(name, err)
	}
	b.mu.Lock()
	delete(b.extra, name)
	b.mu.Unlock()
	return nil
}

// ---- AWS::Config::ConfigurationAggregator ----

// ConfigurationAggregator collects Config data from accounts and regions,
// or from an organization. The identifier is its name.
type ConfigurationAggregator struct {
	C *configservice.Client
	named
}

// NewConfigurationAggregator makes the backend.
func NewConfigurationAggregator(cfg aws.Config) *ConfigurationAggregator {
	return &ConfigurationAggregator{C: configservice.NewFromConfig(cfg)}
}

func (b *ConfigurationAggregator) put(ctx context.Context, name string, m map[string]any, tags []cstypes.Tag) error {
	in := &configservice.PutConfigurationAggregatorInput{}
	if err := toSDK(pick(m, "AccountAggregationSources", "OrganizationAggregationSource"), in); err != nil {
		return InvalidRequest("%v", err)
	}
	in.ConfigurationAggregatorName, in.Tags = aws.String(name), tags
	if _, err := b.C.PutConfigurationAggregator(ctx, in); err != nil {
		return InvalidRequest("%v", err)
	}
	return nil
}

func (b *ConfigurationAggregator) Create(ctx context.Context, m map[string]any) (string, error) {
	name := b.name(m, "ConfigurationAggregatorName", "config-aggregator")
	if _, err := b.Read(ctx, name); err == nil {
		return "", &HandlerError{"AlreadyExists", "configuration aggregator " + name + " exists"}
	}
	if err := b.put(ctx, name, m, configTags(m)); err != nil {
		return "", err
	}
	return name, nil
}

func (b *ConfigurationAggregator) Read(ctx context.Context, name string) (map[string]any, error) {
	out, err := b.C.DescribeConfigurationAggregators(ctx, &configservice.DescribeConfigurationAggregatorsInput{ConfigurationAggregatorNames: []string{name}})
	if err != nil {
		return nil, configErr(name, err)
	}
	if len(out.ConfigurationAggregators) != 1 {
		return nil, NotFound("%s", name)
	}
	a := out.ConfigurationAggregators[0]
	arn := aws.ToString(a.ConfigurationAggregatorArn)
	m := map[string]any{"ConfigurationAggregatorName": name, "ConfigurationAggregatorArn": arn}
	put(m, fromSDK(a), "AccountAggregationSources", "OrganizationAggregationSource")
	if err := readConfigTags(ctx, b.C, arn, m); err != nil {
		return nil, err
	}
	return m, nil
}

// Update puts the aggregator again with next's tags (moto's Put replaces
// them: without tags it leaves none), then removes any tag next lacks.
func (b *ConfigurationAggregator) Update(ctx context.Context, name string, _, next map[string]any) error {
	if err := b.put(ctx, name, next, configTags(next)); err != nil {
		return err
	}
	cur, err := b.Read(ctx, name)
	if err != nil {
		return err
	}
	return syncConfigTags(ctx, b.C, str(cur, "ConfigurationAggregatorArn"), cur, next)
}

func (b *ConfigurationAggregator) Delete(ctx context.Context, name string) error {
	if _, err := b.C.DeleteConfigurationAggregator(ctx, &configservice.DeleteConfigurationAggregatorInput{ConfigurationAggregatorName: aws.String(name)}); err != nil {
		return configErr(name, err)
	}
	return nil
}

// ---- AWS::Config::AggregationAuthorization ----

// AggregationAuthorization lets another account's aggregator collect this
// account's data in a region. Both properties are create-only and together
// the identifier ("<account>|<region>"); only the tags change.
type AggregationAuthorization struct{ C *configservice.Client }

// NewAggregationAuthorization makes the backend.
func NewAggregationAuthorization(cfg aws.Config) *AggregationAuthorization {
	return &AggregationAuthorization{C: configservice.NewFromConfig(cfg)}
}

func (b *AggregationAuthorization) Create(ctx context.Context, m map[string]any) (string, error) {
	id := str(m, "AuthorizedAccountId") + "|" + str(m, "AuthorizedAwsRegion")
	if _, err := b.Read(ctx, id); err == nil {
		return "", &HandlerError{"AlreadyExists", "aggregation authorization " + id + " exists"}
	}
	if _, err := b.C.PutAggregationAuthorization(ctx, &configservice.PutAggregationAuthorizationInput{
		AuthorizedAccountId: aws.String(str(m, "AuthorizedAccountId")),
		AuthorizedAwsRegion: aws.String(str(m, "AuthorizedAwsRegion")),
		Tags:                configTags(m),
	}); err != nil {
		return "", InvalidRequest("%v", err)
	}
	return id, nil
}

func (b *AggregationAuthorization) Read(ctx context.Context, id string) (map[string]any, error) {
	account, region, _ := strings.Cut(id, "|")
	p := configservice.NewDescribeAggregationAuthorizationsPaginator(b.C, &configservice.DescribeAggregationAuthorizationsInput{})
	for p.HasMorePages() {
		out, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, a := range out.AggregationAuthorizations {
			if aws.ToString(a.AuthorizedAccountId) != account || aws.ToString(a.AuthorizedAwsRegion) != region {
				continue
			}
			arn := aws.ToString(a.AggregationAuthorizationArn)
			m := map[string]any{"AuthorizedAccountId": account, "AuthorizedAwsRegion": region, "AggregationAuthorizationArn": arn}
			if err := readConfigTags(ctx, b.C, arn, m); err != nil {
				return nil, err
			}
			return m, nil
		}
	}
	return nil, NotFound("%s", id)
}

func (b *AggregationAuthorization) Update(ctx context.Context, id string, prev, next map[string]any) error {
	cur, err := b.Read(ctx, id)
	if err != nil {
		return err
	}
	return syncConfigTags(ctx, b.C, str(cur, "AggregationAuthorizationArn"), prev, next)
}

func (b *AggregationAuthorization) Delete(ctx context.Context, id string) error {
	// moto answers a delete of one that does not exist with success.
	if _, err := b.Read(ctx, id); err != nil {
		return err
	}
	account, region, _ := strings.Cut(id, "|")
	_, err := b.C.DeleteAggregationAuthorization(ctx, &configservice.DeleteAggregationAuthorizationInput{
		AuthorizedAccountId: aws.String(account), AuthorizedAwsRegion: aws.String(region),
	})
	return err
}

// ---- AWS::Config::OrganizationConformancePack ----

// OrganizationConformancePack deploys a conformance pack to the
// organization's accounts. The template (TemplateS3Uri or TemplateBody) is
// write-only, and the API's Put needs it on every update, so the backend
// keeps the last one given, as the handler must; it keeps the tags too
// (moto has none for these packs).
type OrganizationConformancePack struct {
	C     *configservice.Client
	mu    sync.Mutex
	extra map[string]map[string]any // name -> TemplateS3Uri/TemplateBody, Tags
}

// NewOrganizationConformancePack makes the backend.
func NewOrganizationConformancePack(cfg aws.Config) *OrganizationConformancePack {
	return &OrganizationConformancePack{C: configservice.NewFromConfig(cfg), extra: map[string]map[string]any{}}
}

func (b *OrganizationConformancePack) put(ctx context.Context, name string, m map[string]any) error {
	in := &configservice.PutOrganizationConformancePackInput{}
	b.mu.Lock()
	tpl := pick(b.extra[name], "TemplateS3Uri", "TemplateBody")
	b.mu.Unlock()
	if t := pick(m, "TemplateS3Uri", "TemplateBody"); len(t) > 0 {
		tpl = t
	}
	src := pick(m, "DeliveryS3Bucket", "DeliveryS3KeyPrefix", "ConformancePackInputParameters", "ExcludedAccounts")
	for k, v := range tpl {
		src[k] = v
	}
	if err := toSDK(src, in); err != nil {
		return InvalidRequest("%v", err)
	}
	in.OrganizationConformancePackName = aws.String(name)
	if _, err := b.C.PutOrganizationConformancePack(ctx, in); err != nil {
		return InvalidRequest("%v", err)
	}
	b.mu.Lock()
	b.extra[name] = tpl
	if v, ok := m["Tags"]; ok {
		b.extra[name]["Tags"] = v
	}
	b.mu.Unlock()
	return nil
}

func (b *OrganizationConformancePack) Create(ctx context.Context, m map[string]any) (string, error) {
	name := str(m, "OrganizationConformancePackName")
	if _, err := b.Read(ctx, name); err == nil {
		return "", &HandlerError{"AlreadyExists", "organization conformance pack " + name + " exists"}
	}
	if err := b.put(ctx, name, m); err != nil {
		return "", err
	}
	return name, nil
}

func (b *OrganizationConformancePack) Read(ctx context.Context, name string) (map[string]any, error) {
	out, err := b.C.DescribeOrganizationConformancePacks(ctx, &configservice.DescribeOrganizationConformancePacksInput{OrganizationConformancePackNames: []string{name}})
	if err != nil {
		return nil, configErr(name, err)
	}
	if len(out.OrganizationConformancePacks) != 1 {
		return nil, NotFound("%s", name)
	}
	p := out.OrganizationConformancePacks[0]
	m := map[string]any{"OrganizationConformancePackName": name, "OrganizationConformancePackArn": aws.ToString(p.OrganizationConformancePackArn)}
	put(m, fromSDK(p), "DeliveryS3Bucket", "DeliveryS3KeyPrefix", "ConformancePackInputParameters", "ExcludedAccounts")
	b.mu.Lock()
	if v, ok := b.extra[name]["Tags"]; ok {
		m["Tags"] = v
	}
	b.mu.Unlock()
	return m, nil
}

func (b *OrganizationConformancePack) Update(ctx context.Context, name string, _, next map[string]any) error {
	return b.put(ctx, name, next)
}

func (b *OrganizationConformancePack) Delete(ctx context.Context, name string) error {
	if _, err := b.C.DeleteOrganizationConformancePack(ctx, &configservice.DeleteOrganizationConformancePackInput{OrganizationConformancePackName: aws.String(name)}); err != nil {
		return configErr(name, err)
	}
	b.mu.Lock()
	delete(b.extra, name)
	b.mu.Unlock()
	return nil
}
