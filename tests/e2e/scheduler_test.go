// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedtypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	schedv1 "github.com/highfivelabs/ack-extensions/services/scheduler/apis/v1alpha1"
)

// groupTags is a schedule group's tags in the cloud, as sorted k=v pairs,
// without the services.k8s.aws/ tags ACK's runtime adds to every resource.
func groupTags(ctx context.Context, c *scheduler.Client, arn string) (string, error) {
	out, err := c.ListTagsForResource(ctx, &scheduler.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
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

// ScheduleGroup: an EventBridge Scheduler schedule group. ACK has no
// Scheduler controller; the manager runs beside EventBridge's (open
// question 8).
func TestScheduleGroup(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = schedv1.AddToScheme(scheme)
	env := harness.Start(t, "scheduler", scheme)
	cloud := scheduler.NewFromConfig(env.AWS)
	ctx := context.Background()

	waitSynced := func(t *testing.T, obj *schedv1.ScheduleGroup) *schedv1.ScheduleGroup {
		t.Helper()
		got := &schedv1.ScheduleGroup{}
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
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &schedv1.ScheduleGroup{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}
	tagsAre := func(t *testing.T, arn, want string) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, "tags "+want, func() error {
			got, err := groupTags(ctx, cloud, arn)
			if err != nil {
				return err
			}
			if got != want {
				return fmt.Errorf("tags %q", got)
			}
			return nil
		})
	}

	nightly := &schedv1.ScheduleGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "default"},
		Spec: schedv1.ScheduleGroupSpec{
			Name: aws.String("nightly"),
			Tags: []*schedv1.Tag{{Key: aws.String("team"), Value: aws.String("data")}},
		},
	}
	var arn string

	t.Run("a group is made, tagged, and settles", func(t *testing.T) {
		if err := env.Client.Create(ctx, nightly); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t, nightly)
		arn = string(*got.Status.ACKResourceMetadata.ARN)
		out, err := cloud.GetScheduleGroup(ctx, &scheduler.GetScheduleGroupInput{Name: aws.String("nightly")})
		if err != nil || aws.ToString(out.Arn) != arn || out.State != schedtypes.ScheduleGroupStateActive {
			t.Fatalf("cloud group %+v: %v", out, err)
		}
		tagsAre(t, arn, "team=data")
		updates := func() int {
			n := 0
			for _, line := range strings.Split(env.Logs(), "\n") {
				if strings.Contains(line, ">>> rm.customUpdate") && strings.Contains(line, `"name": "nightly"`) {
					n++
				}
			}
			return n
		}
		before := updates()
		time.Sleep(15 * time.Second)
		if n := updates(); n != before {
			t.Fatalf("updated %d times across resyncs", n-before)
		}
	})

	t.Run("its tags are synced", func(t *testing.T) {
		harness.Eventually(t, 30*time.Second, "spec updated", func() error {
			got := &schedv1.ScheduleGroup{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(nightly), got); err != nil {
				return err
			}
			got.Spec.Tags = []*schedv1.Tag{{Key: aws.String("env"), Value: aws.String("prod")}}
			return env.Client.Update(ctx, got)
		})
		tagsAre(t, arn, "env=prod")
	})

	t.Run("the name cannot change", func(t *testing.T) {
		got := &schedv1.ScheduleGroup{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(nightly), got); err != nil {
			t.Fatal(err)
		}
		got.Spec.Name = aws.String("weekly")
		err := env.Client.Update(ctx, got)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("changing name: got %v, want the CRD's immutability refusal", err)
		}
	})

	t.Run("deletion-policy retain leaves the group", func(t *testing.T) {
		keep := &schedv1.ScheduleGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name: "reports", Namespace: "default",
				Annotations: map[string]string{ackv1alpha1.AnnotationDeletionPolicy: string(ackv1alpha1.DeletionPolicyRetain)},
			},
			Spec: schedv1.ScheduleGroupSpec{Name: aws.String("reports")},
		}
		if err := env.Client.Create(ctx, keep); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, keep)
		if err := env.Client.Delete(ctx, keep); err != nil {
			t.Fatal(err)
		}
		gone(t, keep)
		if _, err := cloud.GetScheduleGroup(ctx, &scheduler.GetScheduleGroupInput{Name: aws.String("reports")}); err != nil {
			t.Fatalf("retained group: %v", err)
		}
	})

	t.Run("delete deletes the group", func(t *testing.T) {
		if err := env.Client.Delete(ctx, nightly); err != nil {
			t.Fatal(err)
		}
		gone(t, nightly)
		_, err := cloud.GetScheduleGroup(ctx, &scheduler.GetScheduleGroupInput{Name: aws.String("nightly")})
		var nf *schedtypes.ResourceNotFoundException
		if !errors.As(err, &nf) {
			t.Fatalf("group still there, or another error: %v", err)
		}
	})
}
