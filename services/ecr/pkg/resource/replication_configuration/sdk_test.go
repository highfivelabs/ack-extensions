// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package replication_configuration

import (
	"errors"
	"testing"

	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	"github.com/aws/aws-sdk-go-v2/aws"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/ecr/apis/v1alpha1"
)

func config(filters []*svcapitypes.RepositoryFilter) *svcapitypes.ReplicationConfigurationForRegistry {
	return &svcapitypes.ReplicationConfigurationForRegistry{Rules: []*svcapitypes.ReplicationRule{{
		Destinations:      []*svcapitypes.ReplicationDestination{{Region: aws.String("eu-west-1"), RegistryID: aws.String("123456789012")}},
		RepositoryFilters: filters,
	}}}
}

func withConfig(cfg *svcapitypes.ReplicationConfigurationForRegistry) *resource {
	return &resource{ko: &svcapitypes.ReplicationConfiguration{Spec: svcapitypes.ReplicationConfigurationSpec{ReplicationConfiguration: cfg}}}
}

// What is put reads back as the same spec.
func TestRoundTrip(t *testing.T) {
	want := config([]*svcapitypes.RepositoryFilter{{Filter: aws.String("prod-"), FilterType: aws.String("PREFIX_MATCH")}})
	got := &svcapitypes.ReplicationConfigurationForRegistry{Rules: rulesFromSDK(configurationToSDK(want))}
	if d := newResourceDelta(withConfig(want), withConfig(got)); len(d.Differences) != 0 {
		t.Fatalf("round trip differs: %v", d.Differences)
	}
}

// ECR reads no filters back as none; the spec may say [] or leave it out.
func TestDeltaNoFiltersIsNoFilters(t *testing.T) {
	a, b := withConfig(config(nil)), withConfig(config([]*svcapitypes.RepositoryFilter{}))
	if d := newResourceDelta(a, b); len(d.Differences) != 0 {
		t.Fatalf("nil and empty filters differ: %v", d.Differences)
	}
	c := withConfig(config([]*svcapitypes.RepositoryFilter{{Filter: aws.String("x"), FilterType: aws.String("PREFIX_MATCH")}}))
	if d := newResourceDelta(a, c); len(d.Differences) == 0 {
		t.Fatal("a filter added is no difference")
	}
}

// Deleting puts no rules; a nil spec is no rules too, never a nil
// configuration (which ECR would refuse).
func TestNilConfigurationIsNoRules(t *testing.T) {
	out := configurationToSDK(nil)
	if out == nil || out.Rules == nil || len(out.Rules) != 0 {
		t.Fatalf("configurationToSDK(nil) = %+v", out)
	}
}

func TestRegistryMismatch(t *testing.T) {
	ko := &svcapitypes.ReplicationConfiguration{}
	if err := registryMismatch(ko, "123456789012"); err != nil {
		t.Fatalf("no registryID: %v", err)
	}
	ko.Spec.RegistryID = aws.String("123456789012")
	if err := registryMismatch(ko, "123456789012"); err != nil {
		t.Fatalf("own registry: %v", err)
	}
	ko.Spec.RegistryID = aws.String("210987654321")
	var term *ackerr.TerminalError
	if err := registryMismatch(ko, "123456789012"); !errors.As(err, &term) {
		t.Fatalf("another registry: %v, want a terminal error", err)
	}
}
