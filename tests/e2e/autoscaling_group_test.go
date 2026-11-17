// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	astypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	asv1 "github.com/highfivelabs/ack-extensions/services/autoscaling/apis/v1alpha1"
	ec2v1 "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
)

// TestAutoScalingGroup tests autoscaling.aws.hfl.dev AutoScalingGroup (our
// fixed copy of ACK's AutoScalingGroup): launchTemplateRef ordering and
// resolution against ec2.aws.hfl.dev LaunchTemplate, spec.context, per-tag
// propagateAtLaunch, update, and delete.
func TestAutoScalingGroup(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = asv1.AddToScheme(scheme)
	_ = ec2v1.AddToScheme(scheme)
	env := harness.Start(t, "autoscaling", scheme)
	cloud := autoscaling.NewFromConfig(env.AWS)
	ec2Cloud := ec2.NewFromConfig(env.AWS)
	ctx := context.Background()

	images, err := ec2Cloud.DescribeImages(ctx, &ec2.DescribeImagesInput{Owners: []string{"amazon"}})
	if err != nil || len(images.Images) == 0 {
		t.Fatalf("no image in the fake cloud: %v", err)
	}

	ltAWSName := "ackx-asg-lt"
	ltOut, err := ec2Cloud.CreateLaunchTemplate(ctx, &ec2.CreateLaunchTemplateInput{
		LaunchTemplateName: aws.String(ltAWSName),
		LaunchTemplateData: &ec2types.RequestLaunchTemplateData{
			ImageId:      images.Images[0].ImageId,
			InstanceType: ec2types.InstanceTypeT3Micro,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cleanupLog(t, "launch template "+ltAWSName, func() error {
		_, err := ec2Cloud.DeleteLaunchTemplate(ctx, &ec2.DeleteLaunchTemplateInput{LaunchTemplateName: aws.String(ltAWSName)})
		return err
	})

	asgName := "ackx-asg-ref"
	describeGroup := func() (*astypes.AutoScalingGroup, error) {
		out, err := cloud.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
			AutoScalingGroupNames: []string{asgName},
		})
		if err != nil {
			return nil, err
		}
		if len(out.AutoScalingGroups) == 0 {
			return nil, fmt.Errorf("auto scaling group %s not found", asgName)
		}
		return &out.AutoScalingGroups[0], nil
	}

	// Note: spec.context is not set here because moto ignores Context on
	// CreateAutoScalingGroup/UpdateAutoScalingGroup and never returns it in
	// DescribeAutoScalingGroups; payload wiring is covered by TestContextInPayloads.
	asg := &asv1.AutoScalingGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "web-asg", Namespace: "default"},
		Spec: asv1.AutoScalingGroupSpec{
			Name:              aws.String(asgName),
			MinSize:           aws.Int64(0),
			MaxSize:           aws.Int64(2),
			AvailabilityZones: []*string{aws.String(harness.Region + "a")},
			LaunchTemplate: &asv1.LaunchTemplateSpecification{
				LaunchTemplateRef: &ackv1alpha1.AWSResourceReferenceWrapper{
					From: &ackv1alpha1.AWSResourceReference{Name: aws.String("web-lt")},
				},
				Version: aws.String("$Latest"),
			},
			Tags: []*asv1.Tag{
				{Key: aws.String("env"), Value: aws.String("test"), PropagateAtLaunch: aws.Bool(true)},
				{Key: aws.String("team"), Value: aws.String("platform")},
			},
			EnabledMetrics: []*asv1.EnabledMetric{
				{Metric: aws.String("GroupMinSize")},
				{Metric: aws.String("GroupMaxSize"), Granularity: aws.String("1Minute")},
			},
			SuspendedProcesses: []*asv1.SuspendedProcess{
				{ProcessName: aws.String("Launch")},
				{ProcessName: aws.String("Terminate")},
			},
		},
	}

	t.Run("nothing is made before the launch template syncs", func(t *testing.T) {
		if err := env.Client.Create(ctx, asg); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Second)
		if _, err := describeGroup(); err == nil {
			t.Fatalf("auto scaling group %s made before LaunchTemplate synced", asgName)
		}
	})

	t.Run("the referenced launch template resolves and group syncs", func(t *testing.T) {
		ltObj := &ec2v1.LaunchTemplate{
			ObjectMeta: metav1.ObjectMeta{Name: "web-lt", Namespace: "default"},
			Spec: ec2v1.LaunchTemplateSpec{
				Name: aws.String(ltAWSName),
				Data: &ec2v1.RequestLaunchTemplateData{
					InstanceType: aws.String("t3.micro"),
				},
			},
		}
		ackEC2Synced(t, ctx, env.Client, ltObj, &ltObj.Status.Conditions, func() {
			ltObj.Status.ID = ltOut.LaunchTemplate.LaunchTemplateId
		})

		got := &asv1.AutoScalingGroup{}
		harness.Eventually(t, 60*time.Second, "auto scaling group synced", func() error {
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(asg), got); err != nil {
				return err
			}
			if ok, why := synced(got.Status.Conditions); !ok {
				return fmt.Errorf("not synced: %s", why)
			}
			return nil
		})

		grp, err := describeGroup()
		if err != nil {
			t.Fatal(err)
		}
		if grp.LaunchTemplate == nil || aws.ToString(grp.LaunchTemplate.LaunchTemplateName) != ltAWSName {
			t.Fatalf("group launch template = %+v, want %s", grp.LaunchTemplate, ltAWSName)
		}
		tagPropagate := map[string]bool{}
		for _, tg := range grp.Tags {
			tagPropagate[aws.ToString(tg.Key)] = aws.ToBool(tg.PropagateAtLaunch)
		}
		if !tagPropagate["env"] {
			t.Fatalf("expected tag env PropagateAtLaunch=true, got %+v", grp.Tags)
		}
		if tagPropagate["team"] {
			t.Fatalf("expected tag team PropagateAtLaunch=false, got %+v", grp.Tags)
		}

		if got.Spec.LaunchTemplate == nil || got.Spec.LaunchTemplate.LaunchTemplateRef == nil {
			t.Fatalf("stored spec lost launchTemplateRef: %+v", got.Spec.LaunchTemplate)
		}
		if got.Spec.LaunchTemplate.LaunchTemplateName != nil {
			t.Errorf("stored spec holds resolved launchTemplateName %q; should keep reference only", aws.ToString(got.Spec.LaunchTemplate.LaunchTemplateName))
		}

		enabledSet := map[string]bool{}
		for _, m := range grp.EnabledMetrics {
			enabledSet[aws.ToString(m.Metric)] = true
		}
		if !enabledSet["GroupMinSize"] || !enabledSet["GroupMaxSize"] {
			t.Fatalf("expected GroupMinSize and GroupMaxSize enabled, got %+v", grp.EnabledMetrics)
		}
		suspendedSet := map[string]bool{}
		for _, p := range grp.SuspendedProcesses {
			suspendedSet[aws.ToString(p.ProcessName)] = true
		}
		if !suspendedSet["Launch"] || !suspendedSet["Terminate"] {
			t.Fatalf("expected Launch and Terminate suspended, got %+v", grp.SuspendedProcesses)
		}
	})

	t.Run("update maxSize, tag propagateAtLaunch, and suspendedProcesses", func(t *testing.T) {
		got := &asv1.AutoScalingGroup{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(asg), got); err != nil {
			t.Fatal(err)
		}
		patch := client.MergeFrom(got.DeepCopy())
		got.Spec.MaxSize = aws.Int64(3)
		for _, tg := range got.Spec.Tags {
			if aws.ToString(tg.Key) == "team" {
				tg.PropagateAtLaunch = aws.Bool(true)
			}
		}
		got.Spec.SuspendedProcesses = []*asv1.SuspendedProcess{
			{ProcessName: aws.String("Terminate")},
			{ProcessName: aws.String("HealthCheck")},
		}
		if err := env.Client.Patch(ctx, got, patch); err != nil {
			t.Fatal(err)
		}

		harness.Eventually(t, 60*time.Second, "group updated in AWS", func() error {
			grp, err := describeGroup()
			if err != nil {
				return err
			}
			if aws.ToInt32(grp.MaxSize) != 3 {
				return fmt.Errorf("maxSize = %d, want 3", aws.ToInt32(grp.MaxSize))
			}
			for _, tg := range grp.Tags {
				if aws.ToString(tg.Key) == "team" && !aws.ToBool(tg.PropagateAtLaunch) {
					return fmt.Errorf("tag team PropagateAtLaunch still false (tags=%+v)", grp.Tags)
				}
			}
			suspendedSet := map[string]bool{}
			for _, p := range grp.SuspendedProcesses {
				suspendedSet[aws.ToString(p.ProcessName)] = true
			}
			if suspendedSet["Launch"] || !suspendedSet["Terminate"] || !suspendedSet["HealthCheck"] {
				return fmt.Errorf("suspended processes = %+v, want Terminate and HealthCheck only", grp.SuspendedProcesses)
			}
			return nil
		})
	})

	t.Run("lifecycle hook attached via autoScalingGroupRef", func(t *testing.T) {
		hookName := "ackx-launch-hook"
		hook := &asv1.LifecycleHook{
			ObjectMeta: metav1.ObjectMeta{Name: "launch-hook", Namespace: "default"},
			Spec: asv1.LifecycleHookSpec{
				LifecycleHookName: aws.String(hookName),
				AutoScalingGroupRef: &ackv1alpha1.AWSResourceReferenceWrapper{
					From: &ackv1alpha1.AWSResourceReference{Name: aws.String("web-asg")},
				},
				LifecycleTransition: aws.String("autoscaling:EC2_INSTANCE_LAUNCHING"),
				HeartbeatTimeout:    aws.Int64(120),
			},
		}
		if err := env.Client.Create(ctx, hook); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "lifecycle hook synced", func() error {
			got := &asv1.LifecycleHook{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(hook), got); err != nil {
				return err
			}
			if ok, why := synced(got.Status.Conditions); !ok {
				return fmt.Errorf("not synced: %s", why)
			}
			out, err := cloud.DescribeLifecycleHooks(ctx, &autoscaling.DescribeLifecycleHooksInput{
				AutoScalingGroupName: aws.String(asgName),
				LifecycleHookNames:   []string{hookName},
			})
			if err != nil {
				return err
			}
			if len(out.LifecycleHooks) != 1 || aws.ToInt32(out.LifecycleHooks[0].HeartbeatTimeout) != 120 {
				return fmt.Errorf("unexpected hooks: %+v", out.LifecycleHooks)
			}
			return nil
		})

		if err := env.Client.Delete(ctx, hook); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "lifecycle hook deleted", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(hook), &asv1.LifecycleHook{})
			if err == nil {
				return fmt.Errorf("hook object still present")
			}
			return client.IgnoreNotFound(err)
		})
	})

	t.Run("delete removes the group", func(t *testing.T) {
		if err := env.Client.Delete(ctx, asg); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "auto scaling group gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(asg), &asv1.AutoScalingGroup{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			if err := client.IgnoreNotFound(err); err != nil {
				return err
			}
			if _, err := describeGroup(); err == nil {
				return fmt.Errorf("group %s still in AWS", asgName)
			}
			return nil
		})
	})
}
