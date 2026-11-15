// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package bucket

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/s3/apis/v1alpha1"
)

// The one fix our copy of ACK's Bucket carries (docs/plan.md section 6.38
// and section 8, item 15): S3 answers GetBucketEncryption with
// ServerSideEncryptionConfigurationNotFoundError for a bucket with no
// default encryption, the SDK returns a nil output with it, and
// s3-controller v1.15.0 dereferenced that output (a nil pointer panic on
// every read). The read now ends with no encryption in the spec.
func Test_addPutFieldsToSpec_EncryptionNotFound(t *testing.T) {
	rm := &resourceManager{
		sdkapi: newMockedSDKClient(map[string]opResult{
			"GetBucketEncryption": {err: apiErr("ServerSideEncryptionConfigurationNotFoundError")},
		}),
	}
	desired := newBucketResource("no-default-encryption")
	desired.ko.Spec.Encryption = &svcapitypes.ServerSideEncryptionConfiguration{}
	ko := desired.ko.DeepCopy()

	require.NotPanics(t, func() {
		require.NoError(t, rm.addPutFieldsToSpec(context.Background(), desired, ko))
	})
	assert.Nil(t, ko.Spec.Encryption)
}

// Any other GetBucketEncryption error is still returned, as upstream does.
func Test_addPutFieldsToSpec_EncryptionOtherError(t *testing.T) {
	rm := &resourceManager{
		sdkapi: newMockedSDKClient(map[string]opResult{
			"GetBucketEncryption": {err: apiErr("AccessDenied")},
		}),
	}
	desired := newBucketResource("denied")
	ko := desired.ko.DeepCopy()
	require.Error(t, rm.addPutFieldsToSpec(context.Background(), desired, ko))
}
