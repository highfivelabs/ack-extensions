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
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	astypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	asv1 "github.com/highfivelabs/ack-extensions/services/autoscaling/apis/v1alpha1"
)

// cloudPolicy is the group's policy of that name in the fake cloud, or nil.
func cloudPolicy(ctx context.Context, c *autoscaling.Client, group, name string) (*astypes.ScalingPolicy, error) {
	out, err := c.DescribePolicies(ctx, &autoscaling.DescribePoliciesInput{AutoScalingGroupName: aws.String(group), PolicyNames: []string{name}})
	if err != nil {
		return nil, err
	}
	for i := range out.ScalingPolicies {
		if aws.ToString(out.ScalingPolicies[i].PolicyName) == name {
			return &out.ScalingPolicies[i], nil
		}
	}
	return nil, nil
}

// asgFixture makes the group "web" directly in the fake cloud (ACK's
// AutoScalingGroup would make it on a real cluster).
func asgFixture(t *testing.T, ctx context.Context, env *harness.Env, cloud *autoscaling.Client) {
	t.Helper()
	images, err := ec2.NewFromConfig(env.AWS).DescribeImages(ctx, &ec2.DescribeImagesInput{Owners: []string{"amazon"}})
	if err != nil || len(images.Images) == 0 {
		t.Fatalf("no image in the fake cloud: %v", err)
	}
	if _, err := cloud.CreateLaunchConfiguration(ctx, &autoscaling.CreateLaunchConfigurationInput{
		LaunchConfigurationName: aws.String("web"), ImageId: images.Images[0].ImageId, InstanceType: aws.String("t3.micro"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cloud.CreateAutoScalingGroup(ctx, &autoscaling.CreateAutoScalingGroupInput{
		AutoScalingGroupName: aws.String("web"), LaunchConfigurationName: aws.String("web"),
		MinSize: aws.Int32(0), MaxSize: aws.Int32(2), AvailabilityZones: []string{"us-west-2a"},
	}); err != nil {
		t.Fatal(err)
	}
}

// ScalingPolicy: an EC2 Auto Scaling group's scaling policy, put with
// PutScalingPolicy. ACK's AutoScalingGroup (autoscaling-controller
// v1.5.0) has none, and a group without policies does not scale.
func TestScalingPolicy(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = asv1.AddToScheme(scheme)
	env := harness.Start(t, "autoscaling", scheme)
	cloud := autoscaling.NewFromConfig(env.AWS)
	ctx := context.Background()

	asgFixture(t, ctx, env, cloud)

	get := func(t *testing.T, obj *asv1.ScalingPolicy) *asv1.ScalingPolicy {
		t.Helper()
		got := &asv1.ScalingPolicy{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	waitSynced := func(t *testing.T, obj *asv1.ScalingPolicy) *asv1.ScalingPolicy {
		t.Helper()
		got := &asv1.ScalingPolicy{}
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
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &asv1.ScalingPolicy{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}

	cpu := &asv1.ScalingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "web-cpu", Namespace: "default"},
		Spec: asv1.ScalingPolicySpec{
			AutoScalingGroupName: aws.String("web"), PolicyName: aws.String("web-cpu"),
			PolicyType: aws.String("TargetTrackingScaling"),
			TargetTrackingConfiguration: &asv1.TargetTrackingConfiguration{
				PredefinedMetricSpecification: &asv1.PredefinedMetricSpecification{PredefinedMetricType: aws.String("ASGAverageCPUUtilization")},
				TargetValue:                   aws.Float64(50),
			},
		},
	}

	t.Run("a target-tracking policy is put", func(t *testing.T) {
		if err := env.Client.Create(ctx, cpu); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t, cpu)
		if !strings.Contains(string(*got.Status.ACKResourceMetadata.ARN), ":policyName/web-cpu") {
			t.Fatalf("status ARN %v", got.Status.ACKResourceMetadata.ARN)
		}
		p, err := cloudPolicy(ctx, cloud, "web", "web-cpu")
		if err != nil || p == nil {
			t.Fatalf("policy in the cloud: %v %v", p, err)
		}
		if p.TargetTrackingConfiguration == nil || aws.ToFloat64(p.TargetTrackingConfiguration.TargetValue) != 50 {
			t.Fatalf("cloud target tracking %+v", p.TargetTrackingConfiguration)
		}
	})

	t.Run("a changed target is put again", func(t *testing.T) {
		obj := get(t, cpu)
		obj.Spec.TargetTrackingConfiguration.TargetValue = aws.Float64(65)
		if err := env.Client.Update(ctx, obj); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "target 65 in the cloud", func() error {
			p, err := cloudPolicy(ctx, cloud, "web", "web-cpu")
			if err != nil || p == nil {
				return fmt.Errorf("policy %v: %v", p, err)
			}
			if v := aws.ToFloat64(p.TargetTrackingConfiguration.TargetValue); v != 65 {
				return fmt.Errorf("target %v", v)
			}
			return nil
		})
		waitSynced(t, cpu)
	})

	step := &asv1.ScalingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "web-up", Namespace: "default"},
		Spec: asv1.ScalingPolicySpec{
			AutoScalingGroupName: aws.String("web"), PolicyName: aws.String("web-up"),
			AdjustmentType: aws.String("ChangeInCapacity"), ScalingAdjustment: aws.Int64(1),
		},
	}

	t.Run("server defaults are no drift", func(t *testing.T) {
		if err := env.Client.Create(ctx, step); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, step)
		// The spec leaves the policy type and cooldown unset; the fake
		// reports SimpleScaling and a cooldown of 300 (AWS reports the
		// type, and a cooldown only when one was set). Over several
		// resyncs (every 5 seconds here) the policy is not put again (the
		// controller's log shows no update), and the object stays synced
		// with no recoverable error.
		updates := func() int {
			n := 0
			for _, line := range strings.Split(env.Logs(), "\n") {
				if strings.Contains(line, ">>> rm.sdkUpdate") && strings.Contains(line, `"name": "web-up"`) {
					n++
				}
			}
			return n
		}
		before := updates()
		time.Sleep(15 * time.Second)
		if n := updates(); n != before {
			t.Fatalf("the policy was put again %d times across resyncs: the server's defaults read as drift", n-before)
		}
		got := get(t, step)
		if ok, why := synced(got.Status.Conditions); !ok {
			t.Fatalf("not synced after resyncs: %s", why)
		}
		for _, c := range got.Status.Conditions {
			if c.Type == ackv1alpha1.ConditionTypeRecoverable && c.Status == "True" {
				t.Fatalf("recoverable error: %s", aws.ToString(c.Message))
			}
		}
		p, err := cloudPolicy(ctx, cloud, "web", "web-up")
		if err != nil || p == nil {
			t.Fatalf("policy in the cloud: %v %v", p, err)
		}
		if aws.ToString(p.PolicyType) != "SimpleScaling" || aws.ToInt32(p.ScalingAdjustment) != 1 {
			t.Fatalf("cloud policy type %q adjustment %d", aws.ToString(p.PolicyType), aws.ToInt32(p.ScalingAdjustment))
		}
	})

	t.Run("the policy name cannot change", func(t *testing.T) {
		obj := get(t, cpu)
		obj.Spec.PolicyName = aws.String("renamed")
		err := env.Client.Update(ctx, obj)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("changing policyName: got %v, want the CRD's immutability refusal", err)
		}
	})

	t.Run("deletion-policy retain leaves the policy", func(t *testing.T) {
		keep := &asv1.ScalingPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name: "web-down", Namespace: "default",
				Annotations: map[string]string{ackv1alpha1.AnnotationDeletionPolicy: string(ackv1alpha1.DeletionPolicyRetain)},
			},
			Spec: asv1.ScalingPolicySpec{
				AutoScalingGroupName: aws.String("web"), PolicyName: aws.String("web-down"),
				AdjustmentType: aws.String("ChangeInCapacity"), ScalingAdjustment: aws.Int64(-1),
			},
		}
		if err := env.Client.Create(ctx, keep); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, keep)
		if err := env.Client.Delete(ctx, keep); err != nil {
			t.Fatal(err)
		}
		gone(t, keep)
		if p, err := cloudPolicy(ctx, cloud, "web", "web-down"); err != nil || p == nil {
			t.Fatalf("retained policy gone from the cloud: %v", err)
		}
	})

	t.Run("delete removes the policy", func(t *testing.T) {
		for _, obj := range []*asv1.ScalingPolicy{cpu, step} {
			if err := env.Client.Delete(ctx, obj); err != nil {
				t.Fatal(err)
			}
			gone(t, obj)
			if p, err := cloudPolicy(ctx, cloud, "web", aws.ToString(obj.Spec.PolicyName)); err != nil || p != nil {
				t.Fatalf("%s still in the cloud (%v)", obj.Name, err)
			}
		}
	})
}

// cloudScheduledAction is the group's scheduled action of that name in the
// fake cloud, or nil.
func cloudScheduledAction(ctx context.Context, c *autoscaling.Client, group, name string) (*astypes.ScheduledUpdateGroupAction, error) {
	out, err := c.DescribeScheduledActions(ctx, &autoscaling.DescribeScheduledActionsInput{AutoScalingGroupName: aws.String(group), ScheduledActionNames: []string{name}})
	if err != nil {
		return nil, err
	}
	for i := range out.ScheduledUpdateGroupActions {
		if aws.ToString(out.ScheduledUpdateGroupActions[i].ScheduledActionName) == name {
			return &out.ScheduledUpdateGroupActions[i], nil
		}
	}
	return nil, nil
}

// ScheduledAction: a scheduled change of an EC2 Auto Scaling group's
// capacity, put with PutScheduledUpdateGroupAction. ACK's AutoScalingGroup
// (autoscaling-controller v1.5.0) has none.
//
// moto stores an action as given. AWS computes the next start time of a
// recurring action and deletes a one-time action after it runs; neither
// can be seen here.
func TestScheduledAction(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = asv1.AddToScheme(scheme)
	env := harness.Start(t, "autoscaling", scheme)
	cloud := autoscaling.NewFromConfig(env.AWS)
	ctx := context.Background()
	asgFixture(t, ctx, env, cloud)

	get := func(t *testing.T, obj *asv1.ScheduledAction) *asv1.ScheduledAction {
		t.Helper()
		got := &asv1.ScheduledAction{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	waitSynced := func(t *testing.T, obj *asv1.ScheduledAction) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.Name+" synced", func() error {
			got := &asv1.ScheduledAction{}
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
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &asv1.ScheduledAction{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}

	nightly := &asv1.ScheduledAction{
		ObjectMeta: metav1.ObjectMeta{Name: "web-nightly", Namespace: "default"},
		Spec: asv1.ScheduledActionSpec{
			AutoScalingGroupName: aws.String("web"), ScheduledActionName: aws.String("web-nightly"),
			Recurrence: aws.String("0 20 * * *"), TimeZone: aws.String("Europe/Oslo"),
			MinSize: aws.Int64(0), MaxSize: aws.Int64(0), DesiredCapacity: aws.Int64(0),
		},
	}

	t.Run("a recurring action is put and settles", func(t *testing.T) {
		if err := env.Client.Create(ctx, nightly); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, nightly)
		a, err := cloudScheduledAction(ctx, cloud, "web", "web-nightly")
		if err != nil || a == nil {
			t.Fatalf("action in the cloud: %v %v", a, err)
		}
		if aws.ToString(a.Recurrence) != "0 20 * * *" || aws.ToString(a.TimeZone) != "Europe/Oslo" || aws.ToInt32(a.MaxSize) != 0 {
			t.Fatalf("cloud action recurrence %q zone %q max %d", aws.ToString(a.Recurrence), aws.ToString(a.TimeZone), aws.ToInt32(a.MaxSize))
		}
	})

	t.Run("a changed schedule is put again", func(t *testing.T) {
		obj := get(t, nightly)
		obj.Spec.Recurrence = aws.String("30 21 * * 1-5")
		obj.Spec.DesiredCapacity = aws.Int64(1)
		obj.Spec.MaxSize = aws.Int64(1)
		if err := env.Client.Update(ctx, obj); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "new schedule in the cloud", func() error {
			a, err := cloudScheduledAction(ctx, cloud, "web", "web-nightly")
			if err != nil || a == nil {
				return fmt.Errorf("action %v: %v", a, err)
			}
			if aws.ToString(a.Recurrence) != "30 21 * * 1-5" || aws.ToInt32(a.DesiredCapacity) != 1 {
				return fmt.Errorf("recurrence %q desired %d", aws.ToString(a.Recurrence), aws.ToInt32(a.DesiredCapacity))
			}
			return nil
		})
		waitSynced(t, nightly)
	})

	once := &asv1.ScheduledAction{
		ObjectMeta: metav1.ObjectMeta{Name: "web-launch", Namespace: "default"},
		Spec: asv1.ScheduledActionSpec{
			AutoScalingGroupName: aws.String("web"), ScheduledActionName: aws.String("web-launch"),
			StartTime:       &metav1.Time{Time: time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)},
			DesiredCapacity: aws.Int64(2),
		},
	}

	t.Run("a one-time action is put with its start time", func(t *testing.T) {
		if err := env.Client.Create(ctx, once); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, once)
		a, err := cloudScheduledAction(ctx, cloud, "web", "web-launch")
		if err != nil || a == nil {
			t.Fatalf("action in the cloud: %v %v", a, err)
		}
		if a.StartTime == nil || !a.StartTime.Equal(once.Spec.StartTime.Time) {
			t.Fatalf("cloud start time %v, want %v", a.StartTime, once.Spec.StartTime.Time)
		}
	})

	t.Run("the action name cannot change", func(t *testing.T) {
		obj := get(t, nightly)
		obj.Spec.ScheduledActionName = aws.String("renamed")
		err := env.Client.Update(ctx, obj)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("changing scheduledActionName: got %v, want the CRD's immutability refusal", err)
		}
	})

	t.Run("deletion-policy retain leaves the action", func(t *testing.T) {
		keep := &asv1.ScheduledAction{
			ObjectMeta: metav1.ObjectMeta{
				Name: "web-morning", Namespace: "default",
				Annotations: map[string]string{ackv1alpha1.AnnotationDeletionPolicy: string(ackv1alpha1.DeletionPolicyRetain)},
			},
			Spec: asv1.ScheduledActionSpec{AutoScalingGroupName: aws.String("web"), ScheduledActionName: aws.String("web-morning"),
				Recurrence: aws.String("0 7 * * *"), DesiredCapacity: aws.Int64(1)},
		}
		if err := env.Client.Create(ctx, keep); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, keep)
		if err := env.Client.Delete(ctx, keep); err != nil {
			t.Fatal(err)
		}
		gone(t, keep)
		if a, err := cloudScheduledAction(ctx, cloud, "web", "web-morning"); err != nil || a == nil {
			t.Fatalf("retained action gone from the cloud: %v", err)
		}
	})

	t.Run("delete removes the actions", func(t *testing.T) {
		for _, obj := range []*asv1.ScheduledAction{nightly, once} {
			if err := env.Client.Delete(ctx, obj); err != nil {
				t.Fatal(err)
			}
			gone(t, obj)
			if a, err := cloudScheduledAction(ctx, cloud, "web", aws.ToString(obj.Spec.ScheduledActionName)); err != nil || a != nil {
				t.Fatalf("%s still in the cloud (%v)", obj.Name, err)
			}
		}
	})
}
