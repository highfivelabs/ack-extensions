// Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//     http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0
//
// Modified by High Five Labs for ack-extensions: copied from
// aws-controllers-k8s/kms-controller pull request #112 (head eb4057e4),
// pkg/resource/replica_key/hook.go, customFind's conversion of
// DescribeKey's KeyMetadata. Changes: a function of the SDK's KeyMetadata,
// so that ReplicateKey's ReplicaKeyMetadata converts the same way.

package replica_key

import (
	ackkmsapitypes "github.com/aws-controllers-k8s/kms-controller/apis/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// keyMetadataFromSDK converts KMS's KeyMetadata to ACK kms-controller's.
func keyMetadataFromSDK(in *svcsdktypes.KeyMetadata) *ackkmsapitypes.KeyMetadata {
	if in == nil {
		return nil
	}
	f0 := &ackkmsapitypes.KeyMetadata{}
	if in.AWSAccountId != nil {
		f0.AWSAccountID = in.AWSAccountId
	}
	if in.Arn != nil {
		f0.ARN = in.Arn
	}
	if in.CloudHsmClusterId != nil {
		f0.CloudHsmClusterID = in.CloudHsmClusterId
	}
	if in.CreationDate != nil {
		f0.CreationDate = &metav1.Time{*in.CreationDate}
	}
	if in.CustomKeyStoreId != nil {
		f0.CustomKeyStoreID = in.CustomKeyStoreId
	}
	if in.DeletionDate != nil {
		f0.DeletionDate = &metav1.Time{*in.DeletionDate}
	}
	if in.Description != nil {
		f0.Description = in.Description
	}
	f0.Enabled = &in.Enabled
	if in.EncryptionAlgorithms != nil {
		f0f8 := []*string{}
		for _, f0f8iter := range in.EncryptionAlgorithms {
			var f0f8elem *string
			f0f8elem = aws.String(string(f0f8iter))
			f0f8 = append(f0f8, f0f8elem)
		}
		f0.EncryptionAlgorithms = f0f8
	}
	if in.ExpirationModel != "" {
		f0.ExpirationModel = aws.String(string(in.ExpirationModel))
	}
	if in.KeyId != nil {
		f0.KeyID = in.KeyId
	}
	if in.KeyManager != "" {
		f0.KeyManager = aws.String(string(in.KeyManager))
	}
	if in.KeySpec != "" {
		f0.KeySpec = aws.String(string(in.KeySpec))
	}
	if in.KeyState != "" {
		f0.KeyState = aws.String(string(in.KeyState))
	}
	if in.KeyUsage != "" {
		f0.KeyUsage = aws.String(string(in.KeyUsage))
	}
	if in.MacAlgorithms != nil {
		f0f15 := []*string{}
		for _, f0f15iter := range in.MacAlgorithms {
			var f0f15elem *string
			f0f15elem = aws.String(string(f0f15iter))
			f0f15 = append(f0f15, f0f15elem)
		}
		f0.MacAlgorithms = f0f15
	}
	if in.MultiRegion != nil {
		f0.MultiRegion = in.MultiRegion
	}
	if in.MultiRegionConfiguration != nil {
		f0f17 := &ackkmsapitypes.MultiRegionConfiguration{}
		if in.MultiRegionConfiguration.MultiRegionKeyType != "" {
			f0f17.MultiRegionKeyType = aws.String(string(in.MultiRegionConfiguration.MultiRegionKeyType))
		}
		if in.MultiRegionConfiguration.PrimaryKey != nil {
			f0f17f1 := &ackkmsapitypes.MultiRegionKey{}
			if in.MultiRegionConfiguration.PrimaryKey.Arn != nil {
				f0f17f1.ARN = in.MultiRegionConfiguration.PrimaryKey.Arn
			}
			if in.MultiRegionConfiguration.PrimaryKey.Region != nil {
				f0f17f1.Region = in.MultiRegionConfiguration.PrimaryKey.Region
			}
			f0f17.PrimaryKey = f0f17f1
		}
		if in.MultiRegionConfiguration.ReplicaKeys != nil {
			f0f17f2 := []*ackkmsapitypes.MultiRegionKey{}
			for _, f0f17f2iter := range in.MultiRegionConfiguration.ReplicaKeys {
				f0f17f2elem := &ackkmsapitypes.MultiRegionKey{}
				if f0f17f2iter.Arn != nil {
					f0f17f2elem.ARN = f0f17f2iter.Arn
				}
				if f0f17f2iter.Region != nil {
					f0f17f2elem.Region = f0f17f2iter.Region
				}
				f0f17f2 = append(f0f17f2, f0f17f2elem)
			}
			f0f17.ReplicaKeys = f0f17f2
		}
		f0.MultiRegionConfiguration = f0f17
	}
	if in.Origin != "" {
		f0.Origin = aws.String(string(in.Origin))
	}
	if in.PendingDeletionWindowInDays != nil {
		pendingDeletionWindowInDaysCopy := int64(*in.PendingDeletionWindowInDays)
		f0.PendingDeletionWindowInDays = &pendingDeletionWindowInDaysCopy
	}
	if in.SigningAlgorithms != nil {
		f0f20 := []*string{}
		for _, f0f20iter := range in.SigningAlgorithms {
			var f0f20elem *string
			f0f20elem = aws.String(string(f0f20iter))
			f0f20 = append(f0f20, f0f20elem)
		}
		f0.SigningAlgorithms = f0f20
	}
	if in.ValidTo != nil {
		f0.ValidTo = &metav1.Time{*in.ValidTo}
	}
	return f0
}
