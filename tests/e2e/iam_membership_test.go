// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	ackiam "github.com/aws-controllers-k8s/iam-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	iamv1 "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

// groupsOf is the user's groups in the cloud, sorted and comma-joined.
func groupsOf(ctx context.Context, c *iam.Client, user string) (string, error) {
	out, err := c.ListGroupsForUser(ctx, &iam.ListGroupsForUserInput{UserName: aws.String(user)})
	if err != nil {
		return "", err
	}
	var gs []string
	for _, g := range out.Groups {
		gs = append(gs, aws.ToString(g.GroupName))
	}
	sort.Strings(gs)
	return strings.Join(gs, ","), nil
}

// UserGroupMembership: an IAM user in a list of groups, Terraform's
// aws_iam_user_group_membership. Not exclusive: groups the user is in for
// other reasons are left alone.
func TestUserGroupMembership(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = iamv1.AddToScheme(scheme)
	_ = ackiam.AddToScheme(scheme)
	env := harness.Start(t, "iam", scheme)
	cloud := iam.NewFromConfig(env.AWS)
	ctx := context.Background()

	for _, u := range []string{"alice", "bob"} {
		if _, err := cloud.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(u)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []string{"dev", "ops", "oncall", "audit"} {
		if _, err := cloud.CreateGroup(ctx, &iam.CreateGroupInput{GroupName: aws.String(g)}); err != nil {
			t.Fatal(err)
		}
	}
	// alice is in audit through something else, not this object.
	if _, err := cloud.AddUserToGroup(ctx, &iam.AddUserToGroupInput{UserName: aws.String("alice"), GroupName: aws.String("audit")}); err != nil {
		t.Fatal(err)
	}

	waitSynced := func(t *testing.T, obj *iamv1.UserGroupMembership) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.Name+" synced", func() error {
			got := &iamv1.UserGroupMembership{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
				return err
			}
			if ok, why := synced(got.Status.Conditions); !ok {
				return fmt.Errorf("not synced: %s", why)
			}
			return nil
		})
	}
	cloudGroups := func(t *testing.T, user, want string) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, user+" in "+want, func() error {
			got, err := groupsOf(ctx, cloud, user)
			if err != nil {
				return err
			}
			if got != want {
				return fmt.Errorf("groups %q", got)
			}
			return nil
		})
	}
	setGroups := func(t *testing.T, obj *iamv1.UserGroupMembership, groups ...string) {
		t.Helper()
		// The controller writes the object's status on every reconcile, so
		// an update can meet a newer version: read and try again.
		harness.Eventually(t, 30*time.Second, obj.Name+" groups updated", func() error {
			got := &iamv1.UserGroupMembership{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
				return err
			}
			got.Spec.Groups = nil
			for _, g := range groups {
				got.Spec.Groups = append(got.Spec.Groups, aws.String(g))
			}
			return env.Client.Update(ctx, got)
		})
	}
	gone := func(t *testing.T, obj client.Object) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.GetName()+" gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &iamv1.UserGroupMembership{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}

	alice := &iamv1.UserGroupMembership{
		ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: "default"},
		Spec:       iamv1.UserGroupMembershipSpec{UserName: aws.String("alice"), Groups: []*string{aws.String("dev"), aws.String("ops")}},
	}

	t.Run("the user is put in the listed groups and the others are left", func(t *testing.T) {
		if err := env.Client.Create(ctx, alice); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, alice)
		cloudGroups(t, "alice", "audit,dev,ops")
		updates := func() int {
			n := 0
			for _, line := range strings.Split(env.Logs(), "\n") {
				if strings.Contains(line, ">>> rm.sdkUpdate") && strings.Contains(line, `"name": "alice"`) {
					n++
				}
			}
			return n
		}
		before := updates()
		time.Sleep(15 * time.Second)
		if n := updates(); n != before {
			t.Fatalf("updated %d times across resyncs: a group not listed reads as drift", n-before)
		}
	})

	t.Run("a group taken off the list is removed, one added is added", func(t *testing.T) {
		setGroups(t, alice, "dev", "oncall")
		cloudGroups(t, "alice", "audit,dev,oncall")
	})

	t.Run("a removal outside Kubernetes is put back", func(t *testing.T) {
		if _, err := cloud.RemoveUserFromGroup(ctx, &iam.RemoveUserFromGroupInput{UserName: aws.String("alice"), GroupName: aws.String("dev")}); err != nil {
			t.Fatal(err)
		}
		cloudGroups(t, "alice", "audit,dev,oncall")
	})

	t.Run("the user cannot change", func(t *testing.T) {
		got := &iamv1.UserGroupMembership{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(alice), got); err != nil {
			t.Fatal(err)
		}
		got.Spec.UserName = aws.String("bob")
		err := env.Client.Update(ctx, got)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("changing userName: got %v, want the CRD's immutability refusal", err)
		}
	})

	t.Run("references resolve to ACK's User and Groups", func(t *testing.T) {
		// A User and Groups as ACK's iam-controller leaves them once synced;
		// the controller does not run here.
		synced := []*ackv1alpha1.Condition{{Type: ackv1alpha1.ConditionTypeResourceSynced, Status: corev1.ConditionTrue}}
		user := &ackiam.User{ObjectMeta: metav1.ObjectMeta{Name: "bob", Namespace: "default"}, Spec: ackiam.UserSpec{Name: aws.String("bob")}}
		if err := env.Client.Create(ctx, user); err != nil {
			t.Fatal(err)
		}
		user.Status.Conditions = synced
		if err := env.Client.Status().Update(ctx, user); err != nil {
			t.Fatal(err)
		}
		group := &ackiam.Group{ObjectMeta: metav1.ObjectMeta{Name: "ops", Namespace: "default"}, Spec: ackiam.GroupSpec{Name: aws.String("ops")}}
		if err := env.Client.Create(ctx, group); err != nil {
			t.Fatal(err)
		}
		group.Status.Conditions = synced
		if err := env.Client.Status().Update(ctx, group); err != nil {
			t.Fatal(err)
		}
		bob := &iamv1.UserGroupMembership{
			ObjectMeta: metav1.ObjectMeta{Name: "bob", Namespace: "default"},
			Spec: iamv1.UserGroupMembershipSpec{
				UserRef:   &ackv1alpha1.AWSResourceReferenceWrapper{From: &ackv1alpha1.AWSResourceReference{Name: aws.String("bob")}},
				GroupRefs: []*ackv1alpha1.AWSResourceReferenceWrapper{{From: &ackv1alpha1.AWSResourceReference{Name: aws.String("ops")}}},
			},
		}
		if err := env.Client.Create(ctx, bob); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, bob)
		cloudGroups(t, "bob", "ops")
	})

	t.Run("deletion-policy retain leaves the user in the groups", func(t *testing.T) {
		keep := &iamv1.UserGroupMembership{
			ObjectMeta: metav1.ObjectMeta{
				Name: "bob-dev", Namespace: "default",
				Annotations: map[string]string{ackv1alpha1.AnnotationDeletionPolicy: string(ackv1alpha1.DeletionPolicyRetain)},
			},
			Spec: iamv1.UserGroupMembershipSpec{UserName: aws.String("bob"), Groups: []*string{aws.String("dev")}},
		}
		if err := env.Client.Create(ctx, keep); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, keep)
		cloudGroups(t, "bob", "dev,ops")
		if err := env.Client.Delete(ctx, keep); err != nil {
			t.Fatal(err)
		}
		gone(t, keep)
		cloudGroups(t, "bob", "dev,ops")
	})

	t.Run("delete removes the listed groups and leaves the others", func(t *testing.T) {
		if err := env.Client.Delete(ctx, alice); err != nil {
			t.Fatal(err)
		}
		gone(t, alice)
		cloudGroups(t, "alice", "audit")
	})
}
