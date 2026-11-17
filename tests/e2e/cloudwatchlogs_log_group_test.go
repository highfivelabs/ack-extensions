// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	cwv1 "github.com/highfivelabs/ack-extensions/services/cloudwatchlogs/apis/v1alpha1"
)

// TestCloudWatchLogsLogGroup tests cloudwatchlogs.aws.hfl.dev LogGroup (our
// fixed copy of ACK's LogGroup with spec.logGroupClass): create, update,
// retain deletion policy, and delete against moto.
func TestCloudWatchLogsLogGroup(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = cwv1.AddToScheme(scheme)
	env := harness.Start(t, "cloudwatchlogs", scheme)
	cloud := cloudwatchlogs.NewFromConfig(env.AWS)
	ctx := context.Background()

	lgName := "/ackx/test-log-group"
	describe := func() (*cwtypes.LogGroup, error) {
		out, err := cloud.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
			LogGroupNamePrefix: aws.String(lgName),
		})
		if err != nil {
			return nil, err
		}
		for i := range out.LogGroups {
			if aws.ToString(out.LogGroups[i].LogGroupName) == lgName {
				return &out.LogGroups[i], nil
			}
		}
		return nil, fmt.Errorf("log group %s not found", lgName)
	}

	lg := &cwv1.LogGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "app-logs", Namespace: "default"},
		Spec: cwv1.LogGroupSpec{
			Name:          aws.String(lgName),
			LogGroupClass: aws.String("INFREQUENT_ACCESS"),
			RetentionDays: aws.Int64(14),
			Tags: map[string]*string{
				"env": aws.String("test"),
			},
		},
	}
	if err := env.Client.Create(ctx, lg); err != nil {
		t.Fatal(err)
	}
	cleanupLog(t, "log group "+lgName, func() error {
		_, err := cloud.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(lgName)})
		return err
	})

	harness.Eventually(t, 60*time.Second, "log group synced", func() error {
		var cur cwv1.LogGroup
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(lg), &cur); err != nil {
			return err
		}
		if ok, why := synced(cur.Status.Conditions); !ok {
			return fmt.Errorf("not synced: %s", why)
		}
		return nil
	})

	got, err := describe()
	if err != nil {
		t.Fatal(err)
	}
	if aws.ToInt32(got.RetentionInDays) != 14 {
		t.Fatalf("RetentionInDays = %d, want 14", aws.ToInt32(got.RetentionInDays))
	}
	if got.LogGroupClass != "" && string(got.LogGroupClass) != "INFREQUENT_ACCESS" {
		t.Fatalf("LogGroupClass = %q, want INFREQUENT_ACCESS", got.LogGroupClass)
	}

	// Update retention and tags.
	var cur cwv1.LogGroup
	if err := env.Client.Get(ctx, client.ObjectKeyFromObject(lg), &cur); err != nil {
		t.Fatal(err)
	}
	cur.Spec.RetentionDays = aws.Int64(30)
	cur.Spec.Tags["team"] = aws.String("platform")
	if err := env.Client.Update(ctx, &cur); err != nil {
		t.Fatal(err)
	}
	harness.Eventually(t, 60*time.Second, "log group retention updated", func() error {
		g, err := describe()
		if err != nil {
			return err
		}
		if aws.ToInt32(g.RetentionInDays) != 30 {
			return fmt.Errorf("RetentionInDays = %d, want 30", aws.ToInt32(g.RetentionInDays))
		}
		return nil
	})

	// Delete CR and confirm AWS log group is deleted.
	if err := env.Client.Delete(ctx, &cur); err != nil {
		t.Fatal(err)
	}
	harness.Eventually(t, 60*time.Second, "log group deleted", func() error {
		if _, err := describe(); err == nil {
			return fmt.Errorf("log group %s still exists", lgName)
		}
		return nil
	})

	// Retain deletion policy keeps the log group in AWS.
	retainedName := "/ackx/retained-log-group"
	retained := &cwv1.LogGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "retained-logs",
			Namespace:   "default",
			Annotations: map[string]string{"services.k8s.aws/deletion-policy": "retain"},
		},
		Spec: cwv1.LogGroupSpec{
			Name:          aws.String(retainedName),
			LogGroupClass: aws.String("STANDARD"),
		},
	}
	if err := env.Client.Create(ctx, retained); err != nil {
		t.Fatal(err)
	}
	cleanupLog(t, "log group "+retainedName, func() error {
		_, err := cloud.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(retainedName)})
		return err
	})
	harness.Eventually(t, 60*time.Second, "retained log group synced", func() error {
		var c cwv1.LogGroup
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(retained), &c); err != nil {
			return err
		}
		if ok, why := synced(c.Status.Conditions); !ok {
			return fmt.Errorf("not synced: %s", why)
		}
		return nil
	})
	if err := env.Client.Delete(ctx, retained); err != nil {
		t.Fatal(err)
	}
	harness.Eventually(t, 60*time.Second, "retained log group CR deleted", func() error {
		var c cwv1.LogGroup
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(retained), &c); err == nil {
			return fmt.Errorf("CR still present")
		}
		return nil
	})
	out, err := cloud.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: aws.String(retainedName)})
	if err != nil || len(out.LogGroups) == 0 {
		t.Fatalf("retained log group missing after CR delete: len=%d err=%v", len(out.LogGroups), err)
	}
}
