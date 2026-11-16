// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package launch_template

// The ACK ec2 kinds LaunchTemplate.spec.data's references name
// (securityGroupRefs, networkInterfaces[].subnetRef), read through the
// manager's client; ACK's generator writes no RBAC marker for a reference
// to a kind of the same service (apis/v1alpha1/ack_kinds.go).

// +kubebuilder:rbac:groups=ec2.services.k8s.aws,resources=securitygroups;subnets,verbs=get;list;watch
