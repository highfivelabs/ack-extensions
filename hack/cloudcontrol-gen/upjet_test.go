// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/cfn"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
)

func mustSchema(t *testing.T, js string) *cfn.Schema {
	t.Helper()
	s, err := cfn.Parse([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func entry(svc, res, tf string, official, ours []string, targets map[string][]string, refs ...*upjetRef) *upjetEntry {
	e := &upjetEntry{TargetsOurs: targets}
	e.Ours.Service, e.Ours.Resource, e.Ours.OfficialKinds, e.Ours.OursKinds = svc, res, official, ours
	e.Facts.Terraform, e.Facts.References = tf, refs
	return e
}

// Each way a Upjet reference becomes, or does not become, a reference
// field here, on a made-up type.
func TestDeriveReferences(t *testing.T) {
	s := mustSchema(t, `{"typeName":"AWS::Demo::Thing","properties":{
		"VpcId":{"type":"string"},"KeyArn":{"type":"string"},"BucketName":{"type":"string"},
		"RoleArn":{"type":"string"},"SubnetIds":{"type":"array","items":{"type":"string"}},
		"DetectorId":{"type":"string"},"Id":{"type":"string"},"LogGroupName":{"type":"string"}},
		"readOnlyProperties":["/properties/Id"],"primaryIdentifier":["/properties/Id"]}`)
	det := mustSchema(t, `{"typeName":"AWS::Demo::Detector","properties":{"Id":{"type":"string"},"Name":{"type":"string"}},
		"readOnlyProperties":["/properties/Id"],"primaryIdentifier":["/properties/Id"]}`)
	served := map[string][]string{"aws_vpc": {"VPC"}, "aws_kms_key": {"Key"}, "aws_s3_bucket": {"Bucket"}, "aws_iam_role": {"Role"}, "aws_subnet": {"Subnet"}, "aws_demo_detector": {"Detector"}, "aws_cloudwatch_log_group": {"LogGroup"}}
	ix := &servedIndex{
		ack: map[string][]ackKind{
			"ec2/vpcs": {{"ec2.services.k8s.aws", "vpcs"}}, "kms/keys": {{"kms.services.k8s.aws", "keys"}},
			"s3/buckets": {{"s3.services.k8s.aws", "buckets"}}, "iam/roles": {{"iam.services.k8s.aws", "roles"}},
			"logs/loggroups": {{"cloudwatchlogs.services.k8s.aws", "loggroups"}},
		},
		cc:      map[string]*ccKind{"demo/Detector": {group: "demo.cloudcontrol.aws.hfl.dev", kind: "Detector", schema: det}},
		idPaths: map[string]refTarget{"ec2.services.k8s.aws/VPC": {ID: "status.vpcID"}},
		byTF: map[string][]*upjetEntry{
			"aws_vpc":                  {entry("ec2", "VPC", "aws_vpc", []string{"VPC"}, nil, nil)},
			"aws_kms_key":              {entry("kms", "Key", "aws_kms_key", []string{"Key"}, nil, nil)},
			"aws_s3_bucket":            {entry("s3", "Bucket", "aws_s3_bucket", []string{"Bucket"}, nil, nil)},
			"aws_iam_role":             {entry("iam", "Role", "aws_iam_role", []string{"Role"}, nil, nil)},
			"aws_demo_detector":        {entry("demo", "Detector", "aws_demo_detector", nil, []string{"Detector (Cloud Control)"}, nil)},
			"aws_cloudwatch_log_group": {entry("logs", "LogGroup", "aws_cloudwatch_log_group", []string{"LogGroup"}, nil, nil)},
		},
	}
	arn := "github.com/upbound/provider-aws/v2/config/cluster/common.ARNExtractor()"
	e := entry("demo", "Thing", "aws_demo_thing", nil, []string{"Thing (Cloud Control)"}, served,
		&upjetRef{Field: "vpc_id", Target: "aws_vpc"},
		&upjetRef{Field: "key_arn", Target: "aws_kms_key", Extractor: arn},
		&upjetRef{Field: "bucket_name", Target: "aws_s3_bucket"},
		&upjetRef{Field: "role_arn", Target: "aws_iam_role"},
		&upjetRef{Field: "subnet_ids", Target: "aws_subnet"},
		&upjetRef{Field: "detector_id", Target: "aws_demo_detector"},
		&upjetRef{Field: "log_group_name", Target: "aws_cloudwatch_log_group", Extractor: arn},
		&upjetRef{Field: "config[*].vpc_id", Target: "aws_vpc"},
		&upjetRef{Field: "id", Target: "aws_vpc"},
		&upjetRef{Field: "vpc_id", Target: "aws_nothing"},
	)
	got := map[string]*derived{}
	for _, d := range ix.deriveReferences(s, []*upjetEntry{e}) {
		got[d.field+">"+d.target] = d
	}
	for key, want := range map[string]string{
		"vpc_id>aws_vpc":                          "applied ec2.services.k8s.aws VPC status.vpcID",
		"key_arn>aws_kms_key":                     "applied kms.services.k8s.aws Key status.ackResourceMetadata.arn",
		"bucket_name>aws_s3_bucket":               "skipped its ID path is not in referenceTargets",
		"role_arn>aws_iam_role":                   "skipped the property takes an ARN",
		"subnet_ids>aws_subnet":                   "skipped not a string property",
		"detector_id>aws_demo_detector":           "applied demo.cloudcontrol.aws.hfl.dev Detector status.id",
		"log_group_name>aws_cloudwatch_log_group": "skipped the property takes a name",
		"config[*].vpc_id>aws_vpc":                "skipped nested field",
		"id>aws_vpc":                              "skipped read-only",
		"vpc_id>aws_nothing":                      "skipped not served here",
	} {
		d := got[key]
		if d == nil {
			t.Errorf("%s: not derived", key)
			continue
		}
		have := d.outcome + " " + d.ref.Group + " " + d.ref.Kind + " " + d.ref.Path + " " + d.reason
		for _, w := range strings.Fields(want) {
			if !strings.Contains(have, w) {
				t.Errorf("%s: %q lacks %q", key, have, w)
			}
		}
	}

	// The overlay's reference to the same property wins; a different
	// target is a disagreement, listed and not applied.
	ds := ix.deriveReferences(s, []*upjetEntry{e})
	hand := []cfn.Reference{{Property: "/properties/VpcId", Group: "ec2.services.k8s.aws", Kind: "VPC", Path: "status.id"}}
	merged := mergeReferences(s, hand, ds)
	refs, err := s.References(merged)
	if err != nil {
		t.Fatal(err)
	}
	var props []string
	for _, r := range refs {
		props = append(props, r.Property+"="+r.Path)
	}
	if want := "/properties/DetectorId=status.id /properties/KeyArn=status.ackResourceMetadata.arn /properties/VpcId=status.id"; strings.Join(props, " ") != want {
		t.Errorf("merged %v, want %s", props, want)
	}
	for _, d := range ds {
		if d.field == "vpc_id" && d.target == "aws_vpc" && d.outcome != "disagrees" {
			t.Errorf("vpc_id: %s %s, want disagrees", d.outcome, d.reason)
		}
	}
}

// Every reference of the overlay that Upjet also gives agrees with it
// (none is overridden), the slice's provenance points at the reference,
// and the generated registry carries the references applied.
func TestUpjetReferencesOnTheRealInputs(t *testing.T) {
	root := filepath.Join("..", "..")
	cfg, files, err := readConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	up, ix, err := upjetIndex(root, files, cfg)
	if err != nil || up == nil {
		t.Fatalf("upjet: %v %v", up, err)
	}
	raw, err := os.ReadFile(filepath.Join(root, upjetSlice))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	applied := 0
	for _, ty := range cfg.Types {
		s, err := cfn.Parse(files[schemaFile(ty.TypeName)])
		if err != nil {
			t.Fatal(err)
		}
		ut := ix.forType(up, ty, s)
		refs, err := s.References(ut.merged(s, ty.References))
		if err != nil {
			t.Fatalf("%s: %v", ty.TypeName, err)
		}
		hand := map[string]cfn.Reference{}
		for _, h := range ty.References {
			hand[h.Property] = h
		}
		for _, r := range refs {
			if h, ok := hand[r.Property]; ok && (h.Kind != r.Kind || h.Path != r.Path) {
				t.Errorf("%s %s: the overlay's reference was overridden", ty.TypeName, r.Property)
			}
		}
		reg := registry.ByTypeName(ty.TypeName)
		for _, d := range ut.refs {
			if d.slice <= 0 || !strings.Contains(lines[d.slice-1], `"field": "`+d.field+`"`) {
				t.Errorf("%s %s: slice line %d is not the reference", ty.TypeName, d.field, d.slice)
			}
			if _, ok := hand[d.ref.Property]; ok && d.outcome == "disagrees" {
				t.Logf("%s %s: %s", ty.TypeName, d.field, d.reason)
			}
			if d.outcome != "applied" {
				continue
			}
			applied++
			found := false
			for _, r := range reg.References {
				found = found || (r.Property == d.ref.Property && r.Kind == d.ref.Kind && r.Path == d.ref.Path)
			}
			if !found {
				t.Errorf("%s %s: applied but not in the generated registry (run task cloudcontrol:generate)", ty.TypeName, d.ref.Property)
			}
		}
	}
	if applied == 0 {
		t.Error("no Upjet reference applied")
	}
}
