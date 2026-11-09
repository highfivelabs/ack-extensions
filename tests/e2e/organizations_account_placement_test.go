// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	ackorg "github.com/aws-controllers-k8s/organizations-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	orgv1 "github.com/highfivelabs/ack-extensions/services/organizations/apis/v1alpha1"
)

// parentOf is the root or OU an account is in, in the cloud.
func parentOf(ctx context.Context, c *organizations.Client, account string) (string, error) {
	out, err := c.ListParents(ctx, &organizations.ListParentsInput{ChildId: aws.String(account)})
	if err != nil {
		return "", err
	}
	if len(out.Parents) != 1 {
		return "", fmt.Errorf("account %s has %d parents", account, len(out.Parents))
	}
	return aws.ToString(out.Parents[0].Id), nil
}

// AccountPlacement: the OU (or root) an account is in, beside ACK's
// Account and OrganizationalUnit.
func TestOrganizationsAccountPlacement(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = orgv1.AddToScheme(scheme)
	_ = ackorg.AddToScheme(scheme)
	env := harness.Start(t, "organizations", scheme)
	cloud := organizations.NewFromConfig(env.AWS)
	ctx := context.Background()

	if _, err := cloud.CreateOrganization(ctx, &organizations.CreateOrganizationInput{FeatureSet: orgtypes.OrganizationFeatureSetAll}); err != nil {
		t.Fatal(err)
	}
	roots, err := cloud.ListRoots(ctx, &organizations.ListRootsInput{})
	if err != nil || len(roots.Roots) != 1 {
		t.Fatalf("roots: %+v %v", roots, err)
	}
	root := aws.ToString(roots.Roots[0].Id)
	newOU := func(name string) string {
		out, err := cloud.CreateOrganizationalUnit(ctx, &organizations.CreateOrganizationalUnitInput{ParentId: aws.String(root), Name: aws.String(name)})
		if err != nil {
			t.Fatal(err)
		}
		return aws.ToString(out.OrganizationalUnit.Id)
	}
	workloads, sandbox := newOU("workloads"), newOU("sandbox")
	app := newMemberAccount(t, ctx, cloud, "app")
	tools := newMemberAccount(t, ctx, cloud, "tools")

	// ACK's Account and OrganizationalUnit, as ACK's organizations-controller
	// would leave them once synced (that controller does not run here).
	syncedCond := []*ackv1alpha1.Condition{{Type: ackv1alpha1.ConditionTypeResourceSynced, Status: corev1.ConditionTrue}}
	ackAccount := &ackorg.Account{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec:       ackorg.AccountSpec{Name: aws.String("app"), Email: aws.String("app@example.com")},
	}
	if err := env.Client.Create(ctx, ackAccount); err != nil {
		t.Fatal(err)
	}
	ackAccount.Status.AccountID, ackAccount.Status.Conditions = aws.String(app), syncedCond
	if err := env.Client.Status().Update(ctx, ackAccount); err != nil {
		t.Fatal(err)
	}
	ackOU := &ackorg.OrganizationalUnit{
		ObjectMeta: metav1.ObjectMeta{Name: "workloads", Namespace: "default"},
		Spec:       ackorg.OrganizationalUnitSpec{Name: aws.String("workloads"), ParentID: aws.String(root)},
	}
	if err := env.Client.Create(ctx, ackOU); err != nil {
		t.Fatal(err)
	}
	ackOU.Status.ID, ackOU.Status.Conditions = aws.String(workloads), syncedCond
	if err := env.Client.Status().Update(ctx, ackOU); err != nil {
		t.Fatal(err)
	}

	waitSynced := func(t *testing.T, obj *orgv1.AccountPlacement) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.Name+" synced", func() error {
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
				return err
			}
			if ok, why := synced(obj.Status.Conditions); !ok {
				return fmt.Errorf("not synced: %s", why)
			}
			return nil
		})
	}
	parentIs := func(t *testing.T, account, want string) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, account+" in "+want, func() error {
			got, err := parentOf(ctx, cloud, account)
			if err != nil {
				return err
			}
			if got != want {
				return fmt.Errorf("in %s", got)
			}
			return nil
		})
	}
	gone := func(t *testing.T, obj client.Object) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.GetName()+" gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &orgv1.AccountPlacement{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}
	// update retries a change to the spec on a conflict with the controller.
	update := func(t *testing.T, obj *orgv1.AccountPlacement, change func(*orgv1.AccountPlacement)) {
		t.Helper()
		harness.Eventually(t, 30*time.Second, obj.Name+" updated", func() error {
			got := &orgv1.AccountPlacement{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
				return err
			}
			change(got)
			return env.Client.Update(ctx, got)
		})
	}
	logCount := func(name, what string) int {
		n := 0
		for _, line := range strings.Split(env.Logs(), "\n") {
			if strings.Contains(line, what) && strings.Contains(line, `"name": "`+name+`"`) {
				n++
			}
		}
		return n
	}

	byRef := &orgv1.AccountPlacement{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec: orgv1.AccountPlacementSpec{
			AccountRef: &ackv1alpha1.AWSResourceReferenceWrapper{From: &ackv1alpha1.AWSResourceReference{Name: aws.String("app")}},
			ParentRef:  &ackv1alpha1.AWSResourceReferenceWrapper{From: &ackv1alpha1.AWSResourceReference{Name: aws.String("workloads")}},
		},
	}

	t.Run("ACK's Account is moved into ACK's OU by reference, and settles", func(t *testing.T) {
		parentIs(t, app, root)
		if err := env.Client.Create(ctx, byRef); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, byRef)
		parentIs(t, app, workloads)
		desc, err := cloud.DescribeAccount(ctx, &organizations.DescribeAccountInput{AccountId: aws.String(app)})
		if err != nil {
			t.Fatal(err)
		}
		if md := byRef.Status.ACKResourceMetadata; md == nil || md.ARN == nil || string(*md.ARN) != aws.ToString(desc.Account.Arn) {
			t.Fatalf("status ARN %+v, account's %s", byRef.Status.ACKResourceMetadata, aws.ToString(desc.Account.Arn))
		}
		creates, updates := logCount("app", ">>> rm.sdkCreate"), logCount("app", ">>> rm.sdkUpdate")
		time.Sleep(15 * time.Second)
		if c, u := logCount("app", ">>> rm.sdkCreate"), logCount("app", ">>> rm.sdkUpdate"); c != creates || u != updates {
			t.Fatalf("across resyncs: %d more creates, %d more updates", c-creates, u-updates)
		}
	})

	t.Run("moved outside Kubernetes, it is moved back", func(t *testing.T) {
		if _, err := cloud.MoveAccount(ctx, &organizations.MoveAccountInput{AccountId: aws.String(app), SourceParentId: aws.String(workloads), DestinationParentId: aws.String(root)}); err != nil {
			t.Fatal(err)
		}
		parentIs(t, app, workloads)
	})

	t.Run("a new parent in the spec moves it", func(t *testing.T) {
		update(t, byRef, func(o *orgv1.AccountPlacement) {
			o.Spec.ParentRef = nil
			o.Spec.ParentID = aws.String(sandbox)
		})
		parentIs(t, app, sandbox)
		waitSynced(t, byRef)
	})

	byID := &orgv1.AccountPlacement{
		ObjectMeta: metav1.ObjectMeta{Name: "tools", Namespace: "default"},
		Spec:       orgv1.AccountPlacementSpec{AccountID: aws.String(tools), ParentID: aws.String(root)},
	}

	t.Run("an account already in its parent is placed without a move", func(t *testing.T) {
		if err := env.Client.Create(ctx, byID); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, byID)
		parentIs(t, tools, root)
		if md := byID.Status.ACKResourceMetadata; md == nil || md.ARN == nil || !strings.HasSuffix(string(*md.ARN), "/"+tools) {
			t.Fatalf("status ARN %+v", byID.Status.ACKResourceMetadata)
		}
	})

	t.Run("the account cannot change", func(t *testing.T) {
		harness.Eventually(t, 30*time.Second, "the CRD's immutability refusal", func() error {
			got := &orgv1.AccountPlacement{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(byID), got); err != nil {
				return err
			}
			got.Spec.AccountID = aws.String(app)
			err := env.Client.Update(ctx, got)
			if err != nil && strings.Contains(err.Error(), "immutable") {
				return nil
			}
			if err == nil {
				t.Fatal("accountID changed")
			}
			return err
		})
	})

	t.Run("delete leaves the account where it is", func(t *testing.T) {
		update(t, byID, func(o *orgv1.AccountPlacement) { o.Spec.ParentID = aws.String(workloads) })
		parentIs(t, tools, workloads)
		waitSynced(t, byID)
		for _, o := range []*orgv1.AccountPlacement{byRef, byID} {
			if err := env.Client.Delete(ctx, o); err != nil {
				t.Fatal(err)
			}
			gone(t, o)
		}
		time.Sleep(5 * time.Second)
		parentIs(t, app, sandbox)
		parentIs(t, tools, workloads)
	})
}
