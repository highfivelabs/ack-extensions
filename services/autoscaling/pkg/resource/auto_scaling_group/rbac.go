// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package auto_scaling_group

// AutoScalingGroup's launchTemplateRef names ack-extensions's ec2
// LaunchTemplate (ec2.aws.hfl.dev); ACK's generator writes no RBAC marker for
// a reference to a kind aliased into the same service package
// (apis/v1alpha1/launch_template.go).

// +kubebuilder:rbac:groups=ec2.aws.hfl.dev,resources=launchtemplates,verbs=get;list;watch
