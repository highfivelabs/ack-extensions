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
	aas "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	aastypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	aasv1 "github.com/highfivelabs/ack-extensions/services/applicationautoscaling/apis/v1alpha1"
)

const ecsService = "service/app/web"

func cloudAppScheduledAction(ctx context.Context, c *aas.Client, name string) (*aastypes.ScheduledAction, error) {
	out, err := c.DescribeScheduledActions(ctx, &aas.DescribeScheduledActionsInput{
		ServiceNamespace: aastypes.ServiceNamespaceEcs, ResourceId: aws.String(ecsService),
		ScalableDimension: aastypes.ScalableDimensionECSServiceDesiredCount, ScheduledActionNames: []string{name},
	})
	if err != nil {
		return nil, err
	}
	for i := range out.ScheduledActions {
		if aws.ToString(out.ScheduledActions[i].ScheduledActionName) == name {
			return &out.ScheduledActions[i], nil
		}
	}
	return nil, nil
}

// ScheduledAction (Application Auto Scaling): a scheduled change of a
// scalable target's capacity, here an ECS service's desired count, put
// with PutScheduledAction. ACK's applicationautoscaling-controller
// (v1.9.0) has ScalableTarget and ScalingPolicy and ignores this
// resource.
func TestApplicationScheduledAction(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = aasv1.AddToScheme(scheme)
	env := harness.Start(t, "applicationautoscaling", scheme)
	cloud := aas.NewFromConfig(env.AWS)
	ctx := context.Background()

	// No scalable target is registered: moto does not require one for a
	// scheduled action (AWS does), and registering an ECS service's would
	// need the ECS service itself in moto.

	get := func(t *testing.T, obj *aasv1.ScheduledAction) *aasv1.ScheduledAction {
		t.Helper()
		got := &aasv1.ScheduledAction{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	waitSynced := func(t *testing.T, obj *aasv1.ScheduledAction) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.Name+" synced", func() error {
			got := &aasv1.ScheduledAction{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
				return err
			}
			if ok, why := synced(got.Status.Conditions); !ok {
				return fmt.Errorf("not synced: %s", why)
			}
			return nil
		})
	}
	gone := func(t *testing.T, obj client.Object) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.GetName()+" gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &aasv1.ScheduledAction{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}
	spec := func(name, schedule string, min, max int64) aasv1.ScheduledActionSpec {
		return aasv1.ScheduledActionSpec{
			ServiceNamespace: aws.String("ecs"), ResourceID: aws.String(ecsService),
			ScalableDimension: aws.String("ecs:service:DesiredCount"), ScheduledActionName: aws.String(name),
			Schedule:             aws.String(schedule),
			ScalableTargetAction: &aasv1.ScalableTargetAction{MinCapacity: aws.Int64(min), MaxCapacity: aws.Int64(max)},
		}
	}

	night := &aasv1.ScheduledAction{
		ObjectMeta: metav1.ObjectMeta{Name: "web-night", Namespace: "default"},
		Spec:       spec("web-night", "cron(0 20 * * ? *)", 1, 1),
	}

	t.Run("a recurring action is put and settles", func(t *testing.T) {
		if err := env.Client.Create(ctx, night); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, night)
		a, err := cloudAppScheduledAction(ctx, cloud, "web-night")
		if err != nil || a == nil {
			t.Fatalf("action in the cloud: %v %v", a, err)
		}
		if aws.ToString(a.Schedule) != "cron(0 20 * * ? *)" || aws.ToInt32(a.ScalableTargetAction.MaxCapacity) != 1 {
			t.Fatalf("cloud schedule %q max %d", aws.ToString(a.Schedule), aws.ToInt32(a.ScalableTargetAction.MaxCapacity))
		}
		// Over several resyncs (every 5 seconds here) it is not put again.
		updates := func() int {
			n := 0
			for _, line := range strings.Split(env.Logs(), "\n") {
				if strings.Contains(line, ">>> rm.sdkUpdate") && strings.Contains(line, `"name": "web-night"`) {
					n++
				}
			}
			return n
		}
		before := updates()
		time.Sleep(15 * time.Second)
		if n := updates(); n != before {
			t.Fatalf("put again %d times across resyncs", n-before)
		}
	})

	t.Run("a changed capacity is put again", func(t *testing.T) {
		obj := get(t, night)
		obj.Spec.ScalableTargetAction.MaxCapacity = aws.Int64(2)
		obj.Spec.Schedule = aws.String("cron(30 21 ? * MON-FRI *)")
		if err := env.Client.Update(ctx, obj); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "new action in the cloud", func() error {
			a, err := cloudAppScheduledAction(ctx, cloud, "web-night")
			if err != nil || a == nil {
				return fmt.Errorf("action %v: %v", a, err)
			}
			if aws.ToInt32(a.ScalableTargetAction.MaxCapacity) != 2 || aws.ToString(a.Schedule) != "cron(30 21 ? * MON-FRI *)" {
				return fmt.Errorf("max %d schedule %q", aws.ToInt32(a.ScalableTargetAction.MaxCapacity), aws.ToString(a.Schedule))
			}
			return nil
		})
		waitSynced(t, night)
	})

	t.Run("the target cannot change", func(t *testing.T) {
		obj := get(t, night)
		obj.Spec.ResourceID = aws.String("service/app/other")
		err := env.Client.Update(ctx, obj)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("changing resourceID: got %v, want the CRD's immutability refusal", err)
		}
	})

	t.Run("deletion-policy retain leaves the action", func(t *testing.T) {
		keep := &aasv1.ScheduledAction{
			ObjectMeta: metav1.ObjectMeta{
				Name: "web-morning", Namespace: "default",
				Annotations: map[string]string{ackv1alpha1.AnnotationDeletionPolicy: string(ackv1alpha1.DeletionPolicyRetain)},
			},
			Spec: spec("web-morning", "cron(0 7 * * ? *)", 2, 4),
		}
		if err := env.Client.Create(ctx, keep); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, keep)
		if err := env.Client.Delete(ctx, keep); err != nil {
			t.Fatal(err)
		}
		gone(t, keep)
		if a, err := cloudAppScheduledAction(ctx, cloud, "web-morning"); err != nil || a == nil {
			t.Fatalf("retained action gone from the cloud: %v", err)
		}
	})

	t.Run("delete removes the action", func(t *testing.T) {
		if err := env.Client.Delete(ctx, night); err != nil {
			t.Fatal(err)
		}
		gone(t, night)
		if a, err := cloudAppScheduledAction(ctx, cloud, "web-night"); err != nil || a != nil {
			t.Fatalf("action still in the cloud (%v)", err)
		}
	})
}
