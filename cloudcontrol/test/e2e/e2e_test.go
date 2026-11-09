// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/guardduty"
	gdtypes "github.com/aws/aws-sdk-go-v2/service/guardduty/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/test/fake"
)

// metadata is a SAML metadata document of at least 1000 characters (the
// schema's minLength); version tells two apart.
func metadata(version int) string {
	pad := strings.Repeat("A", 1000)
	return fmt.Sprintf(`<?xml version="1.0"?><EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp.example.com/v%d"><IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol"><KeyDescriptor use="signing"><KeyInfo xmlns="http://www.w3.org/2000/09/xmldsig#"><X509Data><X509Certificate>%s</X509Certificate></X509Data></KeyInfo></KeyDescriptor></IDPSSODescriptor></EntityDescriptor>`, version, pad)
}

func ops(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("patch %q: %v", body, err)
	}
	return out
}

func patchPaths(t *testing.T, calls []fake.Call) []string {
	var out []string
	for _, c := range calls {
		for _, o := range ops(t, c.Body) {
			out = append(out, fmt.Sprint(o["op"], " ", o["path"]))
		}
	}
	return out
}

// TestSAMLProvider: create with a write-only private key from a Secret;
// the asynchronous request is followed to the end; the ARN is the
// identifier; an update sends one replace and nothing read-only or
// create-only; the API server refuses a create-only change; delete.
func TestSAMLProvider(t *testing.T) {
	e := start(t)
	const typ = "AWS::IAM::SAMLProvider"
	const key = "-----BEGIN PRIVATE KEY-----\nFAKEFAKEFAKE\n-----END PRIVATE KEY-----"
	if err := e.c.Create(e.ctx, secret("saml-key", map[string]string{"private.pem": key})); err != nil {
		t.Fatal(err)
	}
	u := obj("SAMLProvider", "corp-idp", map[string]any{
		"name":                 "corp-idp",
		"samlMetadataDocument": metadata(1),
		"addPrivateKey":        map[string]any{"name": "saml-key", "key": "private.pem"},
		"tags":                 []any{map[string]any{"key": "team", "value": "platform"}},
	})
	if err := e.c.Create(e.ctx, u); err != nil {
		t.Fatal(err)
	}
	got := e.waitSynced(t, u)
	arn := "arn:aws:iam::123456789012:saml-provider/corp-idp"
	if s, _, _ := unstructured.NestedString(got.Object, "status", "arn"); s != arn {
		t.Errorf("status.arn = %q, want %q", s, arn)
	}
	if s, _, _ := unstructured.NestedString(got.Object, "status", "cloudControl", "identifier"); s != arn {
		t.Errorf("status.cloudControl.identifier = %q, want %q", s, arn)
	}
	if s, _, _ := unstructured.NestedString(got.Object, "status", "ackResourceMetadata", "arn"); s != arn {
		t.Errorf("status.ackResourceMetadata.arn = %q", s)
	}
	if s, _, _ := unstructured.NestedString(got.Object, "status", "cloudControl", "writeOnlyDigest"); !strings.HasPrefix(s, "sha256:") {
		t.Errorf("status.cloudControl.writeOnlyDigest = %q", s)
	}

	// In moto, through IAM's own API.
	ic := iam.NewFromConfig(e.moto)
	p, err := ic.GetSAMLProvider(e.ctx, &iam.GetSAMLProviderInput{SAMLProviderArn: aws.String(arn)})
	if err != nil || aws.ToString(p.SAMLMetadataDocument) != metadata(1) {
		t.Fatalf("moto: %v, metadata matches: %v", err, err == nil && aws.ToString(p.SAMLMetadataDocument) == metadata(1))
	}
	// The Secret's value reached Cloud Control, and Cloud Control never
	// gives it back.
	if v := e.cc.WriteOnly(typ, arn)["AddPrivateKey"]; v != key {
		t.Errorf("write-only AddPrivateKey sent = %q, want the Secret's value", v)
	}
	ccCfg, _ := fake.AWSConfig(e.ccURL, region)
	gr, err := cloudcontrol.NewFromConfig(ccCfg).GetResource(e.ctx, &cloudcontrol.GetResourceInput{TypeName: aws.String(typ), Identifier: aws.String(arn)})
	if err != nil || strings.Contains(aws.ToString(gr.ResourceDescription.Properties), "AddPrivateKey") {
		t.Errorf("GetResource: %v; properties %s", err, aws.ToString(gr.ResourceDescription.Properties))
	}
	if s, _, _ := unstructured.NestedString(got.Object, "spec", "addPrivateKey", "name"); s != "saml-key" {
		t.Errorf("spec.addPrivateKey lost: %v", got.Object["spec"])
	}
	// The create was asynchronous: followed to the end.
	var polls int
	for _, c := range e.calls("GetResourceRequestStatus", "") {
		if strings.HasSuffix(c.Identifier, "-create") {
			polls++
		}
	}
	if polls < 3 {
		t.Errorf("GetResourceRequestStatus for the create: %d calls, want at least 3 (two IN_PROGRESS, then SUCCESS)", polls)
	}

	// Update the metadata: one replace, of that property only.
	if err := e.update(t, u, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, metadata(2), "spec", "samlMetadataDocument")
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "new metadata in moto", func() error {
		p, err := ic.GetSAMLProvider(e.ctx, &iam.GetSAMLProviderInput{SAMLProviderArn: aws.String(arn)})
		if err != nil {
			return err
		}
		if aws.ToString(p.SAMLMetadataDocument) != metadata(2) {
			return fmt.Errorf("old metadata")
		}
		return nil
	})
	e.waitSynced(t, u)
	paths := patchPaths(t, e.calls("UpdateResource", typ))
	if strings.Join(paths, ",") != "replace /SamlMetadataDocument" {
		t.Errorf("patches sent: %v, want only a replace of /SamlMetadataDocument", paths)
	}

	// A create-only change is refused by the API server (CEL), not AWS.
	err = e.update(t, u, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, "other-idp", "spec", "name")
	})
	if err == nil || !strings.Contains(err.Error(), "create-only") {
		t.Errorf("changing spec.name: %v, want a create-only refusal", err)
	}
	err = e.update(t, u, func(o *unstructured.Unstructured) {
		unstructured.RemoveNestedField(o.Object, "spec", "addPrivateKey")
	})
	if err == nil || !strings.Contains(err.Error(), "create-only") {
		t.Errorf("removing spec.addPrivateKey: %v, want a create-only refusal", err)
	}

	if err := e.c.Delete(e.ctx, u); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, u)
	if _, err := ic.GetSAMLProvider(e.ctx, &iam.GetSAMLProviderInput{SAMLProviderArn: aws.String(arn)}); err == nil {
		t.Errorf("the SAML provider is still in moto after delete")
	}
}

func rtTags(t *testing.T, e *env, id string) map[string]string {
	t.Helper()
	out, err := ec2.NewFromConfig(e.moto).DescribeTransitGatewayRouteTables(e.ctx, &ec2.DescribeTransitGatewayRouteTablesInput{TransitGatewayRouteTableIds: []string{id}})
	if err != nil || len(out.TransitGatewayRouteTables) != 1 {
		t.Fatalf("describe %s: %v", id, err)
	}
	m := map[string]string{}
	for _, tg := range out.TransitGatewayRouteTables[0].Tags {
		m[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
	}
	m["@state"] = string(out.TransitGatewayRouteTables[0].State)
	return m
}

// TestTransitGatewayRouteTable: create with tags (ACK's default tags added,
// not written back to spec); a tag update; a reorder alone is no update;
// a create-only change refused; deletion-policy retain; adoption by the
// primary identifier fills spec from AWS; delete; a handler failure's
// error code and message reach ACK.Terminal.
func TestTransitGatewayRouteTable(t *testing.T) {
	e := start(t)
	const typ = "AWS::EC2::TransitGatewayRouteTable"
	ec := ec2.NewFromConfig(e.moto)
	tgw, err := ec.CreateTransitGateway(e.ctx, &ec2.CreateTransitGatewayInput{Description: aws.String("hub")})
	if err != nil {
		t.Fatal(err)
	}
	tgwID := aws.ToString(tgw.TransitGateway.TransitGatewayId)

	u := obj("TransitGatewayRouteTable", "hub-spokes", map[string]any{
		"transitGatewayId": tgwID,
		"tags":             []any{map[string]any{"key": "env", "value": "dev"}},
	})
	if err := e.c.Create(e.ctx, u); err != nil {
		t.Fatal(err)
	}
	got := e.waitSynced(t, u)
	id, _, _ := unstructured.NestedString(got.Object, "status", "transitGatewayRouteTableId")
	if !strings.HasPrefix(id, "tgw-rtb-") {
		t.Fatalf("status.transitGatewayRouteTableId = %q", id)
	}
	tags := rtTags(t, e, id)
	if tags["env"] != "dev" || tags["services.k8s.aws/namespace"] != "default" {
		t.Errorf("tags in moto: %v; want env=dev and ACK's services.k8s.aws/namespace=default", tags)
	}
	if l, _, _ := unstructured.NestedSlice(got.Object, "spec", "tags"); len(l) != 1 {
		t.Errorf("spec.tags = %v: ACK's default tags must not be written into spec", l)
	}

	// Change and add a tag.
	if err := e.update(t, u, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedSlice(o.Object, []any{
			map[string]any{"key": "env", "value": "prod"},
			map[string]any{"key": "owner", "value": "network"},
		}, "spec", "tags")
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "tags updated in moto", func() error {
		tags := rtTags(t, e, id)
		if tags["env"] != "prod" || tags["owner"] != "network" {
			return fmt.Errorf("tags %v", tags)
		}
		return nil
	})
	e.waitSynced(t, u)
	for _, p := range patchPaths(t, e.calls("UpdateResource", typ)) {
		if p != "replace /Tags" {
			t.Errorf("patch %q: only /Tags may be patched (the id is read-only, the gateway create-only)", p)
		}
	}

	// Reordering the tags (insertionOrder: false) is no change: no update
	// across three resyncs.
	before := len(e.calls("UpdateResource", typ))
	if err := e.update(t, u, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedSlice(o.Object, []any{
			map[string]any{"key": "owner", "value": "network"},
			map[string]any{"key": "env", "value": "prod"},
		}, "spec", "tags")
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(16 * time.Second)
	if n := len(e.calls("UpdateResource", typ)); n != before {
		t.Errorf("a reorder of an unordered list caused %d update(s)", n-before)
	}

	// The gateway is create-only.
	err = e.update(t, u, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, "tgw-0123456789abcdef0", "spec", "transitGatewayId")
	})
	if err == nil || !strings.Contains(err.Error(), "create-only") {
		t.Errorf("changing transitGatewayId: %v, want a create-only refusal", err)
	}

	// deletion-policy retain leaves the table.
	if err := e.update(t, u, func(o *unstructured.Unstructured) {
		a := o.GetAnnotations()
		if a == nil {
			a = map[string]string{}
		}
		a["services.k8s.aws/deletion-policy"] = "retain"
		o.SetAnnotations(a)
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if err := e.c.Delete(e.ctx, u); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, u)
	if s := rtTags(t, e, id)["@state"]; s == "deleted" {
		t.Fatalf("retain: the table was deleted")
	}

	// Adopt it by its primary identifier: spec is filled from AWS.
	a := obj("TransitGatewayRouteTable", "hub-spokes-adopted", map[string]any{"transitGatewayId": tgwID})
	a.SetAnnotations(map[string]string{
		"services.k8s.aws/adoption-policy": "adopt",
		"services.k8s.aws/adoption-fields": fmt.Sprintf(`{"transitGatewayRouteTableId": %q}`, id),
	})
	if err := e.c.Create(e.ctx, a); err != nil {
		t.Fatal(err)
	}
	got = e.waitSynced(t, a)
	if got.GetAnnotations()[ackv1alpha1.AnnotationAdopted] != "true" {
		t.Errorf("not marked adopted: %v", got.GetAnnotations())
	}
	if s, _, _ := unstructured.NestedString(got.Object, "status", "cloudControl", "identifier"); s != id {
		t.Errorf("adopted identifier %q, want %q", s, id)
	}
	l, _, _ := unstructured.NestedSlice(got.Object, "spec", "tags")
	var kv []string
	for _, x := range l {
		m := x.(map[string]any)
		kv = append(kv, fmt.Sprint(m["key"], "=", m["value"]))
	}
	sort.Strings(kv)
	if strings.Join(kv, ",") != "env=prod,owner=network" {
		t.Errorf("adopted spec.tags = %v, want AWS's tags without ACK's", kv)
	}
	if err := e.c.Delete(e.ctx, a); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, a)
	if s := rtTags(t, e, id)["@state"]; s != "deleted" {
		t.Errorf("after deleting the adopted object the table is %q", s)
	}

	// A handler failure: its code and message reach the object.
	bad := obj("TransitGatewayRouteTable", "no-such-gateway", map[string]any{"transitGatewayId": "tgw-00000000000000000"})
	if err := e.c.Create(e.ctx, bad); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "terminal condition", func() error {
		s, msg := condition(e.get(t, bad), ackv1alpha1.ConditionTypeTerminal)
		if s != "True" || !strings.Contains(msg, "InvalidRequest") {
			return fmt.Errorf("terminal=%q %q", s, msg)
		}
		return nil
	})
	got = e.get(t, bad)
	if c, _, _ := unstructured.NestedString(got.Object, "status", "cloudControl", "request", "errorCode"); c != "InvalidRequest" {
		t.Errorf("status.cloudControl.request.errorCode = %q", c)
	}
	_, msg := condition(got, ackv1alpha1.ConditionTypeTerminal)
	t.Logf("handler failure as the user sees it: %s", msg)
}

// TestDetector: nested objects and unordered arrays of objects; drift made
// in GuardDuty's own API is put back on the resync with one replace; a
// reorder of features is no update; delete.
func TestDetector(t *testing.T) {
	e := start(t)
	const typ = "AWS::GuardDuty::Detector"
	u := obj("Detector", "account-detector", map[string]any{
		"enable":                     true,
		"findingPublishingFrequency": "SIX_HOURS",
		"dataSources":                map[string]any{"s3Logs": map[string]any{"enable": true}},
		"features": []any{
			map[string]any{"name": "EBS_MALWARE_PROTECTION", "status": "ENABLED"},
			map[string]any{"name": "RDS_LOGIN_EVENTS", "status": "DISABLED"},
		},
	})
	if err := e.c.Create(e.ctx, u); err != nil {
		t.Fatal(err)
	}
	got := e.waitSynced(t, u)
	id, _, _ := unstructured.NestedString(got.Object, "status", "id")
	if id == "" {
		t.Fatalf("no status.id: %v", got.Object["status"])
	}
	gd := guardduty.NewFromConfig(e.moto)
	d, err := gd.GetDetector(e.ctx, &guardduty.GetDetectorInput{DetectorId: aws.String(id)})
	if err != nil || d.FindingPublishingFrequency != gdtypes.FindingPublishingFrequencySixHours || d.DataSources.S3Logs.Status != gdtypes.DataSourceStatusEnabled {
		t.Fatalf("moto: %v %+v", err, d)
	}

	// Drift: change it in GuardDuty directly; the resync puts it back.
	before := len(e.calls("UpdateResource", typ))
	if _, err := gd.UpdateDetector(e.ctx, &guardduty.UpdateDetectorInput{
		DetectorId: aws.String(id), Enable: aws.Bool(true), FindingPublishingFrequency: gdtypes.FindingPublishingFrequencyOneHour,
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "drift corrected in moto", func() error {
		d, err := gd.GetDetector(e.ctx, &guardduty.GetDetectorInput{DetectorId: aws.String(id)})
		if err != nil {
			return err
		}
		if d.FindingPublishingFrequency != gdtypes.FindingPublishingFrequencySixHours {
			return fmt.Errorf("frequency %s", d.FindingPublishingFrequency)
		}
		return nil
	})
	paths := patchPaths(t, e.calls("UpdateResource", typ)[before:])
	if strings.Join(paths, ",") != "replace /FindingPublishingFrequency" {
		t.Errorf("drift patches: %v, want one replace of /FindingPublishingFrequency", paths)
	}
	e.waitSynced(t, u)

	// Reordering features (insertionOrder: false) is no change.
	before = len(e.calls("UpdateResource", typ))
	if err := e.update(t, u, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedSlice(o.Object, []any{
			map[string]any{"name": "RDS_LOGIN_EVENTS", "status": "DISABLED"},
			map[string]any{"name": "EBS_MALWARE_PROTECTION", "status": "ENABLED"},
		}, "spec", "features")
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(16 * time.Second)
	if n := len(e.calls("UpdateResource", typ)); n != before {
		t.Errorf("a reorder of features caused %d update(s): %v", n-before, patchPaths(t, e.calls("UpdateResource", typ)[before:]))
	}

	// A real change to a nested object: one replace, merged over AWS's
	// value (moto's kubernetes data source stays).
	if err := e.update(t, u, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, false, "spec", "dataSources", "s3Logs", "enable")
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "S3 logs disabled in moto", func() error {
		d, err := gd.GetDetector(e.ctx, &guardduty.GetDetectorInput{DetectorId: aws.String(id)})
		if err != nil {
			return err
		}
		if d.DataSources.S3Logs.Status != gdtypes.DataSourceStatusDisabled {
			return fmt.Errorf("s3 logs %s", d.DataSources.S3Logs.Status)
		}
		return nil
	})
	e.waitSynced(t, u)

	if err := e.c.Delete(e.ctx, u); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, u)
	if _, err := gd.GetDetector(e.ctx, &guardduty.GetDetectorInput{DetectorId: aws.String(id)}); err == nil {
		t.Errorf("the detector is still in moto after delete")
	}
}
