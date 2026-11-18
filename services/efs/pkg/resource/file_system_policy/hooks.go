// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package file_system_policy

import (
	"errors"
	"strings"

	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/efs"
	"github.com/aws/smithy-go"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/efs/apis/v1alpha1"
)

// ARNPlaceholder is what a policy document writes for the file system's own
// ARN: the file system's ARN holds its ID, which is not known when the
// document is written, so the controller fills it in.
const ARNPlaceholder = "${fileSystemARN}"

// isPolicyNotFound is true when EFS says the file system has no policy or
// the file system is gone.
func isPolicyNotFound(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.ErrorCode() {
	case "PolicyNotFound", "FileSystemNotFound":
		return true
	}
	return false
}

// fileSystemARN is the ARN of file system id in the manager's account and
// Region.
func (rm *resourceManager) fileSystemARN(id string) string {
	return "arn:" + string(rm.awsPartition) + ":elasticfilesystem:" + string(rm.awsRegion) + ":" +
		string(rm.awsAccountID) + ":file-system/" + id
}

// renderPolicy fills ARNPlaceholder in document with arn.
func renderPolicy(document, arn string) string {
	return strings.ReplaceAll(document, ARNPlaceholder, arn)
}

// renderRequestPolicy writes the desired document, placeholders filled, into
// a Put request.
func renderRequestPolicy(rm *resourceManager, desired *resource, input *svcsdk.PutFileSystemPolicyInput) {
	spec := desired.ko.Spec
	if spec.Policy == nil || spec.FileSystemID == nil {
		return
	}
	input.Policy = aws.String(renderPolicy(*spec.Policy, rm.fileSystemARN(*spec.FileSystemID)))
}

// keepPolicyTemplate keeps the desired template in the spec of what was read
// while the document the file system holds is that template rendered, so a
// template is not seen as a change on every read.
func keepPolicyTemplate(rm *resourceManager, desired *resource, read *svcapitypes.FileSystemPolicy) {
	spec := desired.ko.Spec
	if spec.Policy == nil || read.Spec.Policy == nil || read.Spec.FileSystemID == nil ||
		!strings.Contains(*spec.Policy, ARNPlaceholder) {
		return
	}
	rendered := renderPolicy(*spec.Policy, rm.fileSystemARN(*read.Spec.FileSystemID))
	if equal, err := ackcompare.IAMPolicyDocumentEqual(rendered, *read.Spec.Policy); err == nil && equal {
		read.Spec.Policy = spec.Policy
	}
}
