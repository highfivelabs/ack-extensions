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
// Modified by High Five Labs for ack-extensions: the AddPermission,
// RemovePermission and GetPolicy calls, and the reading of a statement's
// principal, action and conditions back into AddPermissionInput's fields,
// are copied from aws-controllers-k8s/lambda-controller v1.19.0
// (251806273b5d), pkg/resource/alias/hooks.go (addPermission,
// removePermission, setPermissions). Changes: one statement for a
// function, version or alias instead of an alias's whole set; the policy
// is parsed with encoding/json instead of github.com/micahhausler/
// aws-iam-policy (no new dependency); a missing function or policy reads as
// not found and a missing statement counts as removed.

package permission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/lambda"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	smithy "github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/lambda/apis/v1alpha1"
)

// stringOrList is an IAM policy value that is a string or a list of them.
type stringOrList []string

func (s *stringOrList) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*s = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

func (s stringOrList) first() *string {
	if len(s) == 0 {
		return nil
	}
	return aws.String(s[0])
}

// principal is "*" or {"Service": ...} or {"AWS": ...}.
type principal struct {
	any     bool
	service stringOrList
	account stringOrList
}

func (p *principal) UnmarshalJSON(b []byte) error {
	var star string
	if err := json.Unmarshal(b, &star); err == nil {
		p.any = star == "*"
		if !p.any {
			p.account = stringOrList{star}
		}
		return nil
	}
	var m struct {
		Service stringOrList `json:"Service"`
		AWS     stringOrList `json:"AWS"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	p.service, p.account = m.Service, m.AWS
	return nil
}

// statement is the part of a policy statement AddPermission writes.
type statement struct {
	Sid       string                             `json:"Sid"`
	Action    stringOrList                       `json:"Action"`
	Principal principal                          `json:"Principal"`
	Condition map[string]map[string]stringOrList `json:"Condition"`
	raw       json.RawMessage
}

type policyDocument struct {
	Statement []json.RawMessage `json:"Statement"`
}

// findStatement returns the statement with this Sid, and its JSON.
func findStatement(policy, sid string) (*statement, bool, error) {
	var doc policyDocument
	if err := json.Unmarshal([]byte(policy), &doc); err != nil {
		return nil, false, fmt.Errorf("reading the function's policy: %w", err)
	}
	for _, raw := range doc.Statement {
		var st statement
		if err := json.Unmarshal(raw, &st); err != nil {
			return nil, false, fmt.Errorf("reading the function's policy: %w", err)
		}
		if st.Sid == sid {
			st.raw = raw
			return &st, true, nil
		}
	}
	return nil, false, nil
}

func (st *statement) condition(op, key string) *string {
	for k, v := range st.Condition[op] {
		if strings.EqualFold(k, key) {
			return v.first()
		}
	}
	return nil
}

// observedSpec is the statement read back into the spec's fields (ACK's
// Alias, setPermissions).
func (st *statement) observedSpec() svcapitypes.PermissionSpec {
	var spec svcapitypes.PermissionSpec
	spec.StatementID = aws.String(st.Sid)
	spec.Action = st.Action.first()
	switch {
	case st.Principal.any:
		spec.Principal = aws.String("*")
	case len(st.Principal.service) > 0:
		spec.Principal = st.Principal.service.first()
	default:
		spec.Principal = st.Principal.account.first()
	}
	spec.SourceARN = st.condition("ArnLike", "AWS:SourceArn")
	spec.SourceAccount = st.condition("StringEquals", "AWS:SourceAccount")
	spec.EventSourceToken = st.condition("StringEquals", "lambda:EventSourceToken")
	spec.PrincipalOrgID = st.condition("StringEquals", "aws:PrincipalOrgID")
	spec.FunctionURLAuthType = st.condition("StringEquals", "lambda:FunctionUrlAuthType")
	return spec
}

// fillUnset copies the observed value of every field the spec leaves
// unset, as adoption needs. A field the spec sets is kept as written: AWS
// stores some in a normal form (an account ID as its root ARN), and every
// field is immutable, so a difference there is not drift to correct.
func fillUnset(spec *svcapitypes.PermissionSpec, observed svcapitypes.PermissionSpec) {
	for _, f := range []struct{ dst, src **string }{
		{&spec.Action, &observed.Action},
		{&spec.Principal, &observed.Principal},
		{&spec.SourceARN, &observed.SourceARN},
		{&spec.SourceAccount, &observed.SourceAccount},
		{&spec.EventSourceToken, &observed.EventSourceToken},
		{&spec.PrincipalOrgID, &observed.PrincipalOrgID},
		{&spec.FunctionURLAuthType, &observed.FunctionURLAuthType},
	} {
		if *f.dst == nil && *f.src != nil {
			*f.dst = *f.src
		}
	}
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

func qualifier(ko *svcapitypes.Permission) *string {
	if aws.ToString(ko.Spec.Qualifier) == "" {
		return nil
	}
	return ko.Spec.Qualifier
}

func (rm *resourceManager) requiredFieldsMissingFromReadOneInput(r *resource) bool {
	return aws.ToString(r.ko.Spec.FunctionName) == "" || aws.ToString(r.ko.Spec.StatementID) == ""
}

// sdkFind reads the function's (or version's or alias's) policy and keeps
// the statement with this ID. No function, no policy (Lambda answers
// ResourceNotFoundException for an empty one) or no such statement reads
// as not found.
func (rm *resourceManager) sdkFind(ctx context.Context, r *resource) (latest *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkFind")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, ackerr.NotFound
	}
	resp, err := rm.sdkapi.GetPolicy(ctx, &svcsdk.GetPolicyInput{
		FunctionName: r.ko.Spec.FunctionName,
		Qualifier:    qualifier(r.ko),
	})
	rm.metrics.RecordAPICall("READ_ONE", "GetPolicy", err)
	if err != nil {
		if isAPIError(err, "ResourceNotFoundException") {
			return nil, ackerr.NotFound
		}
		return nil, err
	}
	st, ok, err := findStatement(aws.ToString(resp.Policy), aws.ToString(r.ko.Spec.StatementID))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ackerr.NotFound
	}
	ko := r.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	fillUnset(&ko.Spec, st.observedSpec())
	ko.Status.Statement = aws.String(string(st.raw))
	arn := ackv1alpha1.AWSResourceName(rm.permissionARN(ko))
	ko.Status.ACKResourceMetadata.ARN = &arn
	return &resource{ko}, nil
}

// permissionARN names the statement. Lambda has no ARN for one, so this is
// the function's ARN form (with its qualifier) and the statement's ID; it
// is an identifier for ACK's metadata, never sent to AWS.
func (rm *resourceManager) permissionARN(ko *svcapitypes.Permission) string {
	fn := aws.ToString(ko.Spec.FunctionName)
	base := fn
	if !strings.HasPrefix(fn, "arn:") {
		base = rm.ARNFromName("function:" + fn)
	}
	if q := qualifier(ko); q != nil && !strings.HasSuffix(base, ":"+*q) {
		base += ":" + *q
	}
	return base + "/policy/" + aws.ToString(ko.Spec.StatementID)
}

func (rm *resourceManager) sdkCreate(ctx context.Context, desired *resource) (created *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkCreate")
	defer func() { exit(err) }()
	spec := desired.ko.Spec
	if rm.requiredFieldsMissingFromReadOneInput(desired) {
		return nil, ackerr.NewTerminalError(fmt.Errorf("spec.functionName and spec.statementID are required"))
	}
	input := &svcsdk.AddPermissionInput{
		FunctionName:     spec.FunctionName,
		Qualifier:        qualifier(desired.ko),
		StatementId:      spec.StatementID,
		Action:           spec.Action,
		Principal:        spec.Principal,
		SourceArn:        spec.SourceARN,
		SourceAccount:    spec.SourceAccount,
		EventSourceToken: spec.EventSourceToken,
		PrincipalOrgID:   spec.PrincipalOrgID,
	}
	if spec.FunctionURLAuthType != nil {
		input.FunctionUrlAuthType = svcsdktypes.FunctionUrlAuthType(*spec.FunctionURLAuthType)
	}
	resp, err := rm.sdkapi.AddPermission(ctx, input)
	rm.metrics.RecordAPICall("CREATE", "AddPermission", err)
	if err != nil {
		return nil, err
	}
	ko := desired.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	ko.Status.Statement = resp.Statement
	arn := ackv1alpha1.AWSResourceName(rm.permissionARN(ko))
	ko.Status.ACKResourceMetadata.ARN = &arn
	return &resource{ko}, nil
}

// sdkUpdate refuses every change: Lambda cannot change a statement (the
// CRD also marks every field immutable).
func (rm *resourceManager) sdkUpdate(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta interface{},
) (updated *resource, err error) {
	return nil, ackerr.NewTerminalError(fmt.Errorf("a Permission cannot be changed; delete it and create another"))
}

func (rm *resourceManager) sdkDelete(ctx context.Context, r *resource) (deleted *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkDelete")
	defer func() { exit(err) }()
	if rm.requiredFieldsMissingFromReadOneInput(r) {
		return nil, nil
	}
	_, err = rm.sdkapi.RemovePermission(ctx, &svcsdk.RemovePermissionInput{
		FunctionName: r.ko.Spec.FunctionName,
		Qualifier:    qualifier(r.ko),
		StatementId:  r.ko.Spec.StatementID,
	})
	rm.metrics.RecordAPICall("DELETE", "RemovePermission", err)
	if err != nil && !isAPIError(err, "ResourceNotFoundException") {
		return nil, err
	}
	return nil, nil
}

func (rm *resourceManager) setStatusDefaults(ko *svcapitypes.Permission) {
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

// terminalAWSError: a statement Lambda refuses, or a policy at its size
// limit, is refused again until the spec changes. A function that does
// not exist yet (ResourceNotFoundException) and a concurrent change to the
// policy (ResourceConflictException) are retried.
func (rm *resourceManager) terminalAWSError(err error) bool {
	return isAPIError(err, "InvalidParameterValueException", "PolicyLengthExceededException")
}
