// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

// What main.go (generated) cannot know: the kinds in other groups that
// AutoScalingGroup's cross-service references name.

import (
	ackec2apitypes "github.com/aws-controllers-k8s/ec2-controller/apis/v1alpha1"
	ackelbv2apitypes "github.com/aws-controllers-k8s/elbv2-controller/apis/v1alpha1"
	ackiamapitypes "github.com/aws-controllers-k8s/iam-controller/apis/v1alpha1"

	hfl_ec2 "github.com/highfivelabs/ack-extensions/services/ec2/apis/v1alpha1"
)

func init() {
	// launchTemplateRef names ack-extensions's ec2 LaunchTemplate (ec2.aws.hfl.dev).
	_ = hfl_ec2.AddToScheme(scheme)
	// instanceRef and capacityReservationRefs name ACK's ec2 Instance and CapacityReservation.
	_ = ackec2apitypes.AddToScheme(scheme)
	// targetGroupARNRefs names ACK's elbv2 TargetGroup.
	_ = ackelbv2apitypes.AddToScheme(scheme)
	// serviceLinkedRoleARNRef and lifecycleHookSpecificationList[].roleARNRef name ACK's iam roles.
	_ = ackiamapitypes.AddToScheme(scheme)
}
