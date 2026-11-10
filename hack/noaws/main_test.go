// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/highfivelabs/ack-extensions/pkg/fakeaws"
)

// Every rule catches what it is for.
func TestScanBad(t *testing.T) {
	r, err := Scan("testdata/bad")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"tests/e2e/bad_test.go:16: G1",
		"tests/e2e/bad_test.go:17: G1",
		"tests/e2e/bad_test.go:18: G1",
		"tests/e2e/bad_test.go:19: G2",
		"tests/e2e/bad_test.go:20: G3",
		"tests/e2e/bad_test.go:21: G4",
		"tests/e2e/bad_test.go:22: G4",
		"tests/operator/bad.sh:4: S1",
		"tests/operator/bad.sh:7: S1",
		"tests/operator/bad.sh:8: G4",
		"Taskfile.yml:1: L1: the global env",
		"Taskfile.yml:1: L1: task check calls test-live",
		"Taskfile.yml:1: L1: task test reaches the live tests",
		"pkg/oops/real.go:1: L2",
	}
	got := strings.Join(r.Findings, "\n")
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("not found: %s", w)
		}
	}
	if len(r.Findings) != len(want) {
		t.Errorf("%d findings, want %d:\n%s", len(r.Findings), len(want), got)
	}
}

// Principals, annotation keys, XML namespaces, `go` and processes given
// fakeaws's environment pass.
func TestScanGood(t *testing.T) {
	r, err := Scan("testdata/good")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) > 0 || r.GoFiles != 1 || r.OtherFiles != 1 {
		t.Errorf("%+v", r)
	}
}

func deployments(t *testing.T, cs ...corev1.Container) *bytes.Buffer {
	t.Helper()
	var l appsv1.DeploymentList
	for _, c := range cs {
		d := appsv1.Deployment{}
		d.Namespace, d.Name = "ack-system", c.Name
		d.Spec.Template.Spec.Containers = []corev1.Container{c}
		l.Items = append(l.Items, d)
	}
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(&l); err != nil {
		t.Fatal(err)
	}
	return &b
}

func TestVerifyDeployments(t *testing.T) {
	e := fakeaws.MustParse("http://moto.fake-aws.svc.cluster.local:5000", fakeaws.Cluster)
	var env []corev1.EnvVar
	for _, kv := range e.Env("10.96.0.1") {
		k, v, _ := strings.Cut(kv, "=")
		env = append(env, corev1.EnvVar{Name: k, Value: v})
	}
	good := corev1.Container{Name: "good", Args: append([]string{"--aws-region=us-west-2"}, e.Args()...), Env: env}
	moto := corev1.Container{Name: "moto", Args: []string{"-H", "0.0.0.0"}}
	var out bytes.Buffer
	if err := verifyDeployments(deployments(t, good, moto), &out); err != nil || !strings.Contains(out.String(), "ack-system/good/good") {
		t.Errorf("%v %s", err, out.String())
	}
	// The incident: the service endpoint only.
	bad := corev1.Container{Name: "bad", Args: []string{"--aws-region=us-west-2", "--aws-endpoint-url=" + e.String()}, Env: env}
	if err := verifyDeployments(deployments(t, good, bad), &out); err == nil || !strings.Contains(err.Error(), "ack-system/bad/bad") {
		t.Errorf("the incident's manager passed: %v", err)
	}
	if err := verifyDeployments(deployments(t, moto), &out); err == nil {
		t.Error("no AWS client passed")
	}
}
