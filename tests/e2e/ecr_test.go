// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	ecrv1 "github.com/highfivelabs/ack-extensions/services/ecr/apis/v1alpha1"
)

// cloudReplication is the registry's replication rules in the fake cloud,
// one string per rule: "region/registry,... filter:type,...".
func cloudReplication(ctx context.Context, c *ecr.Client) (registry string, rules []string, err error) {
	out, err := c.DescribeRegistry(ctx, &ecr.DescribeRegistryInput{})
	if err != nil {
		return "", nil, err
	}
	if out.ReplicationConfiguration == nil {
		return aws.ToString(out.RegistryId), nil, nil
	}
	for _, r := range out.ReplicationConfiguration.Rules {
		var dests, filters []string
		for _, d := range r.Destinations {
			dests = append(dests, aws.ToString(d.Region)+"/"+aws.ToString(d.RegistryId))
		}
		for _, f := range r.RepositoryFilters {
			filters = append(filters, aws.ToString(f.Filter)+":"+string(f.FilterType))
		}
		rules = append(rules, strings.Join(dests, ",")+" "+strings.Join(filters, ","))
	}
	return aws.ToString(out.RegistryId), rules, nil
}

func replicationRule(region, filter string) *ecrv1.ReplicationRule {
	r := &ecrv1.ReplicationRule{Destinations: []*ecrv1.ReplicationDestination{{Region: aws.String(region), RegistryID: aws.String("123456789012")}}}
	if filter != "" {
		r.RepositoryFilters = []*ecrv1.RepositoryFilter{{Filter: aws.String(filter), FilterType: aws.String("PREFIX_MATCH")}}
	}
	return r
}

// configurationOf is one rule to region, as someone outside Kubernetes
// would put it.
func configurationOf(region string) *ecrtypes.ReplicationConfiguration {
	return &ecrtypes.ReplicationConfiguration{Rules: []ecrtypes.ReplicationRule{{
		Destinations: []ecrtypes.ReplicationDestination{{Region: aws.String(region), RegistryId: aws.String("123456789012")}},
	}}}
}

// ReplicationConfiguration: the registry's replication rules, put whole
// with PutReplicationConfiguration and read with DescribeRegistry. There
// is one per registry (account and region).
//
// moto accepts at most one rule ("This feature is disabled" for more;
// AWS allows ten), so every step here uses one.
func TestReplicationConfiguration(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = ecrv1.AddToScheme(scheme)
	env := harness.Start(t, "ecr", scheme)
	cloud := ecr.NewFromConfig(env.AWS)
	ctx := context.Background()

	waitRules := func(t *testing.T, obj *ecrv1.ReplicationConfiguration, want ...string) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, fmt.Sprintf("rules %v in the cloud and %s synced", want, obj.Name), func() error {
			_, got, err := cloudReplication(ctx, cloud)
			if err != nil {
				return err
			}
			if strings.Join(got, ";") != strings.Join(want, ";") {
				return fmt.Errorf("cloud rules %v, want %v", got, want)
			}
			cur := &ecrv1.ReplicationConfiguration{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), cur); err != nil {
				return err
			}
			if ok, why := synced(cur.Status.Conditions); !ok {
				return fmt.Errorf("not synced: %s", why)
			}
			return nil
		})
	}
	gone := func(t *testing.T, obj client.Object) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.GetName()+" gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &ecrv1.ReplicationConfiguration{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}

	registry := &ecrv1.ReplicationConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "registry", Namespace: "default"},
		Spec: ecrv1.ReplicationConfigurationSpec{
			ReplicationConfiguration: &ecrv1.ReplicationConfigurationForRegistry{Rules: []*ecrv1.ReplicationRule{replicationRule("us-east-1", "")}},
		},
	}

	t.Run("create puts the rules", func(t *testing.T) {
		if err := env.Client.Create(ctx, registry); err != nil {
			t.Fatal(err)
		}
		waitRules(t, registry, "us-east-1/123456789012 ")
		got := &ecrv1.ReplicationConfiguration{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(registry), got); err != nil {
			t.Fatal(err)
		}
		if aws.ToString(got.Status.RegistryID) != "123456789012" {
			t.Fatalf("status.registryID = %q", aws.ToString(got.Status.RegistryID))
		}
	})

	t.Run("a changed rule is put again", func(t *testing.T) {
		obj := &ecrv1.ReplicationConfiguration{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(registry), obj); err != nil {
			t.Fatal(err)
		}
		obj.Spec.ReplicationConfiguration.Rules = []*ecrv1.ReplicationRule{replicationRule("eu-west-1", "prod-")}
		if err := env.Client.Update(ctx, obj); err != nil {
			t.Fatal(err)
		}
		waitRules(t, registry, "eu-west-1/123456789012 prod-:PREFIX_MATCH")
	})

	t.Run("a rule changed outside Kubernetes is put back", func(t *testing.T) {
		if _, err := cloud.PutReplicationConfiguration(ctx, &ecr.PutReplicationConfigurationInput{
			ReplicationConfiguration: configurationOf("ap-south-1"),
		}); err != nil {
			t.Fatal(err)
		}
		waitRules(t, registry, "eu-west-1/123456789012 prod-:PREFIX_MATCH")
	})

	t.Run("the CRD refuses what ECR would", func(t *testing.T) {
		for _, c := range []struct {
			name string
			cfg  *ecrv1.ReplicationConfigurationForRegistry
			want string
		}{
			{"no rules", &ecrv1.ReplicationConfigurationForRegistry{Rules: []*ecrv1.ReplicationRule{}}, "spec.replicationConfiguration.rules"},
			{"a filter type ECR does not have", &ecrv1.ReplicationConfigurationForRegistry{Rules: []*ecrv1.ReplicationRule{{
				Destinations:      []*ecrv1.ReplicationDestination{{Region: aws.String("us-east-1"), RegistryID: aws.String("123456789012")}},
				RepositoryFilters: []*ecrv1.RepositoryFilter{{Filter: aws.String("x"), FilterType: aws.String("SUFFIX_MATCH")}},
			}}}, "filterType"},
			{"a registry that is not an account ID", &ecrv1.ReplicationConfigurationForRegistry{Rules: []*ecrv1.ReplicationRule{{
				Destinations: []*ecrv1.ReplicationDestination{{Region: aws.String("us-east-1"), RegistryID: aws.String("prod")}},
			}}}, "registryID"},
		} {
			obj := &ecrv1.ReplicationConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "refused", Namespace: "default"},
				Spec: ecrv1.ReplicationConfigurationSpec{ReplicationConfiguration: c.cfg}}
			err := env.Client.Create(ctx, obj)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s: got %v, want the CRD's refusal naming %q", c.name, err, c.want)
			}
		}
	})

	t.Run("another account's registry is refused", func(t *testing.T) {
		// PutReplicationConfiguration acts on the caller's own registry.
		other := &ecrv1.ReplicationConfiguration{
			ObjectMeta: metav1.ObjectMeta{Name: "other-account", Namespace: "default"},
			Spec: ecrv1.ReplicationConfigurationSpec{RegistryID: aws.String("210987654321"),
				ReplicationConfiguration: &ecrv1.ReplicationConfigurationForRegistry{Rules: []*ecrv1.ReplicationRule{replicationRule("us-east-1", "")}}},
		}
		if err := env.Client.Create(ctx, other); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "terminal condition", func() error {
			obj := &ecrv1.ReplicationConfiguration{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(other), obj); err != nil {
				return err
			}
			for _, c := range obj.Status.Conditions {
				if c.Type == ackv1alpha1.ConditionTypeTerminal && c.Status == corev1.ConditionTrue &&
					strings.Contains(aws.ToString(c.Message), "210987654321") {
					return nil
				}
			}
			return fmt.Errorf("no terminal condition naming the registry: %v", obj.Status.Conditions)
		})
		_, rules, err := cloudReplication(ctx, cloud)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(rules, ";") != "eu-west-1/123456789012 prod-:PREFIX_MATCH" {
			t.Fatalf("the refused object changed the registry: %v", rules)
		}
		if err := env.Client.Delete(ctx, other); err != nil {
			t.Fatal(err)
		}
		gone(t, other)
	})

	t.Run("delete puts no rules", func(t *testing.T) {
		if err := env.Client.Delete(ctx, registry); err != nil {
			t.Fatal(err)
		}
		gone(t, registry)
		_, rules, err := cloudReplication(ctx, cloud)
		if err != nil {
			t.Fatal(err)
		}
		if len(rules) != 0 {
			t.Fatalf("rules left after delete: %v", rules)
		}
	})

	t.Run("deletion-policy retain leaves the rules", func(t *testing.T) {
		keep := &ecrv1.ReplicationConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Name: "kept", Namespace: "default",
				Annotations: map[string]string{ackv1alpha1.AnnotationDeletionPolicy: string(ackv1alpha1.DeletionPolicyRetain)},
			},
			Spec: ecrv1.ReplicationConfigurationSpec{
				ReplicationConfiguration: &ecrv1.ReplicationConfigurationForRegistry{Rules: []*ecrv1.ReplicationRule{replicationRule("us-east-2", "")}},
			},
		}
		if err := env.Client.Create(ctx, keep); err != nil {
			t.Fatal(err)
		}
		waitRules(t, keep, "us-east-2/123456789012 ")
		if err := env.Client.Delete(ctx, keep); err != nil {
			t.Fatal(err)
		}
		gone(t, keep)
		_, rules, err := cloudReplication(ctx, cloud)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(rules, ";") != "us-east-2/123456789012 " {
			t.Fatalf("deletion-policy retain changed the rules: %v", rules)
		}
	})
}
