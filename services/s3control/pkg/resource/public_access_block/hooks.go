// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package public_access_block

import (
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackcfg "github.com/aws-controllers-k8s/runtime/pkg/config"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/s3control"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/s3control/types"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/s3control/apis/v1alpha1"
	"github.com/highfivelabs/ack-extensions/services/s3control/pkg/endpoint"
)

// clientOptions keeps the S3 Control client on a fake's own host when the
// runtime was pointed at one (pkg/endpoint); against AWS it changes
// nothing.
func clientOptions(cfg ackcfg.Config) func(*svcsdk.Options) {
	return endpoint.ForConfig(cfg.EndpointURL, cfg.AllowUnsafeEndpointURL)
}

// accountID is the account every request names: the manager's own (its
// role's, or the one CARM maps the namespace to). The settings are the
// account's, so the spec names no account.
func (rm *resourceManager) accountID() *string {
	id := string(rm.awsAccountID)
	return &id
}

// made is whether this object has put the settings once. Before that its
// read finds nothing, so the create takes over settings the account
// already has, rather than the runtime refusing a new object for a
// setting that already exists (as AccountPasswordPolicy does).
func made(ko *svcapitypes.PublicAccessBlock) bool {
	return ko.Status.ACKResourceMetadata != nil && ko.Status.ACKResourceMetadata.ARN != nil
}

// notMade is the read's check for its required fields
// (custom_check_required_fields_missing_method): there is nothing to read
// until the object has put the settings.
func (rm *resourceManager) notMade(r *resource) bool { return !made(r.ko) }

// setARN names the settings. S3 has no ARN for them, so this is an ARN of
// the manager's form with a fixed name: the kind's identifier
// (is_arn_primary_key) and the mark that the object has put the settings,
// never sent to AWS.
func (rm *resourceManager) setARN(ko *svcapitypes.PublicAccessBlock) {
	if ko.Status.ACKResourceMetadata == nil {
		ko.Status.ACKResourceMetadata = &ackv1alpha1.ResourceMetadata{}
	}
	arn := ackv1alpha1.AWSResourceName(rm.ARNFromName("public-access-block"))
	ko.Status.ACKResourceMetadata.ARN = &arn
}

// setConfiguration is the read: the four settings as S3 reports them (all
// four, false unless set). The response's configuration is its HTTP
// payload, which the generator does not map.
func setConfiguration(ko *svcapitypes.PublicAccessBlock, c *svcsdktypes.PublicAccessBlockConfiguration) {
	if c == nil {
		ko.Spec.PublicAccessBlockConfiguration = nil
		return
	}
	ko.Spec.PublicAccessBlockConfiguration = &svcapitypes.PublicAccessBlockConfiguration{
		BlockPublicACLs:       c.BlockPublicAcls,
		IgnorePublicACLs:      c.IgnorePublicAcls,
		BlockPublicPolicy:     c.BlockPublicPolicy,
		RestrictPublicBuckets: c.RestrictPublicBuckets,
	}
}
