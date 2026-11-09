// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/guardduty"
	gdtypes "github.com/aws/aws-sdk-go-v2/service/guardduty/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/smithy-go"
)

// The backends: what each type's CloudFormation handler does, done in moto
// through the real AWS SDK clients. They are the per-type part of the fake
// and stand in for AWS's handlers, so they are test code: the reconciler
// never sees them. Where moto lacks something a handler does, the backend
// keeps that property itself and says so.

func code(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	return ""
}

func str(m map[string]any, k string) string { v, _ := m[k].(string); return v }

func tagsOf(v any) map[string]string {
	out := map[string]string{}
	l, _ := v.([]any)
	for _, t := range l {
		tm, _ := t.(map[string]any)
		out[str(tm, "Key")] = str(tm, "Value")
	}
	return out
}

func tagList(m map[string]string) []any {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []any
	for _, k := range keys {
		out = append(out, map[string]any{"Key": k, "Value": m[k]})
	}
	return out
}

// ---- AWS::IAM::SAMLProvider on moto's IAM ----

// SAMLProvider is the IAM SAML provider backend. moto 5.2.5 implements
// Create/Get/Update/DeleteSAMLProvider with the name and metadata only: it
// has no tags, assertion encryption mode or private keys for SAML
// providers, so this backend keeps Tags, AssertionEncryptionMode and
// PrivateKeyList itself.
type SAMLProvider struct {
	IAM   *iam.Client
	mu    sync.Mutex
	extra map[string]map[string]any // ARN -> properties moto lacks
	n     int
}

// NewSAMLProvider makes the backend.
func NewSAMLProvider(cfg aws.Config) *SAMLProvider {
	return &SAMLProvider{IAM: iam.NewFromConfig(cfg), extra: map[string]map[string]any{}}
}

var samlExtra = []string{"Tags", "AssertionEncryptionMode", "PrivateKeyList"}

func (b *SAMLProvider) Create(ctx context.Context, m map[string]any) (string, error) {
	name := str(m, "Name")
	if name == "" {
		b.mu.Lock()
		b.n++
		name = fmt.Sprintf("samlprovider-%d", b.n)
		b.mu.Unlock()
	}
	out, err := b.IAM.CreateSAMLProvider(ctx, &iam.CreateSAMLProviderInput{
		Name: aws.String(name), SAMLMetadataDocument: aws.String(str(m, "SamlMetadataDocument")),
	})
	if err != nil {
		if code(err) == "EntityAlreadyExists" {
			return "", &HandlerError{"AlreadyExists", err.Error()}
		}
		return "", InvalidRequest("%v", err)
	}
	arn := aws.ToString(out.SAMLProviderArn)
	b.mu.Lock()
	b.extra[arn] = map[string]any{}
	for _, k := range samlExtra {
		if v, ok := m[k]; ok {
			b.extra[arn][k] = v
		}
	}
	b.mu.Unlock()
	return arn, nil
}

func (b *SAMLProvider) Read(ctx context.Context, arn string) (map[string]any, error) {
	out, err := b.IAM.GetSAMLProvider(ctx, &iam.GetSAMLProviderInput{SAMLProviderArn: aws.String(arn)})
	if err != nil {
		if code(err) == "NoSuchEntity" {
			return nil, NotFound("%s", arn)
		}
		return nil, err
	}
	m := map[string]any{
		"Arn":                  arn,
		"Name":                 arn[strings.LastIndex(arn, "/")+1:],
		"SamlMetadataDocument": aws.ToString(out.SAMLMetadataDocument),
	}
	b.mu.Lock()
	for k, v := range b.extra[arn] {
		m[k] = v
	}
	b.mu.Unlock()
	return m, nil
}

func (b *SAMLProvider) Update(ctx context.Context, arn string, prev, next map[string]any) error {
	if str(prev, "SamlMetadataDocument") != str(next, "SamlMetadataDocument") {
		if _, err := b.IAM.UpdateSAMLProvider(ctx, &iam.UpdateSAMLProviderInput{
			SAMLProviderArn: aws.String(arn), SAMLMetadataDocument: aws.String(str(next, "SamlMetadataDocument")),
		}); err != nil {
			return InvalidRequest("%v", err)
		}
	}
	b.mu.Lock()
	b.extra[arn] = map[string]any{}
	for _, k := range samlExtra {
		if v, ok := next[k]; ok {
			b.extra[arn][k] = v
		}
	}
	b.mu.Unlock()
	return nil
}

func (b *SAMLProvider) Delete(ctx context.Context, arn string) error {
	if _, err := b.Read(ctx, arn); err != nil {
		return err
	}
	if _, err := b.IAM.DeleteSAMLProvider(ctx, &iam.DeleteSAMLProviderInput{SAMLProviderArn: aws.String(arn)}); err != nil {
		return err
	}
	b.mu.Lock()
	delete(b.extra, arn)
	b.mu.Unlock()
	return nil
}

// ---- AWS::EC2::TransitGatewayRouteTable on moto's EC2 ----

// TransitGatewayRouteTable is the route table backend; moto implements all
// of it. moto keeps a deleted table with state "deleted", as AWS shows one
// for a while; the read treats it as gone, as the CloudFormation handler
// does.
type TransitGatewayRouteTable struct{ EC2 *ec2.Client }

// NewTransitGatewayRouteTable makes the backend.
func NewTransitGatewayRouteTable(cfg aws.Config) *TransitGatewayRouteTable {
	return &TransitGatewayRouteTable{EC2: ec2.NewFromConfig(cfg)}
}

func ec2Tags(m map[string]string) []ec2types.Tag {
	var out []ec2types.Tag
	for _, k := range sortedKeys(m) {
		out = append(out, ec2types.Tag{Key: aws.String(k), Value: aws.String(m[k])})
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (b *TransitGatewayRouteTable) Create(ctx context.Context, m map[string]any) (string, error) {
	in := &ec2.CreateTransitGatewayRouteTableInput{TransitGatewayId: aws.String(str(m, "TransitGatewayId"))}
	if tags := tagsOf(m["Tags"]); len(tags) > 0 {
		in.TagSpecifications = []ec2types.TagSpecification{{ResourceType: ec2types.ResourceTypeTransitGatewayRouteTable, Tags: ec2Tags(tags)}}
	}
	out, err := b.EC2.CreateTransitGatewayRouteTable(ctx, in)
	if err != nil {
		// The real handler's code for a missing gateway is not known; the
		// request cannot succeed as written, so InvalidRequest.
		return "", InvalidRequest("%v", err)
	}
	return aws.ToString(out.TransitGatewayRouteTable.TransitGatewayRouteTableId), nil
}

func (b *TransitGatewayRouteTable) Read(ctx context.Context, id string) (map[string]any, error) {
	out, err := b.EC2.DescribeTransitGatewayRouteTables(ctx, &ec2.DescribeTransitGatewayRouteTablesInput{TransitGatewayRouteTableIds: []string{id}})
	if err != nil {
		if strings.Contains(code(err), "NotFound") {
			return nil, NotFound("%s", id)
		}
		return nil, err
	}
	for _, t := range out.TransitGatewayRouteTables {
		if aws.ToString(t.TransitGatewayRouteTableId) != id || t.State == ec2types.TransitGatewayRouteTableStateDeleted {
			continue
		}
		m := map[string]any{"TransitGatewayRouteTableId": id, "TransitGatewayId": aws.ToString(t.TransitGatewayId)}
		tags := map[string]string{}
		for _, tg := range t.Tags {
			tags[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
		}
		if len(tags) > 0 {
			m["Tags"] = tagList(tags)
		}
		return m, nil
	}
	return nil, NotFound("%s", id)
}

func (b *TransitGatewayRouteTable) Update(ctx context.Context, id string, prev, next map[string]any) error {
	old, now := tagsOf(prev["Tags"]), tagsOf(next["Tags"])
	var gone []ec2types.Tag
	for k := range old {
		if _, ok := now[k]; !ok {
			gone = append(gone, ec2types.Tag{Key: aws.String(k)})
		}
	}
	if len(gone) > 0 {
		if _, err := b.EC2.DeleteTags(ctx, &ec2.DeleteTagsInput{Resources: []string{id}, Tags: gone}); err != nil {
			return err
		}
	}
	changed := map[string]string{}
	for k, v := range now {
		if old[k] != v {
			changed[k] = v
		}
	}
	if len(changed) > 0 {
		if _, err := b.EC2.CreateTags(ctx, &ec2.CreateTagsInput{Resources: []string{id}, Tags: ec2Tags(changed)}); err != nil {
			return err
		}
	}
	return nil
}

func (b *TransitGatewayRouteTable) Delete(ctx context.Context, id string) error {
	if _, err := b.Read(ctx, id); err != nil {
		return err
	}
	_, err := b.EC2.DeleteTransitGatewayRouteTable(ctx, &ec2.DeleteTransitGatewayRouteTableInput{TransitGatewayRouteTableId: aws.String(id)})
	return err
}

// ---- AWS::GuardDuty::Detector on moto's GuardDuty ----

// Detector is the GuardDuty detector backend. moto 5.2.5 implements
// Create/Get/Update/Delete/ListDetectors, with tags at create only (no
// TagResource), and returns data sources in the API's shape, mapped here to
// CloudFormation's.
type Detector struct{ GD *guardduty.Client }

// NewDetector makes the backend.
func NewDetector(cfg aws.Config) *Detector { return &Detector{GD: guardduty.NewFromConfig(cfg)} }

func boolAt(m map[string]any, path ...string) (bool, bool) {
	var cur any = m
	for _, p := range path {
		cm, ok := cur.(map[string]any)
		if !ok {
			return false, false
		}
		cur = cm[p]
	}
	b, ok := cur.(bool)
	return b, ok
}

func dataSources(m map[string]any) *gdtypes.DataSourceConfigurations {
	ds, ok := m["DataSources"].(map[string]any)
	if !ok {
		return nil
	}
	out := &gdtypes.DataSourceConfigurations{}
	if v, ok := boolAt(ds, "S3Logs", "Enable"); ok {
		out.S3Logs = &gdtypes.S3LogsConfiguration{Enable: aws.Bool(v)}
	}
	if v, ok := boolAt(ds, "Kubernetes", "AuditLogs", "Enable"); ok {
		out.Kubernetes = &gdtypes.KubernetesConfiguration{AuditLogs: &gdtypes.KubernetesAuditLogsConfiguration{Enable: aws.Bool(v)}}
	}
	if v, ok := boolAt(ds, "MalwareProtection", "ScanEc2InstanceWithFindings", "EbsVolumes"); ok {
		out.MalwareProtection = &gdtypes.MalwareProtectionConfiguration{ScanEc2InstanceWithFindings: &gdtypes.ScanEc2InstanceWithFindings{EbsVolumes: aws.Bool(v)}}
	}
	return out
}

func features(m map[string]any) []gdtypes.DetectorFeatureConfiguration {
	l, ok := m["Features"].([]any)
	if !ok {
		return nil
	}
	var out []gdtypes.DetectorFeatureConfiguration
	for _, f := range l {
		fm, _ := f.(map[string]any)
		c := gdtypes.DetectorFeatureConfiguration{Name: gdtypes.DetectorFeature(str(fm, "Name")), Status: gdtypes.FeatureStatus(str(fm, "Status"))}
		if ac, ok := fm["AdditionalConfiguration"].([]any); ok {
			for _, a := range ac {
				am, _ := a.(map[string]any)
				c.AdditionalConfiguration = append(c.AdditionalConfiguration, gdtypes.DetectorAdditionalConfiguration{
					Name: gdtypes.FeatureAdditionalConfiguration(str(am, "Name")), Status: gdtypes.FeatureStatus(str(am, "Status")),
				})
			}
		}
		out = append(out, c)
	}
	return out
}

func (b *Detector) Create(ctx context.Context, m map[string]any) (string, error) {
	enable, _ := m["Enable"].(bool)
	in := &guardduty.CreateDetectorInput{Enable: aws.Bool(enable), DataSources: dataSources(m), Features: features(m)}
	if f := str(m, "FindingPublishingFrequency"); f != "" {
		in.FindingPublishingFrequency = gdtypes.FindingPublishingFrequency(f)
	}
	if tags := tagsOf(m["Tags"]); len(tags) > 0 {
		in.Tags = tags
	}
	out, err := b.GD.CreateDetector(ctx, in)
	if err != nil {
		return "", InvalidRequest("%v", err)
	}
	return aws.ToString(out.DetectorId), nil
}

func (b *Detector) Read(ctx context.Context, id string) (map[string]any, error) {
	out, err := b.GD.GetDetector(ctx, &guardduty.GetDetectorInput{DetectorId: aws.String(id)})
	if err != nil {
		if c := code(err); strings.Contains(c, "NotFound") || c == "BadRequestException" || c == "InvalidInputException" {
			return nil, NotFound("%s", id)
		}
		return nil, err
	}
	m := map[string]any{
		"Id":                         id,
		"Enable":                     out.Status == gdtypes.DetectorStatusEnabled,
		"FindingPublishingFrequency": string(out.FindingPublishingFrequency),
	}
	if ds := out.DataSources; ds != nil {
		d := map[string]any{}
		if ds.S3Logs != nil {
			d["S3Logs"] = map[string]any{"Enable": ds.S3Logs.Status == gdtypes.DataSourceStatusEnabled}
		}
		if ds.Kubernetes != nil && ds.Kubernetes.AuditLogs != nil {
			d["Kubernetes"] = map[string]any{"AuditLogs": map[string]any{"Enable": ds.Kubernetes.AuditLogs.Status == gdtypes.DataSourceStatusEnabled}}
		}
		m["DataSources"] = d
	}
	if len(out.Features) > 0 {
		var fs []any
		for _, f := range out.Features {
			fm := map[string]any{"Name": string(f.Name), "Status": string(f.Status)}
			if len(f.AdditionalConfiguration) > 0 {
				var ac []any
				for _, a := range f.AdditionalConfiguration {
					ac = append(ac, map[string]any{"Name": string(a.Name), "Status": string(a.Status)})
				}
				fm["AdditionalConfiguration"] = ac
			}
			fs = append(fs, fm)
		}
		m["Features"] = fs
	}
	if len(out.Tags) > 0 {
		m["Tags"] = tagList(out.Tags)
	}
	return m, nil
}

func (b *Detector) Update(ctx context.Context, id string, prev, next map[string]any) error {
	if fmt.Sprint(tagList(tagsOf(prev["Tags"]))) != fmt.Sprint(tagList(tagsOf(next["Tags"]))) {
		return &HandlerError{"GeneralServiceException", "the fake cannot change a detector's tags: moto 5.2.5 has no GuardDuty TagResource"}
	}
	enable, _ := next["Enable"].(bool)
	in := &guardduty.UpdateDetectorInput{DetectorId: aws.String(id), Enable: aws.Bool(enable), DataSources: dataSources(next), Features: features(next)}
	if f := str(next, "FindingPublishingFrequency"); f != "" {
		in.FindingPublishingFrequency = gdtypes.FindingPublishingFrequency(f)
	}
	_, err := b.GD.UpdateDetector(ctx, in)
	return err
}

func (b *Detector) Delete(ctx context.Context, id string) error {
	if _, err := b.Read(ctx, id); err != nil {
		return err
	}
	_, err := b.GD.DeleteDetector(ctx, &guardduty.DeleteDetectorInput{DetectorId: aws.String(id)})
	return err
}
