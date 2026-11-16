// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package tags

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/autoscaling/apis/v1alpha1"
)

func TestTagStateMap(t *testing.T) {
	tags := []*svcapitypes.Tag{
		{Key: aws.String("k1"), Value: aws.String("v1")},
		{Key: aws.String("k2"), Value: aws.String("v2"), PropagateAtLaunch: aws.Bool(true)},
		{Key: aws.String("k3"), Value: aws.String("v3"), PropagateAtLaunch: aws.Bool(false)},
		nil,
		{Value: aws.String("missing-key")},
	}

	got := tagStateMap(tags)
	assert.Equal(t, map[string]tagState{
		"k1": {value: "v1", propagateAtLaunch: false},
		"k2": {value: "v2", propagateAtLaunch: true},
		"k3": {value: "v3", propagateAtLaunch: false},
	}, got)
}
