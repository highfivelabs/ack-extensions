// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	orgv1 "github.com/highfivelabs/ack-extensions/services/organizations/apis/v1alpha1"
)

const deletionPolicy = "services.k8s.aws/deletion-policy"

// installRetainOnly applies config/admission/retain-only.yaml, the policy
// the install ships, to the test's API server.
func installRetainOnly(t *testing.T, ctx context.Context, c client.Client) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(harness.RepoRoot(t), "config", "admission", "retain-only.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	docs := bytes.Split(raw, []byte("\n---\n"))
	if len(docs) != 2 {
		t.Fatalf("retain-only.yaml: want a policy and a binding, got %d documents", len(docs))
	}
	policy, binding := &admissionv1.ValidatingAdmissionPolicy{}, &admissionv1.ValidatingAdmissionPolicyBinding{}
	if err := yaml.UnmarshalStrict(docs[0], policy); err != nil {
		t.Fatal(err)
	}
	if err := yaml.UnmarshalStrict(docs[1], binding); err != nil {
		t.Fatal(err)
	}
	for _, obj := range []client.Object{policy, binding} {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
}

// Organization: the account's organization, adopted (or created when there
// is none), its root's policy types and its trusted service access. It is
// never deleted from Kubernetes.
func TestOrganizationsOrganization(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = orgv1.AddToScheme(scheme)
	env := harness.Start(t, "organizations", scheme)
	cloud := organizations.NewFromConfig(env.AWS)
	ctx := context.Background()
	const scp, tag = "SERVICE_CONTROL_POLICY", "TAG_POLICY"
	const guardduty, sso, config = "guardduty.amazonaws.com", "sso.amazonaws.com", "config.amazonaws.com"

	waitSynced := func(t *testing.T, obj *orgv1.Organization) *orgv1.Organization {
		t.Helper()
		got := &orgv1.Organization{}
		harness.Eventually(t, 60*time.Second, obj.Name+" synced", func() error {
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
				return err
			}
			if ok, why := synced(got.Status.Conditions); !ok {
				return fmt.Errorf("not synced: %s", why)
			}
			return nil
		})
		return got
	}
	gone := func(t *testing.T, obj client.Object) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.GetName()+" gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &orgv1.Organization{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}
	orgID := func(t *testing.T) string {
		t.Helper()
		out, err := cloud.DescribeOrganization(ctx, &organizations.DescribeOrganizationInput{})
		if err != nil {
			t.Fatalf("the organization: %v", err)
		}
		return aws.ToString(out.Organization.Id)
	}
	// cloudState is the root's enabled policy types and the trusted service
	// principals, each sorted and comma-joined.
	cloudState := func() (string, string, error) {
		roots, err := cloud.ListRoots(ctx, &organizations.ListRootsInput{})
		if err != nil || len(roots.Roots) != 1 {
			return "", "", fmt.Errorf("roots %+v: %v", roots, err)
		}
		var types, principals []string
		for _, p := range roots.Roots[0].PolicyTypes {
			if p.Status == orgtypes.PolicyTypeStatusEnabled {
				types = append(types, string(p.Type))
			}
		}
		svc, err := cloud.ListAWSServiceAccessForOrganization(ctx, &organizations.ListAWSServiceAccessForOrganizationInput{})
		if err != nil {
			return "", "", err
		}
		for _, p := range svc.EnabledServicePrincipals {
			principals = append(principals, aws.ToString(p.ServicePrincipal))
		}
		return strings.Join(sortedIDs(types...), ","), strings.Join(sortedIDs(principals...), ","), nil
	}
	cloudIs := func(t *testing.T, wantTypes, wantPrincipals string) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, "policy types "+wantTypes+", service access "+wantPrincipals, func() error {
			types, principals, err := cloudState()
			if err != nil {
				return err
			}
			if types != wantTypes || principals != wantPrincipals {
				return fmt.Errorf("policy types %q, service access %q", types, principals)
			}
			return nil
		})
	}
	update := func(obj *orgv1.Organization, f func(*orgv1.Organization)) error {
		got := &orgv1.Organization{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
			return err
		}
		f(got)
		return env.Client.Update(ctx, got)
	}
	// refused is the error of an update the API server must refuse, retried
	// while it only conflicts with the controller's own writes.
	refused := func(t *testing.T, obj *orgv1.Organization, f func(*orgv1.Organization)) error {
		t.Helper()
		var err error
		harness.Eventually(t, 30*time.Second, "a refusal", func() error {
			err = update(obj, f)
			if err == nil {
				t.Fatal("the update was accepted")
			}
			if strings.Contains(err.Error(), "the object has been modified") {
				return err
			}
			return nil
		})
		return err
	}
	org := func(name, policy string, types, principals []string) *orgv1.Organization {
		o := &orgv1.Organization{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: orgv1.OrganizationSpec{
				EnabledPolicyTypes:         aws.StringSlice(types),
				AWSServiceAccessPrincipals: aws.StringSlice(principals),
			},
		}
		if policy != "" {
			o.Annotations = map[string]string{deletionPolicy: policy}
		}
		return o
	}

	// Before the admission policy is installed, as on a cluster that
	// predates it: the guarantee is the controller's, not the policy's.
	first := org("first", "delete", []string{scp}, []string{guardduty})
	first.Spec.FeatureSet = aws.String("ALL")
	var id string

	t.Run("with no organization, one is made", func(t *testing.T) {
		if _, err := cloud.DescribeOrganization(ctx, &organizations.DescribeOrganizationInput{}); err == nil {
			t.Fatal("moto already has an organization")
		}
		if err := env.Client.Create(ctx, first); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t, first)
		id = orgID(t)
		if aws.ToString(got.Status.ID) != id || got.Status.RootID == nil || got.Status.MasterAccountID == nil {
			t.Fatalf("status %+v; the organization is %s", got.Status, id)
		}
		cloudIs(t, scp, guardduty)
	})

	t.Run("deleting the object never deletes the organization, whatever its deletion policy", func(t *testing.T) {
		if err := env.Client.Delete(ctx, first); err != nil {
			t.Fatal(err)
		}
		gone(t, first)
		// moto deletes an organization with no member accounts, as AWS
		// does, so it would be gone had DeleteOrganization been called.
		if got := orgID(t); got != id {
			t.Fatalf("the organization is %s, was %s", got, id)
		}
		cloudIs(t, scp, guardduty)
	})

	installRetainOnly(t, ctx, env.Client)

	t.Run("an Organization without the retain annotation is refused", func(t *testing.T) {
		// The policy takes effect a moment after it is made; a dry run
		// creates nothing while it does not.
		harness.Eventually(t, 30*time.Second, "the policy's refusal", func() error {
			err := env.Client.Create(ctx, org("bare", "", nil, nil), client.DryRunAll)
			if err == nil {
				return fmt.Errorf("accepted")
			}
			if !strings.Contains(err.Error(), "must carry the annotation services.k8s.aws/deletion-policy: retain") {
				t.Fatalf("refused for another reason: %v", err)
			}
			return nil
		})
		if err := env.Client.Create(ctx, org("deletes", "delete", nil, nil)); err == nil || !strings.Contains(err.Error(), "deletion-policy: retain") {
			t.Fatalf("deletion-policy delete: %v", err)
		}
	})

	managed := org("main", "retain", []string{scp, tag}, []string{guardduty, sso})

	t.Run("the existing organization is adopted, and settles", func(t *testing.T) {
		if err := env.Client.Create(ctx, managed); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t, managed)
		if aws.ToString(got.Status.ID) != id {
			t.Fatalf("adopted %v, the organization is %s", got.Status.ID, id)
		}
		cloudIs(t, scp+","+tag, guardduty+","+sso)
		harness.Eventually(t, 30*time.Second, "featureSet late-initialized", func() error {
			got := &orgv1.Organization{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(managed), got); err != nil {
				return err
			}
			if aws.ToString(got.Spec.FeatureSet) != "ALL" {
				return fmt.Errorf("featureSet %v", got.Spec.FeatureSet)
			}
			return nil
		})
		updates := func() int { return strings.Count(env.Logs(), `>>> rm.customUpdateOrganization`) }
		before := updates()
		time.Sleep(15 * time.Second) // three resyncs
		if n := updates(); n != before {
			t.Fatalf("updated %d more times across resyncs", n-before)
		}
	})

	t.Run("the retain annotation cannot be changed or removed", func(t *testing.T) {
		for what, f := range map[string]func(*orgv1.Organization){
			"changed": func(o *orgv1.Organization) { o.Annotations[deletionPolicy] = "delete" },
			"removed": func(o *orgv1.Organization) { delete(o.Annotations, deletionPolicy) },
		} {
			if err := refused(t, managed, f); !strings.Contains(err.Error(), "which cannot be removed or changed") {
				t.Fatalf("%s: %v", what, err)
			}
		}
	})

	t.Run("the feature set cannot change", func(t *testing.T) {
		err := refused(t, managed, func(o *orgv1.Organization) { o.Spec.FeatureSet = aws.String("CONSOLIDATED_BILLING") })
		if !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("featureSet: %v", err)
		}
	})

	t.Run("changes outside Kubernetes are put back", func(t *testing.T) {
		got := &orgv1.Organization{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(managed), got); err != nil {
			t.Fatal(err)
		}
		if _, err := cloud.DisablePolicyType(ctx, &organizations.DisablePolicyTypeInput{RootId: got.Status.RootID, PolicyType: tag}); err != nil {
			t.Fatal(err)
		}
		if _, err := cloud.EnableAWSServiceAccess(ctx, &organizations.EnableAWSServiceAccessInput{ServicePrincipal: aws.String(config)}); err != nil {
			t.Fatal(err)
		}
		cloudIs(t, scp+","+tag, guardduty+","+sso)
	})

	t.Run("an entry taken off a list is disabled", func(t *testing.T) {
		harness.Eventually(t, 30*time.Second, "spec updated", func() error {
			return update(managed, func(o *orgv1.Organization) {
				o.Spec.EnabledPolicyTypes = aws.StringSlice([]string{scp})
				o.Spec.AWSServiceAccessPrincipals = aws.StringSlice([]string{sso})
			})
		})
		cloudIs(t, scp, sso)
	})

	t.Run("delete leaves the organization as it is", func(t *testing.T) {
		if err := env.Client.Delete(ctx, managed); err != nil {
			t.Fatal(err)
		}
		gone(t, managed)
		if got := orgID(t); got != id {
			t.Fatalf("the organization is %s, was %s", got, id)
		}
		cloudIs(t, scp, sso)
		if strings.Contains(env.Logs(), "DeleteOrganization") {
			t.Fatal("the controller logged a DeleteOrganization call")
		}
	})
}
