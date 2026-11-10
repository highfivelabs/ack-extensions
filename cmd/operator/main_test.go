// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// A fake endpoint for the managers without the identity endpoint would
// send ACK's start-up STS call to real AWS.
func TestCheckEndpoints(t *testing.T) {
	for _, tc := range []struct {
		args []string
		env  []corev1.EnvVar
		ok   bool
	}{
		{nil, nil, true},
		{[]string{"--aws-endpoint-url=http://moto:5000"}, nil, false},
		{[]string{"--aws-endpoint-url=http://moto:5000", "--aws-identity-endpoint-url=http://moto:5000"}, nil, true},
		{[]string{"--aws-endpoint-url=http://moto:5000"}, []corev1.EnvVar{{Name: "AWS_ENDPOINT_URL", Value: "http://moto:5000"}}, true},
	} {
		if err := checkEndpoints(tc.args, tc.env); (err == nil) != tc.ok {
			t.Errorf("checkEndpoints(%v, %v) = %v", tc.args, tc.env, err)
		}
	}
}
