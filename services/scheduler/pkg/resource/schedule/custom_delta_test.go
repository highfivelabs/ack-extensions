// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package schedule

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/scheduler/apis/v1alpha1"
)

func schedule(defaults bool) *resource {
	ko := &svcapitypes.Schedule{}
	ko.Spec.Name = aws.String("nightly-export")
	ko.Spec.ScheduleExpression = aws.String("cron(0 2 * * ? *)")
	ko.Spec.FlexibleTimeWindow = &svcapitypes.FlexibleTimeWindow{Mode: aws.String("OFF")}
	ko.Spec.Target = &svcapitypes.Target{ARN: aws.String("arn:aws:sqs:us-west-2:123456789012:jobs"), RoleARN: aws.String("arn:aws:iam::123456789012:role/scheduler")}
	if defaults {
		ko.Spec.GroupName = aws.String("default")
		ko.Spec.ScheduleExpressionTimezone = aws.String("UTC")
		ko.Spec.State = aws.String("ENABLED")
		ko.Spec.ActionAfterCompletion = aws.String("NONE")
		ko.Spec.Target.RetryPolicy = &svcapitypes.RetryPolicy{MaximumEventAgeInSeconds: aws.Int64(86400), MaximumRetryAttempts: aws.Int64(185)}
	}
	return &resource{ko}
}

// Scheduler's defaults, read back for a spec that leaves them out, are no
// drift.
func TestDefaultsAreNoDrift(t *testing.T) {
	if d := newResourceDelta(schedule(false), schedule(true)); len(d.Differences) != 0 {
		t.Fatalf("defaults read as drift at %v", d.Differences)
	}
}

// A value the spec sets is still compared.
func TestSetStateIsCompared(t *testing.T) {
	desired := schedule(false)
	desired.ko.Spec.State = aws.String("DISABLED")
	if d := newResourceDelta(desired, schedule(true)); !d.DifferentAt("Spec.State") {
		t.Fatal("a changed state is no drift")
	}
}
