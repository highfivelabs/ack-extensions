// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package operator

import (
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/highfivelabs/ack-extensions/pkg/fakeaws"
)

const fakeURL = "http://moto.fake-aws.svc.cluster.local:5000"

func TestNewFakeAWS(t *testing.T) {
	for _, bad := range []string{fakeaws.RealEndpoint, "http://moto.example.com:5000", "moto:5000"} {
		if _, err := NewFakeAWS(bad); err == nil {
			t.Errorf("NewFakeAWS(%q) accepted", bad)
		}
	}
	if _, err := NewFakeAWS(fakeURL, "*"); err == nil {
		t.Error("a NO_PROXY of * was accepted")
	}
	if _, err := NewFakeAWS(fakeURL, "10.96.0.1"); err != nil {
		t.Error(err)
	}
}

func env(c *corev1.Container) map[string]string {
	m := map[string]string{}
	for _, e := range c.Env {
		m[e.Name] = e.Value
	}
	return m
}

// ACK's controller as its chart makes it, with only the service endpoint
// at the fake (the incident of 2026-11-09): the manager the operator makes
// beside it in fake mode has every endpoint at the fake and the dead proxy.
func TestFakeBesideManager(t *testing.T) {
	fake, err := NewFakeAWS(fakeURL, "10.96.0.1")
	if err != nil {
		t.Fatal(err)
	}
	src := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "ack-rds-controller", Namespace: "ack-system", Labels: map[string]string{"app.kubernetes.io/name": "rds-chart"}},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "controller",
			Args: []string{"--aws-region", "$(AWS_REGION)", "--aws-endpoint-url", "$(AWS_ENDPOINT_URL)", "--enable-carm=false"},
			Env: []corev1.EnvVar{
				{Name: "AWS_REGION", Value: "us-west-2"},
				{Name: "AWS_ENDPOINT_URL", Value: fakeURL},
				{Name: "AWS_IDENTITY_ENDPOINT_URL", Value: ""},
				{Name: "AWS_PROFILE", Value: "default"},
			},
		}}}}},
	}
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "ack-rds-controller", Namespace: "ack-system"}}

	// Copied as is, it could reach real STS: refused by the check.
	c := src.Spec.Template.Spec.Containers[0]
	if err := fake.Verify(&c); err == nil || !strings.Contains(err.Error(), "--aws-identity-endpoint-url") {
		t.Fatalf("ACK's controller without an identity endpoint passed: %v", err)
	}

	r := &Reconciler{Options: Options{Images: map[string]string{"rds": "example.test/rds:dev"}, Identity: IdentityOwn, FakeAWS: fake}}
	objs, err := r.Plan(src, "rds", sa)
	if err != nil {
		t.Fatal(err)
	}
	var mc *corev1.Container
	for _, o := range objs {
		if d, ok := o.(*appsv1.Deployment); ok {
			mc = &d.Spec.Template.Spec.Containers[0]
		}
	}
	if mc == nil {
		t.Fatal("no manager Deployment")
	}
	for _, want := range []string{"--aws-endpoint-url=" + fakeURL, "--aws-identity-endpoint-url=" + fakeURL, "--aws-region", "--enable-carm=false"} {
		if !slices.Contains(mc.Args, want) {
			t.Errorf("args lack %s: %v", want, mc.Args)
		}
	}
	if slices.Contains(mc.Args, "$(AWS_ENDPOINT_URL)") {
		t.Errorf("ACK's endpoint arguments kept: %v", mc.Args)
	}
	e := env(mc)
	if e["HTTPS_PROXY"] != fakeaws.DeadProxy || e["AWS_ENDPOINT_URL"] != fakeURL || e["NO_PROXY"] != "moto.fake-aws.svc.cluster.local,10.96.0.1" {
		t.Errorf("env: %v", e)
	}
	if _, ok := e["AWS_PROFILE"]; ok {
		t.Errorf("AWS_PROFILE kept: %v", e)
	}
	if err := fake.Verify(mc); err != nil {
		t.Error(err)
	}

	// Without fake mode (EKS) the manager is ACK's controller's settings,
	// untouched.
	r.Options.FakeAWS = nil
	objs, err = r.Plan(src, "rds", sa)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if d, ok := o.(*appsv1.Deployment); ok {
			if got := d.Spec.Template.Spec.Containers[0].Env; len(got) != 4 {
				t.Errorf("env changed without fake mode: %v", got)
			}
		}
	}

	// What cannot be seen is refused in fake mode.
	src.Spec.Template.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "aws"}}}}
	r.Options.FakeAWS = fake
	if _, err := r.Plan(src, "rds", sa); err == nil || !strings.Contains(err.Error(), "envFrom") {
		t.Errorf("envFrom in fake mode: %v", err)
	}
}
