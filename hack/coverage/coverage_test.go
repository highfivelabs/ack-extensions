// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

const cfnFixture = `{
 "typeName": "AWS::Demo::Widget",
 "primaryIdentifier": ["/properties/WidgetName"],
 "required": ["WidgetName"],
 "readOnlyProperties": ["/properties/Arn", "/properties/Endpoint/Address", "/properties/Rules/*/Computed"],
 "definitions": {
  "Tag": {"type": "object", "properties": {"Key": {"type": "string"}, "Value": {"type": "string"}}, "required": ["Key"]},
  "Node": {"type": "object", "properties": {"Name": {"type": "string"}, "Child": {"$ref": "#/definitions/Node"}}}
 },
 "properties": {
  "WidgetName": {"type": "string"},
  "Arn": {"type": "string"},
  "Description": {"type": "string"},
  "Endpoint": {"type": "object", "properties": {"Address": {"type": "string"}, "Port": {"type": "integer"}}},
  "SecurityGroupIds": {"type": "array", "items": {"type": "string"}},
  "Labels": {"type": "object", "patternProperties": {"^.*$": {"type": "string"}}},
  "Doc": {"type": "object"},
  "Rules": {"type": "array", "items": {"type": "object", "properties": {"Computed": {"type": "string"}, "Action": {"type": "string"}}}},
  "Tree": {"$ref": "#/definitions/Node"},
  "Tags": {"type": "array", "items": {"$ref": "#/definitions/Tag"}}
 }
}`

func attrPaths(as []attr) []string {
	var out []string
	for _, a := range as {
		out = append(out, a.Path)
	}
	return out
}

func TestParseCFN(t *testing.T) {
	r, err := parseCFN([]byte(cfnFixture))
	if err != nil {
		t.Fatal(err)
	}
	if r.Service != "demo" || r.Name != "Widget" {
		t.Fatalf("got %s %s", r.Service, r.Name)
	}
	want := []string{"Description", "Doc", "Endpoint.Port", "Labels{}", "Rules[].Action", "SecurityGroupIds[]", "Tags[].Key", "Tags[].Value", "Tree.Child", "Tree.Name", "WidgetName"}
	got := strings.Join(attrPaths(r.Attrs), " ")
	if got != strings.Join(want, " ") {
		t.Fatalf("attributes\n got %s\nwant %s", got, strings.Join(want, " "))
	}
	if r.ReadOnlyLeaves != 3 {
		t.Errorf("read-only leaves = %d, want 3 (Arn, Endpoint.Address, Rules[].Computed)", r.ReadOnlyLeaves)
	}
	if r.Cut != 1 {
		t.Errorf("recursion cuts = %d, want 1", r.Cut)
	}
	for _, a := range r.Attrs {
		switch a.Path {
		case "WidgetName":
			if !a.Required {
				t.Error("WidgetName should be required")
			}
		case "Tags[].Key", "Tags[].Value", "Description":
			if a.Required {
				t.Errorf("%s should not be required (Tags is optional)", a.Path)
			}
		case "Doc", "Tree.Child":
			if !a.Free {
				t.Errorf("%s should be free-form", a.Path)
			}
		}
	}
	if len(r.Primary) != 1 || r.Primary[0] != "WidgetName" {
		t.Errorf("primary = %v", r.Primary)
	}
}

const crdFixture = `
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
spec:
  group: demo.services.k8s.aws
  names: {kind: Widget}
  versions:
  - name: v1alpha1
    storage: true
    schema:
      openAPIV3Schema:
        properties:
          spec:
            properties:
              name: {type: string}
              description: {type: string}
              doc: {type: object, x-kubernetes-preserve-unknown-fields: true}
              endpoint:
                properties: {port: {type: integer}, bogus: {type: string}}
                type: object
              securityGroupIDs: {type: array, items: {type: string}}
              vpcRef:
                properties: {from: {properties: {name: {type: string}}, type: object}}
                type: object
              password:
                properties: {key: {type: string}, name: {type: string}, namespace: {type: string}}
                type: object
              labels: {type: object, additionalProperties: {type: string}}
              rules:
                items:
                  properties: {action: {type: string}}
                  type: object
                type: array
              tags: {type: object, additionalProperties: {type: string}}
            type: object
        type: object
`

func TestParseCRD(t *testing.T) {
	k, err := parseCRD([]byte(crdFixture))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(attrPaths(k.Attrs), " ")
	want := "description doc endpoint.bogus endpoint.port labels{} name password rules[].action securityGroupIDs[] tags{}"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if k.Refs != 1 {
		t.Errorf("refs = %d, want 1", k.Refs)
	}
}

func TestMatch(t *testing.T) {
	r, _ := parseCFN([]byte(cfnFixture))
	k, _ := parseCRD([]byte(crdFixture))
	match, tiers, _, unmapped := matchAttrs(r, k, nil)
	for cfn, wantCRD := range map[string]string{
		"description":        "description",
		"widgetname":         "name", // primary identifier
		"endpoint.port":      "endpoint.port",
		"securitygroupids[]": "securityGroupIDs[]", // ids/s variant
		"labels{}":           "labels{}",
		"rules[].action":     "rules[].action",
		"tags[].key":         "tags{}", // tag list against a map
		"tags[].value":       "tags{}",
		"doc":                "doc",
	} {
		if match[cfn] != wantCRD {
			t.Errorf("%s matched %q (%s), want %q", cfn, match[cfn], tiers[cfn], wantCRD)
		}
	}
	if _, ok := match["tree.name"]; ok {
		t.Error("tree.name has no counterpart")
	}
	if strings.Join(unmapped, " ") != "endpoint.bogus password" {
		t.Errorf("unmapped = %v", unmapped)
	}
}

func TestAlias(t *testing.T) {
	r, _ := parseCFN([]byte(cfnFixture))
	k, _ := parseCRD([]byte(crdFixture))
	match, tiers, _, _ := matchAttrs(r, k, map[string]string{"rules[]": "Rules[]", "endpoint.bogus": "Tree.Name"})
	if match["tree.name"] != "endpoint.bogus" || tiers["tree.name"] != "alias" {
		t.Errorf("alias not applied: %q %q", match["tree.name"], tiers["tree.name"])
	}
}

func TestFreeFormAncestor(t *testing.T) {
	cfn := `{"typeName":"AWS::Demo::W","properties":{"Doc":{"type":"object","properties":{"A":{"type":"string"},"B":{"type":"string"}}}}}`
	crd := strings.Replace(crdFixture, "doc: {type: object, x-kubernetes-preserve-unknown-fields: true}", "doc: {type: object, x-kubernetes-preserve-unknown-fields: true}", 1)
	r, _ := parseCFN([]byte(cfn))
	k, _ := parseCRD([]byte(crd))
	match, _, how, _ := matchAttrs(r, k, nil)
	if match["doc.a"] != "doc" || match["doc.b"] != "doc" || how["free-form"] != 2 {
		t.Errorf("a free-form CRD value should cover the properties under it: %v %v", match, how)
	}
}

func TestPct1(t *testing.T) {
	for _, c := range []struct {
		n, total int
		want     string
	}{{0, 0, "0.0"}, {1, 3, "33.3"}, {2, 3, "66.7"}, {1, 8, "12.5"}, {1, 16, "6.3"}, {5, 5, "100.0"}, {1, 1000, "0.1"}} {
		if got := pct1(c.n, c.total); got != c.want {
			t.Errorf("pct1(%d,%d) = %s, want %s", c.n, c.total, got, c.want)
		}
	}
}

func TestBarSVGFixedWidthDeterministic(t *testing.T) {
	s := svcRow{service: "x", attrTotal: 200, attrOfficial: 100, attrOursBeyond: 20, attrUncov: 80}
	a, b := barSVG(barTitle("x", s), s), barSVG(barTitle("x", s), s)
	if string(a) != string(b) {
		t.Fatal("not deterministic")
	}
	out := string(a)
	for _, want := range []string{`width="300"`, "50.0%", "10.0%", "40.0%", colOfficial, colOurs, colNone} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in\n%s", want, out)
		}
	}
	if strings.Contains(out, "http://") && strings.Count(out, "http://") != 1 {
		t.Error("only the SVG namespace may be a URL: no external references")
	}
	if strings.Contains(out, "<script") || strings.Contains(out, "@import") {
		t.Error("scripts and external fonts are not allowed")
	}
}

func TestReplaceSection(t *testing.T) {
	in := "a\n" + markerStart + "\nold\n" + markerEnd + "\nb\n"
	out, err := replaceSection([]byte(in), "new\n")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "a\n"+markerStart+"\nnew\n"+markerEnd+"\nb\n" {
		t.Errorf("got %q", out)
	}
	if _, err := replaceSection([]byte("no markers"), "x"); err == nil {
		t.Error("missing markers should be an error")
	}
}
