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
// pkg/resource/replica_key/hook.go (customFind's KeyMetadata conversion,
// customUpdate, GetDeletePendingWindowInDays), policy_hooks.go and
// tag_hooks.go. Changes: every call on the replica (DescribeKey,
// GetKeyPolicy, PutKeyPolicy, ListResourceTags, TagResource,
// UntagResource, ScheduleKeyDeletion) goes to a client for the replica's
// Region and names the replica by its ARN. The pull request sent them to
// the manager's Region, the primary key's, where the replica's key ID
// names the primary key: its delete would have scheduled the primary key
// for deletion (an ACK maintainer's review of the pull request reports
// the same). The replica is found through the primary key's
// MultiRegionConfiguration, so a replica created before its status was
// saved is found again; a replica pending deletion reads as gone; the
// generated sdk.go's create and delete are written here.

package replica_key

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	ackkmsapitypes "github.com/aws-controllers-k8s/kms-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/kms"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	smithy "github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/kms/apis/v1alpha1"
)

const (
	// DefaultDeletePendingWindowInDays is KMS's default waiting period.
	DefaultDeletePendingWindowInDays = int64(30)
	// policyName is the only key policy name KMS has.
	policyName = "default"
)

// GetDeletePendingWindowInDays returns the pending window (in days) as
// determined by ACK's kms annotation on the object
// (kms.services.k8s.aws/pending-window-in-days, as on ACK's Key), or the
// default value otherwise.
func GetDeletePendingWindowInDays(m *metav1.ObjectMeta) int64 {
	pendingWindow, ok := m.GetAnnotations()[ackkmsapitypes.AnnotationDeletePendingWindow]
	if !ok {
		return DefaultDeletePendingWindowInDays
	}
	pendingWindowInt, err := strconv.Atoi(pendingWindow)
	if err != nil {
		return DefaultDeletePendingWindowInDays
	}
	return int64(pendingWindowInt)
}

func isAPIError(err error, codes ...string) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	for _, c := range codes {
		if apiErr.ErrorCode() == c {
			return true
		}
	}
	return false
}

// replicaClient is a KMS client for the replica's Region: the replica is
// read, changed and deleted there, never in the primary key's Region.
func (rm *resourceManager) replicaClient(region string) *svcsdk.Client {
	return svcsdk.NewFromConfig(rm.clientcfg, func(o *svcsdk.Options) { o.Region = region })
}

// replicaEnabled is the readiness rule (manager.go, IsSynced).
func replicaEnabled(md *ackkmsapitypes.KeyMetadata) bool {
	return md != nil && aws.ToString(md.KeyState) == string(svcsdktypes.KeyStateEnabled)
}

// gone is a replica KMS has deleted or will delete.
func gone(md *svcsdktypes.KeyMetadata) bool {
	switch md.KeyState {
	case svcsdktypes.KeyStatePendingDeletion, svcsdktypes.KeyStatePendingReplicaDeletion:
		return true
	}
	return false
}

// replicaARN returns the ARN of the primary key's replica in this object's
// replica Region, from the primary key's MultiRegionConfiguration, or ""
// when there is none.
func (rm *resourceManager) replicaARN(ctx context.Context, ko *svcapitypes.ReplicaKey) (string, error) {
	resp, err := rm.sdkapi.DescribeKey(ctx, &svcsdk.DescribeKeyInput{KeyId: ko.Spec.KeyID})
	rm.metrics.RecordAPICall("READ_ONE", "DescribeKey", err)
	if err != nil {
		return "", err
	}
	// The key must be a multi-Region key whose primary is in this Region.
	// That is the primary key itself: a replica of it here would have its
	// primary elsewhere. (MultiRegionKeyType says the same on AWS; moto
	// can report a primary key as REPLICA once its replica was described,
	// docs/plan.md section 4.4.)
	md := resp.KeyMetadata
	if md == nil || md.MultiRegionConfiguration == nil || md.MultiRegionConfiguration.PrimaryKey == nil ||
		aws.ToString(md.MultiRegionConfiguration.PrimaryKey.Region) != string(rm.awsRegion) {
		return "", ackerr.NewTerminalError(fmt.Errorf("key %s is not a multi-Region primary key in %s", aws.ToString(ko.Spec.KeyID), rm.awsRegion))
	}
	for _, r := range md.MultiRegionConfiguration.ReplicaKeys {
		if aws.ToString(r.Region) == aws.ToString(ko.Spec.ReplicaRegion) {
			return aws.ToString(r.Arn), nil
		}
	}
	return "", nil
}

func (rm *resourceManager) requiredFieldsMissingFromReadOneInput(r *resource) bool {
	return aws.ToString(r.ko.Spec.KeyID) == "" || aws.ToString(r.ko.Spec.ReplicaRegion) == ""
}

// sdkFind finds the replica through the primary key and reads it in its
// own Region: its metadata, and the policy and tags, so that a change made
// outside Kubernetes is drift. A replica pending deletion reads as gone.
func (rm *resourceManager) sdkFind(ctx context.Context, r *resource) (latest *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkFind")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, ackerr.NotFound
	}
	arn, err := rm.replicaARN(ctx, r.ko)
	if err != nil {
		if isAPIError(err, "NotFoundException") {
			return nil, ackerr.NotFound
		}
		return nil, err
	}
	if arn == "" {
		return nil, ackerr.NotFound
	}
	client := rm.replicaClient(aws.ToString(r.ko.Spec.ReplicaRegion))
	resp, err := client.DescribeKey(ctx, &svcsdk.DescribeKeyInput{KeyId: aws.String(arn)})
	rm.metrics.RecordAPICall("READ_ONE", "DescribeKey", err)
	if err != nil {
		if isAPIError(err, "NotFoundException") {
			return nil, ackerr.NotFound
		}
		return nil, err
	}
	if resp.KeyMetadata == nil || gone(resp.KeyMetadata) {
		return nil, ackerr.NotFound
	}
	ko := r.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	ko.Status.ReplicaKeyMetadata = keyMetadataFromSDK(resp.KeyMetadata)
	rm.setARN(ko)
	// The policy is compared only when the spec sets one; KMS attaches its
	// default policy otherwise.
	if r.ko.Spec.Policy != nil {
		policy, err := rm.getPolicy(ctx, client, arn)
		if err != nil {
			return nil, err
		}
		ko.Spec.Policy = policy
	}
	tags, err := rm.listTags(ctx, client, arn)
	if err != nil {
		return nil, err
	}
	ko.Spec.Tags = tags
	return &resource{ko}, nil
}

func (rm *resourceManager) setARN(ko *svcapitypes.ReplicaKey) {
	if ko.Status.ReplicaKeyMetadata != nil && ko.Status.ReplicaKeyMetadata.ARN != nil {
		arn := ackv1alpha1.AWSResourceName(*ko.Status.ReplicaKeyMetadata.ARN)
		ko.Status.ACKResourceMetadata.ARN = &arn
	}
}

// sdkCreate calls ReplicateKey in the primary key's Region (the manager's),
// as KMS requires.
func (rm *resourceManager) sdkCreate(ctx context.Context, desired *resource) (created *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkCreate")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(desired) {
		return nil, ackerr.NewTerminalError(fmt.Errorf("spec.keyID and spec.replicaRegion are required"))
	}
	spec := desired.ko.Spec
	input := &svcsdk.ReplicateKeyInput{
		KeyId:                          spec.KeyID,
		ReplicaRegion:                  spec.ReplicaRegion,
		Description:                    spec.Description,
		Policy:                         spec.Policy,
		BypassPolicyLockoutSafetyCheck: aws.ToBool(spec.BypassPolicyLockoutSafetyCheck),
		Tags:                           tagsToSDK(spec.Tags),
	}
	resp, err := rm.sdkapi.ReplicateKey(ctx, input)
	rm.metrics.RecordAPICall("CREATE", "ReplicateKey", err)
	if err != nil {
		return nil, err
	}
	ko := desired.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	if resp.ReplicaKeyMetadata != nil {
		ko.Status.ReplicaKeyMetadata = keyMetadataFromSDK(resp.ReplicaKeyMetadata)
	}
	rm.setARN(ko)
	return &resource{ko}, nil
}

// sdkUpdate changes the replica's policy and tags in its own Region (PR
// #112, customUpdate). Every other field is immutable.
func (rm *resourceManager) sdkUpdate(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta *ackcompare.Delta,
) (updated *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkUpdate")
	defer func() { exit(err) }()
	updatedRes := rm.concreteResource(desired.DeepCopy())
	updatedRes.SetStatus(latest)
	md := latest.ko.Status.ReplicaKeyMetadata
	if md == nil || md.ARN == nil {
		return nil, fmt.Errorf("the replica's ARN is not known yet")
	}
	client := rm.replicaClient(aws.ToString(desired.ko.Spec.ReplicaRegion))
	if delta.DifferentAt("Spec.Policy") && aws.ToString(desired.ko.Spec.Policy) != "" {
		if err = rm.updatePolicy(ctx, client, *md.ARN, desired.ko); err != nil {
			return updatedRes, err
		}
	}
	if delta.DifferentAt("Spec.Tags") {
		if err = rm.updateTags(ctx, client, *md.ARN, desired.ko.Spec.Tags); err != nil {
			return updatedRes, err
		}
	}
	rm.setStatusDefaults(updatedRes.ko)
	return updatedRes, nil
}

// sdkDelete schedules the replica's deletion in its own Region. It never
// calls ScheduleKeyDeletion in the primary key's Region, where the
// replica's key ID names the primary key.
func (rm *resourceManager) sdkDelete(ctx context.Context, r *resource) (deleted *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkDelete")
	defer func() { exit(err) }()
	arn := ""
	if md := r.ko.Status.ReplicaKeyMetadata; md != nil {
		arn = aws.ToString(md.ARN)
	}
	if arn == "" {
		if arn, err = rm.replicaARN(ctx, r.ko); err != nil {
			if isAPIError(err, "NotFoundException") {
				return nil, nil
			}
			return nil, err
		}
		if arn == "" {
			return nil, nil
		}
	}
	// The ARN must name a key in the replica's Region, the Region the
	// client sends to: a key ID there names the replica and nothing else.
	// The key ID is sent rather than the ARN (moto's ScheduleKeyDeletion
	// does not resolve an ARN; docs/plan.md section 4.4), with this check
	// standing in for the one AWS makes on an ARN from another Region.
	region := aws.ToString(r.ko.Spec.ReplicaRegion)
	keyID, err := keyIDInRegion(arn, region)
	if err != nil {
		return nil, err
	}
	client := rm.replicaClient(region)
	_, err = client.ScheduleKeyDeletion(ctx, &svcsdk.ScheduleKeyDeletionInput{
		KeyId:               aws.String(keyID),
		PendingWindowInDays: aws.Int32(int32(GetDeletePendingWindowInDays(&r.ko.ObjectMeta))),
	})
	rm.metrics.RecordAPICall("DELETE", "ScheduleKeyDeletion", err)
	if err != nil && !isAPIError(err, "NotFoundException") {
		return nil, err
	}
	return nil, nil
}

// keyIDInRegion returns the key ID of a KMS key ARN
// (arn:<partition>:kms:<region>:<account>:key/<key-id>) when the ARN is in
// region, and a terminal error otherwise.
func keyIDInRegion(arn, region string) (string, error) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 || parts[2] != "kms" || !strings.HasPrefix(parts[5], "key/") || strings.TrimPrefix(parts[5], "key/") == "" {
		return "", ackerr.NewTerminalError(fmt.Errorf("%q is not a KMS key ARN", arn))
	}
	if parts[3] != region {
		return "", ackerr.NewTerminalError(fmt.Errorf("the replica's ARN %s is not in its Region %s; refusing to delete", arn, region))
	}
	return strings.TrimPrefix(parts[5], "key/"), nil
}

// updatePolicy performs the PutKeyPolicy operation (PR #112,
// policy_hooks.go).
func (rm *resourceManager) updatePolicy(ctx context.Context, client *svcsdk.Client, arn string, ko *svcapitypes.ReplicaKey) error {
	_, err := client.PutKeyPolicy(ctx, &svcsdk.PutKeyPolicyInput{
		BypassPolicyLockoutSafetyCheck: aws.ToBool(ko.Spec.BypassPolicyLockoutSafetyCheck),
		KeyId:                          aws.String(arn),
		Policy:                         ko.Spec.Policy,
		PolicyName:                     aws.String(policyName),
	})
	rm.metrics.RecordAPICall("UPDATE", "PutKeyPolicy", err)
	return err
}

// getPolicy performs the GetKeyPolicy API call (PR #112, policy_hooks.go).
func (rm *resourceManager) getPolicy(ctx context.Context, client *svcsdk.Client, arn string) (*string, error) {
	resp, err := client.GetKeyPolicy(ctx, &svcsdk.GetKeyPolicyInput{KeyId: aws.String(arn), PolicyName: aws.String(policyName)})
	rm.metrics.RecordAPICall("GET", "GetKeyPolicy", err)
	if err != nil {
		return nil, err
	}
	return resp.Policy, nil
}

// listTags performs the ListResourceTags API call (PR #112,
// tag_hooks.go), returning the tags sorted by key.
func (rm *resourceManager) listTags(ctx context.Context, client *svcsdk.Client, arn string) ([]*ackkmsapitypes.Tag, error) {
	m := map[string]string{}
	var marker *string
	for {
		resp, err := client.ListResourceTags(ctx, &svcsdk.ListResourceTagsInput{KeyId: aws.String(arn), Marker: marker})
		rm.metrics.RecordAPICall("GET", "ListResourceTags", err)
		if err != nil {
			return nil, err
		}
		for _, t := range resp.Tags {
			m[aws.ToString(t.TagKey)] = aws.ToString(t.TagValue)
		}
		if !resp.Truncated {
			break
		}
		marker = resp.NextMarker
	}
	return tagsFromMap(m), nil
}

// updateTags removes the tags the spec no longer lists and puts the rest
// (PR #112, tag_hooks.go).
func (rm *resourceManager) updateTags(ctx context.Context, client *svcsdk.Client, arn string, desired []*ackkmsapitypes.Tag) error {
	latest, err := rm.listTags(ctx, client, arn)
	if err != nil {
		return err
	}
	want := tagMap(desired)
	var remove []string
	for k := range tagMap(latest) {
		if _, ok := want[k]; !ok {
			remove = append(remove, k)
		}
	}
	if len(remove) > 0 {
		_, err = client.UntagResource(ctx, &svcsdk.UntagResourceInput{KeyId: aws.String(arn), TagKeys: remove})
		rm.metrics.RecordAPICall("UPDATE", "UntagResource", err)
		if err != nil {
			return err
		}
	}
	if len(want) == 0 {
		return nil
	}
	_, err = client.TagResource(ctx, &svcsdk.TagResourceInput{KeyId: aws.String(arn), Tags: tagsToSDK(desired)})
	rm.metrics.RecordAPICall("UPDATE", "TagResource", err)
	return err
}

func tagMap(tags []*ackkmsapitypes.Tag) map[string]string {
	m := map[string]string{}
	for _, t := range tags {
		if t != nil && t.TagKey != nil {
			m[*t.TagKey] = aws.ToString(t.TagValue)
		}
	}
	return m
}

func tagsFromMap(m map[string]string) []*ackkmsapitypes.Tag {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*ackkmsapitypes.Tag, 0, len(keys))
	for _, k := range keys {
		out = append(out, &ackkmsapitypes.Tag{TagKey: aws.String(k), TagValue: aws.String(m[k])})
	}
	return out
}

func tagsToSDK(tags []*ackkmsapitypes.Tag) []svcsdktypes.Tag {
	var out []svcsdktypes.Tag
	for _, t := range tags {
		if t != nil && t.TagKey != nil {
			out = append(out, svcsdktypes.Tag{TagKey: t.TagKey, TagValue: aws.String(aws.ToString(t.TagValue))})
		}
	}
	return out
}

// samePolicy is JSON equality: KMS may store a policy reformatted.
func samePolicy(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	if *a == *b {
		return true
	}
	var ja, jb any
	if json.Unmarshal([]byte(*a), &ja) != nil || json.Unmarshal([]byte(*b), &jb) != nil {
		return false
	}
	return reflect.DeepEqual(ja, jb)
}

func (rm *resourceManager) setStatusDefaults(ko *svcapitypes.ReplicaKey) {
	if ko.Status.ACKResourceMetadata == nil {
		ko.Status.ACKResourceMetadata = &ackv1alpha1.ResourceMetadata{}
	}
	if ko.Status.ACKResourceMetadata.Region == nil {
		ko.Status.ACKResourceMetadata.Region = &rm.awsRegion
	}
	if ko.Status.ACKResourceMetadata.Partition == nil {
		ko.Status.ACKResourceMetadata.Partition = &rm.awsPartition
	}
	if ko.Status.ACKResourceMetadata.OwnerAccountID == nil {
		ko.Status.ACKResourceMetadata.OwnerAccountID = &rm.awsAccountID
	}
	if ko.Status.Conditions == nil {
		ko.Status.Conditions = []*ackv1alpha1.Condition{}
	}
}

// updateConditions sets the Terminal and Recoverable conditions as ACK's
// generated resources do (code-generator templates/pkg/resource/sdk.go.tpl)
// and leaves ACK.ResourceSynced to the runtime, which then asks IsSynced.
func (rm *resourceManager) updateConditions(r *resource, onSuccess bool, err error) (*resource, bool) {
	ko := r.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	var terminalCondition, recoverableCondition, syncCondition *ackv1alpha1.Condition
	for _, condition := range ko.Status.Conditions {
		switch condition.Type {
		case ackv1alpha1.ConditionTypeTerminal:
			terminalCondition = condition
		case ackv1alpha1.ConditionTypeRecoverable:
			recoverableCondition = condition
		case ackv1alpha1.ConditionTypeResourceSynced:
			syncCondition = condition
		}
	}
	var termError *ackerr.TerminalError
	if rm.terminalAWSError(err) || errors.As(err, &termError) {
		if terminalCondition == nil {
			terminalCondition = &ackv1alpha1.Condition{Type: ackv1alpha1.ConditionTypeTerminal}
			ko.Status.Conditions = append(ko.Status.Conditions, terminalCondition)
		}
		msg := err.Error()
		if awsErr, _ := ackerr.AWSError(err); awsErr != nil {
			msg = awsErr.Error()
		}
		terminalCondition.Status = corev1.ConditionTrue
		terminalCondition.Message = &msg
	} else {
		if terminalCondition != nil {
			terminalCondition.Status = corev1.ConditionFalse
			terminalCondition.Message = nil
		}
		if err != nil {
			if recoverableCondition == nil {
				recoverableCondition = &ackv1alpha1.Condition{Type: ackv1alpha1.ConditionTypeRecoverable}
				ko.Status.Conditions = append(ko.Status.Conditions, recoverableCondition)
			}
			msg := err.Error()
			if awsErr, _ := ackerr.AWSError(err); awsErr != nil {
				msg = awsErr.Error()
			}
			recoverableCondition.Status = corev1.ConditionTrue
			recoverableCondition.Message = &msg
		} else if recoverableCondition != nil {
			recoverableCondition.Status = corev1.ConditionFalse
			recoverableCondition.Message = nil
		}
	}
	if terminalCondition != nil || recoverableCondition != nil || syncCondition != nil {
		return &resource{ko}, true
	}
	return nil, false
}

// terminalAWSError: ReplicateKey's errors (KMS API reference) that no
// retry fixes: a replica already in that Region, a bad policy, tags or
// key, a Region or key type KMS cannot replicate to, a disabled primary.
// A primary key that does not exist yet is retried. (Pull request #112
// configures no terminal codes; these are ours.)
func (rm *resourceManager) terminalAWSError(err error) bool {
	return isAPIError(err,
		"AlreadyExistsException", "InvalidArnException", "MalformedPolicyDocumentException",
		"TagException", "UnsupportedOperationException", "DisabledException",
	)
}
