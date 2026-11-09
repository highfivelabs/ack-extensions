// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package scheduled_action

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/autoscaling"
	astypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/autoscaling/apis/v1alpha1"
)

var start = time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)

// atTime sets the package clock for one test.
func atTime(t *testing.T, at time.Time) {
	t.Helper()
	old := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = old })
}

func oneTime(managed bool) *resource {
	ko := &svcapitypes.ScheduledAction{}
	ko.Spec.AutoScalingGroupName = aws.String("web")
	ko.Spec.ScheduledActionName = aws.String("launch")
	ko.Spec.StartTime = &metav1.Time{Time: start}
	ko.Spec.DesiredCapacity = aws.Int64(2)
	if managed {
		ko.Finalizers = []string{FinalizerString}
	}
	return &resource{ko}
}

func listed(names ...string) *svcsdk.DescribeScheduledActionsOutput {
	out := &svcsdk.DescribeScheduledActionsOutput{}
	for _, n := range names {
		out.ScheduledUpdateGroupActions = append(out.ScheduledUpdateGroupActions, astypes.ScheduledUpdateGroupAction{ScheduledActionName: aws.String(n)})
	}
	return out
}

func TestCompletedOneTimeAction(t *testing.T) {
	rm := &resourceManager{awsRegion: "us-west-2", awsAccountID: "123456789012", awsPartition: "aws"}
	recurring := oneTime(true)
	recurring.ko.Spec.Recurrence = aws.String("0 6 * * *")
	for _, c := range []struct {
		name string
		at   time.Time
		r    *resource
		resp *svcsdk.DescribeScheduledActionsOutput
		want bool
	}{
		{"ran and gone", start.Add(time.Minute), oneTime(true), listed(), true},
		{"gone at its very start time", start, oneTime(true), listed(), true},
		{"not yet due: gone means put it", start.Add(-time.Minute), oneTime(true), listed(), false},
		{"still listed after its start time", start.Add(time.Minute), oneTime(true), listed("launch"), false},
		{"another action listed", start.Add(time.Minute), oneTime(true), listed("other"), true},
		{"never created by this object (no finalizer)", start.Add(time.Minute), oneTime(false), listed(), false},
		{"recurring", start.Add(time.Minute), recurring, listed(), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			atTime(t, c.at)
			done := rm.completedOneTimeAction(c.r, c.resp)
			if (done != nil) != c.want {
				t.Fatalf("completed %v, want %v", done != nil, c.want)
			}
			if done == nil {
				return
			}
			if !completed(done) {
				t.Fatal("no Completed condition on a completed action")
			}
			if completed(c.r) {
				t.Fatal("the condition was set on the object read, not on a copy")
			}
			// Completed reads as found with the spec as written, so the
			// runtime sees no drift and puts nothing.
			if d := newResourceDelta(c.r, done); len(d.Differences) != 0 {
				t.Fatalf("a completed action differs from its spec: %v", d.Differences)
			}
		})
	}
}

// Deleting a completed action calls nothing: EC2 Auto Scaling already
// deleted it. (The manager has no client here; a call would panic.)
func TestDeleteCompletedCallsNothing(t *testing.T) {
	rm := &resourceManager{awsRegion: "us-west-2", awsAccountID: "123456789012", awsPartition: "aws"}
	atTime(t, start.Add(time.Hour))
	done := rm.completedOneTimeAction(oneTime(true), listed())
	if done == nil {
		t.Fatal("not completed")
	}
	if _, err := rm.sdkDelete(context.Background(), done); err != nil {
		t.Fatal(err)
	}
}
