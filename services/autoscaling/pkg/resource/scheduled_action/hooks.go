// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package scheduled_action

import (
	"fmt"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/autoscaling"
	corev1 "k8s.io/api/core/v1"
)

// ConditionTypeCompleted is set on a one-time action that has run: EC2
// Auto Scaling deletes a one-time scheduled action once it has run, so it
// is no longer there to read.
const ConditionTypeCompleted ackv1alpha1.ConditionType = "Completed"

// now is the clock completedOneTimeAction reads; tests replace it.
var now = time.Now

// completedOneTimeAction returns the resource as completed when it names
// a one-time action (a start time and no recurrence) whose start time has
// passed, that this object created (it carries ACK's finalizer), and that
// DescribeScheduledActions no longer reports: EC2 Auto Scaling ran it and
// deleted it. It returns nil otherwise, and the read goes on as generated:
// an action still reported is read as usual, and one never created, or
// recurring, or not yet due, is not found and is put.
//
// Completed reads as found, with the spec as written, so there is no
// drift and nothing is put again: the object is synced (Ready) and its
// Completed condition says it ran.
func (rm *resourceManager) completedOneTimeAction(r *resource, resp *svcsdk.DescribeScheduledActionsOutput) *resource {
	ko := r.ko
	if aws.ToString(ko.Spec.Recurrence) != "" || ko.Spec.StartTime == nil {
		return nil
	}
	if now().Before(ko.Spec.StartTime.Time) {
		return nil
	}
	if !containsFinalizer(ko, FinalizerString) {
		return nil
	}
	if resp != nil {
		for _, a := range resp.ScheduledUpdateGroupActions {
			if aws.ToString(a.ScheduledActionName) == aws.ToString(ko.Spec.ScheduledActionName) {
				return nil
			}
		}
	}
	done := ko.DeepCopy()
	rm.setStatusDefaults(done)
	msg := fmt.Sprintf("the one-time action ran at %s; EC2 Auto Scaling deleted it after it ran", ko.Spec.StartTime.UTC().Format(time.RFC3339))
	reason := "Ran"
	done.Status.Conditions = append(done.Status.Conditions, &ackv1alpha1.Condition{
		Type:    ConditionTypeCompleted,
		Status:  corev1.ConditionTrue,
		Reason:  &reason,
		Message: &msg,
	})
	return &resource{done}
}

// completed is true for a resource completedOneTimeAction marked: there
// is nothing left in EC2 Auto Scaling to delete.
func completed(r *resource) bool {
	for _, c := range r.ko.Status.Conditions {
		if c != nil && c.Type == ConditionTypeCompleted && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
