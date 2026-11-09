// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	orgv1 "github.com/highfivelabs/ack-extensions/services/organizations/apis/v1alpha1"
)

// orgTags is a policy's tags in the cloud, as sorted k=v pairs, without the
// services.k8s.aws/ tags ACK's runtime adds to every resource.
func orgTags(ctx context.Context, c *organizations.Client, id string) (string, error) {
	out, err := c.ListTagsForResource(ctx, &organizations.ListTagsForResourceInput{ResourceId: aws.String(id)})
	if err != nil {
		return "", err
	}
	var kv []string
	for _, t := range out.Tags {
		if strings.HasPrefix(aws.ToString(t.Key), "services.k8s.aws/") {
			continue
		}
		kv = append(kv, aws.ToString(t.Key)+"="+aws.ToString(t.Value))
	}
	sort.Strings(kv)
	return strings.Join(kv, ","), nil
}

const (
	denyDeleteBucket = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"s3:DeleteBucket","Resource":"*"}]}`
	denyLeaveOrg     = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"organizations:LeaveOrganization","Resource":"*"}]}`
)

// sameJSON reports whether two documents mean the same: moto stores a
// policy's content re-serialised, with different white space (AWS keeps
// the text as given), so the tests compare the documents, not the text.
func sameJSON(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return a == b
	}
	return reflect.DeepEqual(x, y)
}

// Policy: a service control policy (and the other types) in AWS
// Organizations, beside ACK's organizations-controller.
func TestOrganizationsPolicy(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = orgv1.AddToScheme(scheme)
	env := harness.Start(t, "organizations", scheme)
	cloud := organizations.NewFromConfig(env.AWS)
	ctx := context.Background()

	// The organization is the management account's; Policy needs one.
	if _, err := cloud.CreateOrganization(ctx, &organizations.CreateOrganizationInput{FeatureSet: orgtypes.OrganizationFeatureSetAll}); err != nil {
		t.Fatal(err)
	}

	waitSynced := func(t *testing.T, obj *orgv1.Policy) *orgv1.Policy {
		t.Helper()
		got := &orgv1.Policy{}
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
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &orgv1.Policy{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}
	cloudPolicy := func(id string) (*orgtypes.Policy, error) {
		out, err := cloud.DescribePolicy(ctx, &organizations.DescribePolicyInput{PolicyId: aws.String(id)})
		if err != nil {
			return nil, err
		}
		return out.Policy, nil
	}
	updates := func(name string) int {
		n := 0
		for _, line := range strings.Split(env.Logs(), "\n") {
			if strings.Contains(line, ">>> rm.sdkUpdate") && strings.Contains(line, fmt.Sprintf(`"name": %q`, name)) {
				n++
			}
		}
		return n
	}
	update := func(t *testing.T, obj *orgv1.Policy, f func(*orgv1.Policy)) {
		t.Helper()
		harness.Eventually(t, 30*time.Second, "spec updated", func() error {
			got := &orgv1.Policy{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
				return err
			}
			f(got)
			return env.Client.Update(ctx, got)
		})
	}

	scp := &orgv1.Policy{
		ObjectMeta: metav1.ObjectMeta{Name: "no-bucket-deletes", Namespace: "default"},
		Spec: orgv1.PolicySpec{
			Name:        aws.String("no-bucket-deletes"),
			Description: aws.String("Nobody deletes a bucket"),
			Type:        aws.String("SERVICE_CONTROL_POLICY"),
			Content:     aws.String(denyDeleteBucket),
			Tags:        []*orgv1.Tag{{Key: aws.String("team"), Value: aws.String("platform")}},
		},
	}
	var id string

	t.Run("a service control policy is made, tagged, and settles", func(t *testing.T) {
		if err := env.Client.Create(ctx, scp); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t, scp)
		if got.Status.ID == nil || got.Status.ACKResourceMetadata == nil || got.Status.ACKResourceMetadata.ARN == nil {
			t.Fatalf("status has no id or ARN: %+v", got.Status)
		}
		id = *got.Status.ID
		p, err := cloudPolicy(id)
		if err != nil {
			t.Fatal(err)
		}
		s := p.PolicySummary
		if aws.ToString(s.Name) != "no-bucket-deletes" || aws.ToString(s.Description) != "Nobody deletes a bucket" ||
			s.Type != orgtypes.PolicyTypeServiceControlPolicy || !sameJSON(aws.ToString(p.Content), denyDeleteBucket) {
			t.Fatalf("cloud policy %+v", p)
		}
		if aws.ToString(s.Arn) != string(*got.Status.ACKResourceMetadata.ARN) {
			t.Fatalf("ARN %s, status %s", aws.ToString(s.Arn), *got.Status.ACKResourceMetadata.ARN)
		}
		if tags, err := orgTags(ctx, cloud, id); err != nil || tags != "team=platform" {
			t.Fatalf("tags %q: %v", tags, err)
		}
		before := updates("no-bucket-deletes")
		time.Sleep(15 * time.Second)
		if n := updates("no-bucket-deletes"); n != before {
			t.Fatalf("updated %d times across resyncs", n-before)
		}
	})

	t.Run("a reformatted document is no change", func(t *testing.T) {
		before := updates("no-bucket-deletes")
		update(t, scp, func(p *orgv1.Policy) {
			p.Spec.Content = aws.String(`{
  "Statement": [{"Resource": "*", "Action": "s3:DeleteBucket", "Effect": "Deny"}],
  "Version": "2012-10-17"
}`)
		})
		time.Sleep(15 * time.Second)
		if n := updates("no-bucket-deletes"); n != before {
			t.Fatalf("updated %d times for a document that means the same", n-before)
		}
		p, err := cloudPolicy(id)
		if err != nil || !sameJSON(aws.ToString(p.Content), denyDeleteBucket) {
			t.Fatalf("cloud content %+v: %v", p, err)
		}
	})

	t.Run("a changed document, description and name are updated", func(t *testing.T) {
		update(t, scp, func(p *orgv1.Policy) {
			p.Spec.Content = aws.String(denyLeaveOrg)
			p.Spec.Description = aws.String("Nobody leaves")
			p.Spec.Name = aws.String("no-leaving")
		})
		harness.Eventually(t, 60*time.Second, "cloud policy updated", func() error {
			p, err := cloudPolicy(id)
			if err != nil {
				return err
			}
			if !sameJSON(aws.ToString(p.Content), denyLeaveOrg) || aws.ToString(p.PolicySummary.Description) != "Nobody leaves" || aws.ToString(p.PolicySummary.Name) != "no-leaving" {
				return fmt.Errorf("cloud policy %+v", p)
			}
			return nil
		})
		waitSynced(t, scp)
	})

	t.Run("its tags are synced", func(t *testing.T) {
		update(t, scp, func(p *orgv1.Policy) {
			p.Spec.Tags = []*orgv1.Tag{{Key: aws.String("env"), Value: aws.String("prod")}}
		})
		harness.Eventually(t, 60*time.Second, "tags env=prod", func() error {
			tags, err := orgTags(ctx, cloud, id)
			if err != nil {
				return err
			}
			if tags != "env=prod" {
				return fmt.Errorf("tags %q", tags)
			}
			return nil
		})
		// A tags-only change does not call UpdatePolicy: the document is
		// still the one the previous step put.
		p, err := cloudPolicy(id)
		if err != nil || !sameJSON(aws.ToString(p.Content), denyLeaveOrg) {
			t.Fatalf("cloud policy %+v: %v", p, err)
		}
	})

	t.Run("the type cannot change", func(t *testing.T) {
		got := &orgv1.Policy{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(scp), got); err != nil {
			t.Fatal(err)
		}
		got.Spec.Type = aws.String("TAG_POLICY")
		err := env.Client.Update(ctx, got)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("changing type: got %v, want the CRD's immutability refusal", err)
		}
	})

	t.Run("a second policy with the same name is refused", func(t *testing.T) {
		// moto checks neither the document nor the type's rules; it does
		// refuse a name already in use, as AWS does (DuplicatePolicyException).
		dup := &orgv1.Policy{
			ObjectMeta: metav1.ObjectMeta{Name: "dup", Namespace: "default"},
			Spec: orgv1.PolicySpec{
				Name:        aws.String("no-leaving"),
				Description: aws.String("same name as an existing policy"),
				Type:        aws.String("SERVICE_CONTROL_POLICY"),
				Content:     aws.String(denyLeaveOrg),
			},
		}
		if err := env.Client.Create(ctx, dup); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "terminal condition", func() error {
			got := &orgv1.Policy{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(dup), got); err != nil {
				return err
			}
			for _, c := range got.Status.Conditions {
				if c.Type == ackv1alpha1.ConditionTypeTerminal && c.Status == "True" {
					return nil
				}
			}
			return fmt.Errorf("no terminal condition: %+v", got.Status.Conditions)
		})
		if err := env.Client.Delete(ctx, dup); err != nil {
			t.Fatal(err)
		}
		gone(t, dup)
	})

	t.Run("a tag policy is another type of the same kind", func(t *testing.T) {
		tag := &orgv1.Policy{
			ObjectMeta: metav1.ObjectMeta{Name: "costcenter", Namespace: "default"},
			Spec: orgv1.PolicySpec{
				Name:        aws.String("costcenter"),
				Description: aws.String("Tag keys"),
				Type:        aws.String("TAG_POLICY"),
				Content:     aws.String(`{"tags":{"CostCenter":{"tag_key":{"@@assign":"CostCenter"}}}}`),
			},
		}
		if err := env.Client.Create(ctx, tag); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t, tag)
		p, err := cloudPolicy(*got.Status.ID)
		if err != nil || p.PolicySummary.Type != orgtypes.PolicyTypeTagPolicy {
			t.Fatalf("cloud policy %+v: %v", p, err)
		}
		if err := env.Client.Delete(ctx, tag); err != nil {
			t.Fatal(err)
		}
		gone(t, tag)
	})

	t.Run("deletion-policy retain leaves the policy", func(t *testing.T) {
		keep := &orgv1.Policy{
			ObjectMeta: metav1.ObjectMeta{
				Name: "kept", Namespace: "default",
				Annotations: map[string]string{ackv1alpha1.AnnotationDeletionPolicy: string(ackv1alpha1.DeletionPolicyRetain)},
			},
			Spec: orgv1.PolicySpec{
				Name:        aws.String("kept"),
				Description: aws.String("Stays"),
				Type:        aws.String("SERVICE_CONTROL_POLICY"),
				Content:     aws.String(denyDeleteBucket),
			},
		}
		if err := env.Client.Create(ctx, keep); err != nil {
			t.Fatal(err)
		}
		keptID := *waitSynced(t, keep).Status.ID
		if err := env.Client.Delete(ctx, keep); err != nil {
			t.Fatal(err)
		}
		gone(t, keep)
		if _, err := cloudPolicy(keptID); err != nil {
			t.Fatalf("retained policy: %v", err)
		}
	})

	t.Run("delete deletes the policy", func(t *testing.T) {
		if err := env.Client.Delete(ctx, scp); err != nil {
			t.Fatal(err)
		}
		gone(t, scp)
		_, err := cloudPolicy(id)
		var nf *orgtypes.PolicyNotFoundException
		if !errors.As(err, &nf) {
			t.Fatalf("policy still there, or another error: %v", err)
		}
	})
}
