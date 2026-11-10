// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53resolver"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// F08 and F09, DNS: a hosted zone's query logging to a log group, and a
// VPC's DNSSEC validation, each made, left alone through resyncs, refused
// a create-only change, and deleted.
func TestDNS(t *testing.T) {
	e := start(t)
	r53 := route53.NewFromConfig(e.moto)
	// Route 53 takes query logs only into a log group in us-east-1, as
	// moto checks too, whatever the environment's region.
	logsCfg := e.moto.Copy()
	logsCfg.Region = "us-east-1"
	logs := cloudwatchlogs.NewFromConfig(logsCfg)
	rr := route53resolver.NewFromConfig(e.moto)

	// Fixtures: a public hosted zone and a log group.
	hz, err := r53.CreateHostedZone(e.ctx, &route53.CreateHostedZoneInput{Name: aws.String("dns-e2e.example.com."), CallerReference: aws.String("dns-e2e")})
	if err != nil {
		t.Fatal(err)
	}
	zone := strings.TrimPrefix(aws.ToString(hz.HostedZone.Id), "/hostedzone/")
	const group = "/aws/route53/dns-e2e.example.com"
	if _, err := logs.CreateLogGroup(e.ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)}); err != nil {
		t.Fatal(err)
	}
	lg, err := logs.DescribeLogGroups(e.ctx, &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: aws.String(group)})
	if err != nil || len(lg.LogGroups) != 1 {
		t.Fatalf("log group: %v %+v", err, lg)
	}
	groupARN := aws.ToString(lg.LogGroups[0].Arn)

	const qlType = "AWS::Route53::QueryLoggingConfig"
	ql := obj("QueryLoggingConfig", "dns-e2e-queries", map[string]any{
		"hostedZoneId": zone, "cloudWatchLogsLogGroupArn": groupARN,
	})
	if err := e.c.Create(e.ctx, ql); err != nil {
		t.Fatal(err)
	}
	got := e.waitSynced(t, ql)
	arn, _, _ := unstructured.NestedString(got.Object, "status", "arn")
	qid, _, _ := unstructured.NestedString(got.Object, "status", "id")
	id, _, _ := unstructured.NestedString(got.Object, "status", "cloudControl", "identifier")
	if qid == "" || arn != "arn:aws:route53:::queryloggingconfig/"+qid || id != arn {
		t.Fatalf("status.arn %q, status.id %q, identifier %q", arn, qid, id)
	}
	q, err := r53.GetQueryLoggingConfig(e.ctx, &route53.GetQueryLoggingConfigInput{Id: aws.String(qid)})
	if err != nil || aws.ToString(q.QueryLoggingConfig.HostedZoneId) != zone || aws.ToString(q.QueryLoggingConfig.CloudWatchLogsLogGroupArn) != groupARN {
		t.Fatalf("moto: %v %+v", err, q)
	}

	// A VPC's DNSSEC validation.
	ec := ec2.NewFromConfig(e.moto)
	vpc, err := ec.CreateVpc(e.ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.53.0.0/16")})
	if err != nil {
		t.Fatal(err)
	}
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	// The fixtures go at the end, after the objects (a live run leaves
	// nothing behind).
	t.Cleanup(func() {
		_, _ = ec.DeleteVpc(e.ctx, &ec2.DeleteVpcInput{VpcId: aws.String(vpcID)})
		_, _ = logs.DeleteLogGroup(e.ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})
		_, _ = r53.DeleteHostedZone(e.ctx, &route53.DeleteHostedZoneInput{Id: aws.String(zone)})
	})
	const dsType = "AWS::Route53Resolver::ResolverDNSSECConfig"
	ds := obj("ResolverDNSSECConfig", "dns-e2e-validation", map[string]any{"resourceId": vpcID})
	if err := e.c.Create(e.ctx, ds); err != nil {
		t.Fatal(err)
	}
	got = e.waitSynced(t, ds)
	did, _, _ := unstructured.NestedString(got.Object, "status", "id")
	vs, _, _ := unstructured.NestedString(got.Object, "status", "validationStatus")
	if !strings.HasPrefix(did, "rdsc-") || vs != "ENABLED" {
		t.Fatalf("status.id %q, status.validationStatus %q", did, vs)
	}
	validation := func() string {
		out, err := rr.GetResolverDnssecConfig(e.ctx, &route53resolver.GetResolverDnssecConfigInput{ResourceId: aws.String(vpcID)})
		if err != nil {
			t.Fatal(err)
		}
		return string(out.ResolverDNSSECConfig.ValidationStatus)
	}
	if v := validation(); v != "ENABLED" {
		t.Errorf("validation in moto %q, want ENABLED", v)
	}

	// Resyncs of both make no update.
	time.Sleep(12 * time.Second)
	for _, ty := range []string{qlType, dsType} {
		if n := len(e.calls("UpdateResource", ty)); n != 0 {
			t.Errorf("%d update(s) of an unchanged %s", n, ty)
		}
	}

	// Every property is create-only: the API server refuses a change.
	err = e.update(t, ql, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, "Z0000000000000000000", "spec", "hostedZoneId")
	})
	if err == nil || !strings.Contains(err.Error(), "create-only") {
		t.Errorf("a new hosted zone was not refused as create-only: %v", err)
	}
	err = e.update(t, ds, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, "vpc-00000000000000000", "spec", "resourceId")
	})
	if err == nil || !strings.Contains(err.Error(), "create-only") {
		t.Errorf("a new VPC was not refused as create-only: %v", err)
	}

	// Deleted: the query logging configuration is gone; validation is off.
	for _, u := range []*unstructured.Unstructured{ql, ds} {
		if err := e.c.Delete(e.ctx, u); err != nil {
			t.Fatal(err)
		}
		e.waitGone(t, u)
	}
	if _, err := r53.GetQueryLoggingConfig(e.ctx, &route53.GetQueryLoggingConfigInput{Id: aws.String(qid)}); err == nil {
		t.Error("the query logging configuration is still in moto after delete")
	}
	if v := validation(); v != "DISABLED" {
		t.Errorf("validation in moto after delete %q, want DISABLED", v)
	}
	for _, ty := range []string{qlType, dsType} {
		if n := len(e.calls("DeleteResource", ty)); n != 1 {
			t.Errorf("%d DeleteResource calls for %s, want 1", n, ty)
		}
	}
}
