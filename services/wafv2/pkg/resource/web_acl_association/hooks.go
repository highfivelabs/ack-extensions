// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package web_acl_association

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/wafv2"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/wafv2/types"
	smithy "github.com/aws/smithy-go"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/wafv2/apis/v1alpha1"
)

// webACLRef names ACK's WebACL; the generated references code reads it, and
// the generator writes no RBAC marker for a kind of the same service that is
// not ours.
// +kubebuilder:rbac:groups=wafv2.services.k8s.aws,resources=webacls,verbs=get;list
// +kubebuilder:rbac:groups=wafv2.services.k8s.aws,resources=webacls/status,verbs=get;list

// setWebACL reads the web ACL the resource is associated with: its ARN is
// the spec's (another one, set outside Kubernetes, shows as a delta and is
// replaced), its name the status's.
func setWebACL(ko *svcapitypes.WebACLAssociation, acl *svcsdktypes.WebACL) {
	if acl == nil {
		return
	}
	ko.Spec.WebACLARN = acl.ARN
	ko.Status.WebACLName = acl.Name
}

// associatedElsewhere reports whether the resource is now associated with
// no web ACL, or with one that is not the spec's: the delete then leaves it
// alone, as nothing of this object's is left to disassociate.
func (rm *resourceManager) associatedElsewhere(ctx context.Context, r *resource) (bool, error) {
	resp, err := rm.sdkapi.GetWebACLForResource(ctx, &svcsdk.GetWebACLForResourceInput{ResourceArn: r.ko.Spec.ResourceARN})
	rm.metrics.RecordAPICall("READ_ONE", "GetWebACLForResource", err)
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "WAFNonexistentItemException" {
			return true, nil
		}
		return false, err
	}
	return resp.WebACL == nil || aws.ToString(resp.WebACL.ARN) != aws.ToString(r.ko.Spec.WebACLARN), nil
}
