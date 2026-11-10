// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package operator

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/highfivelabs/ack-extensions/pkg/fakeaws"
)

// FakeAWS is the operator's test-only mode (kind): one endpoint, a fake AWS
// in the cluster, that every manager the operator makes is pointed at
// together (both of ACK's runtime endpoints, AWS_ENDPOINT_URL, fake
// credentials and a dead proxy), whatever ACK's controller it copies
// says. It is set from the operator's own flag, never from a context, and
// never on EKS.
type FakeAWS struct {
	Endpoint fakeaws.Endpoint
	// NoProxy is what besides the fake must skip the dead proxy: the
	// Kubernetes API's address.
	NoProxy []string
}

// NewFakeAWS parses the endpoint (in the cluster or on loopback, never an
// AWS domain) and checks that what it makes passes fakeaws.Verify.
func NewFakeAWS(raw string, noProxy ...string) (*FakeAWS, error) {
	e, err := fakeaws.Parse(raw, fakeaws.Cluster)
	if err != nil {
		return nil, err
	}
	f := &FakeAWS{Endpoint: e, NoProxy: noProxy}
	c := corev1.Container{}
	f.apply(&c)
	if err := f.Verify(&c); err != nil {
		return nil, err
	}
	return f, nil
}

// endpointFlags are ACK's runtime flags that name an endpoint.
var endpointFlags = map[string]bool{"--aws-endpoint-url": true, "--aws-identity-endpoint-url": true}

// apply replaces every endpoint, proxy and AWS credential setting of c with
// the fake's.
func (f *FakeAWS) apply(c *corev1.Container) {
	var args []string
	for i := 0; i < len(c.Args); i++ {
		name, _, hasVal := strings.Cut(c.Args[i], "=")
		if endpointFlags[name] {
			if !hasVal && i+1 < len(c.Args) {
				i++
			}
			continue
		}
		args = append(args, c.Args[i])
	}
	c.Args = append(args, f.Endpoint.Args()...)
	fake := map[string]bool{}
	var add []corev1.EnvVar
	for _, kv := range f.Endpoint.Env(f.NoProxy...) {
		k, v, _ := strings.Cut(kv, "=")
		fake[k] = true
		add = append(add, corev1.EnvVar{Name: k, Value: v})
	}
	var env []corev1.EnvVar
	for _, e := range c.Env {
		// ACK's chart's $(AWS_ENDPOINT_URL) and $(AWS_IDENTITY_ENDPOINT_URL)
		// arguments are replaced above; their variables go too.
		if fake[e.Name] || strings.Contains(e.Name, "ENDPOINT_URL") || e.Name == "AWS_PROFILE" {
			continue
		}
		env = append(env, e)
	}
	c.Env = append(env, add...)
}

// Verify is VerifyContainer.
func (f *FakeAWS) Verify(c *corev1.Container) error { return VerifyContainer(c) }

// VerifyContainer is fakeaws.Verify on a container (in the cluster).
// Variables from envFrom, or AWS and proxy variables from valueFrom, cannot
// be seen here, so they are refused.
func VerifyContainer(c *corev1.Container) error {
	if len(c.EnvFrom) > 0 {
		return fmt.Errorf("container %s: envFrom cannot be checked for AWS settings in fake-AWS mode", c.Name)
	}
	var env []string
	for _, e := range c.Env {
		if e.ValueFrom != nil {
			if strings.HasPrefix(strings.ToUpper(e.Name), "AWS_") || strings.HasSuffix(strings.ToUpper(e.Name), "_PROXY") {
				return fmt.Errorf("container %s: %s comes from valueFrom, which cannot be checked in fake-AWS mode", c.Name, e.Name)
			}
			continue
		}
		env = append(env, e.Name+"="+e.Value)
	}
	if err := fakeaws.Verify(c.Args, env, fakeaws.Cluster); err != nil {
		return fmt.Errorf("container %s: %w", c.Name, err)
	}
	return nil
}

// Fake points a manager's container at the fake and verifies it; with no
// fake (on EKS) it does nothing.
func (f *FakeAWS) Fake(c *corev1.Container) error {
	if f == nil {
		return nil
	}
	f.apply(c)
	return f.Verify(c)
}
