// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package replication_configuration

import (
	"reflect"

	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	"github.com/aws/aws-sdk-go-v2/aws"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/ecr/apis/v1alpha1"
)

// newResourceDelta compares the rules, as upstream pull request #121's
// delta.go does, but on a normal form: a missing and an empty list of
// filters are the same configuration to ECR. spec.registryID is not
// compared: it only names the registry (sdkFind checks it).
func newResourceDelta(a *resource, b *resource) *ackcompare.Delta {
	delta := ackcompare.NewDelta()
	if (a == nil && b != nil) || (a != nil && b == nil) {
		delta.Add("", a, b)
		return delta
	}
	ra, rb := normalRules(a.ko.Spec.ReplicationConfiguration), normalRules(b.ko.Spec.ReplicationConfiguration)
	if !reflect.DeepEqual(ra, rb) {
		delta.Add("Spec.ReplicationConfiguration.Rules", a.ko.Spec.ReplicationConfiguration, b.ko.Spec.ReplicationConfiguration)
	}
	return delta
}

type normalDestination struct{ region, registry string }

type normalFilter struct{ filter, filterType string }

type normalRule struct {
	destinations []normalDestination
	filters      []normalFilter
}

func normalRules(cfg *svcapitypes.ReplicationConfigurationForRegistry) []normalRule {
	if cfg == nil {
		return nil
	}
	var out []normalRule
	for _, r := range cfg.Rules {
		if r == nil {
			continue
		}
		var n normalRule
		for _, d := range r.Destinations {
			if d != nil {
				n.destinations = append(n.destinations, normalDestination{aws.ToString(d.Region), aws.ToString(d.RegistryID)})
			}
		}
		for _, f := range r.RepositoryFilters {
			if f != nil {
				n.filters = append(n.filters, normalFilter{aws.ToString(f.Filter), aws.ToString(f.FilterType)})
			}
		}
		out = append(out, n)
	}
	return out
}
