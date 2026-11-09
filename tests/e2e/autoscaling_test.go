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

// ScalingPolicy: an EC2 Auto Scaling group's scaling policy, put with
// PutScalingPolicy. ACK's AutoScalingGroup (autoscaling-controller
// v1.5.0) has none, and a group without policies does not scale.
func TestScalingPolicy(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = asv1.AddToScheme(scheme)
	env := harness.Start(t, "autoscaling", scheme)
	cloud := autoscaling.NewFromConfig(env.AWS)
	ctx := context.Background()

	// The group the policies scale, made directly in the fake cloud (ACK's
	// AutoScalingGroup would make it on a real cluster).
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

	t.Run("server defaults are read into the spec and settle", func(t *testing.T) {
		if err := env.Client.Create(ctx, step); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, step)
		// PolicyType defaults to SimpleScaling; moto also returns a default
		// cooldown of 300 (AWS returns one only when it was set).
		harness.Eventually(t, 30*time.Second, "policyType late-initialized", func() error {
			got := get(t, step)
			if aws.ToString(got.Spec.PolicyType) != "SimpleScaling" {
				return fmt.Errorf("spec.policyType %q", aws.ToString(got.Spec.PolicyType))
			}
			return nil
		})
		// No drift is left: the object stays synced over several resyncs
		// (every 5 seconds here), with no recoverable error.
		before := get(t, step).Generation
		time.Sleep(15 * time.Second)
		got := get(t, step)
		if ok, why := synced(got.Status.Conditions); !ok {
			t.Fatalf("not synced after resyncs: %s", why)
		}
		for _, c := range got.Status.Conditions {
			if c.Type == ackv1alpha1.ConditionTypeRecoverable && c.Status == "True" {
				t.Fatalf("recoverable error: %s", aws.ToString(c.Message))
			}
		}
		if got.Generation != before {
			t.Fatalf("spec kept changing: generation %d -> %d", before, got.Generation)
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
