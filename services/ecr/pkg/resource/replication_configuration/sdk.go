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
// Modified by High Five Labs for ack-extensions: the behaviour and the
// conversions between the API and the SDK types are copied from
// aws-controllers-k8s/ecr-controller pull request #121 (head f6c7d57e),
// pkg/resource/replication_configuration/manager.go (ReadOne, Create,
// Update, Delete). Changes: split into the sdkFind/sdkCreate/sdkUpdate/
// sdkDelete of our hand-written kinds; its debug logging and the
// DescribeRegistry calls made only to log are dropped; a registry with no
// rules reads as not found, so the runtime creates (and owns) the
// configuration; spec.registryID, if set, must be the registry the
// manager acts as; Update returns the put configuration rather than
// calling Create.

package replication_configuration

import (
	"context"
	"errors"
	"fmt"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/ecr"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	smithy "github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/ecr/apis/v1alpha1"
)

// rulesFromSDK converts DescribeRegistry's rules to the API's (PR #121,
// ReadOne).
func rulesFromSDK(cfg *svcsdktypes.ReplicationConfiguration) []*svcapitypes.ReplicationRule {
	if cfg == nil || len(cfg.Rules) == 0 {
		return nil
	}
	rules := make([]*svcapitypes.ReplicationRule, len(cfg.Rules))
	for i, awsRule := range cfg.Rules {
		destinations := make([]*svcapitypes.ReplicationDestination, len(awsRule.Destinations))
		for j, awsDest := range awsRule.Destinations {
			destinations[j] = &svcapitypes.ReplicationDestination{
				RegistryID: awsDest.RegistryId,
				Region:     awsDest.Region,
			}
		}
		var repositoryFilters []*svcapitypes.RepositoryFilter
		if awsRule.RepositoryFilters != nil {
			repositoryFilters = make([]*svcapitypes.RepositoryFilter, len(awsRule.RepositoryFilters))
			for k, awsFilter := range awsRule.RepositoryFilters {
				filterType := string(awsFilter.FilterType)
				repositoryFilters[k] = &svcapitypes.RepositoryFilter{
					Filter:     awsFilter.Filter,
					FilterType: &filterType,
				}
			}
		}
		rules[i] = &svcapitypes.ReplicationRule{
			Destinations:      destinations,
			RepositoryFilters: repositoryFilters,
		}
	}
	return rules
}

// configurationToSDK converts the spec's configuration to the SDK's (PR
// #121, Create). A nil configuration is no rules.
func configurationToSDK(cfg *svcapitypes.ReplicationConfigurationForRegistry) *svcsdktypes.ReplicationConfiguration {
	out := &svcsdktypes.ReplicationConfiguration{Rules: []svcsdktypes.ReplicationRule{}}
	if cfg == nil {
		return out
	}
	for _, rule := range cfg.Rules {
		if rule == nil {
			continue
		}
		destinations := make([]svcsdktypes.ReplicationDestination, 0, len(rule.Destinations))
		for _, dest := range rule.Destinations {
			if dest == nil {
				continue
			}
			destinations = append(destinations, svcsdktypes.ReplicationDestination{
				RegistryId: dest.RegistryID,
				Region:     dest.Region,
			})
		}
		var repositoryFilters []svcsdktypes.RepositoryFilter
		for _, filter := range rule.RepositoryFilters {
			if filter == nil {
				continue
			}
			repositoryFilters = append(repositoryFilters, svcsdktypes.RepositoryFilter{
				Filter:     filter.Filter,
				FilterType: svcsdktypes.RepositoryFilterType(aws.ToString(filter.FilterType)),
			})
		}
		out.Rules = append(out.Rules, svcsdktypes.ReplicationRule{
			Destinations:      destinations,
			RepositoryFilters: repositoryFilters,
		})
	}
	return out
}

// registryMismatch is a terminal error when spec.registryID names another
// registry: PutReplicationConfiguration acts on the caller's own registry
// and takes no registry ID.
func registryMismatch(ko *svcapitypes.ReplicationConfiguration, actual string) error {
	want := aws.ToString(ko.Spec.RegistryID)
	if want == "" || want == actual {
		return nil
	}
	return ackerr.NewTerminalError(fmt.Errorf("spec.registryID %s is not the registry this manager acts as (%s); a replication configuration can only be put in the caller's own registry", want, actual))
}

// sdkFind reads the registry. A registry with no replication rules has no
// configuration to own: it reads as not found, and the runtime creates it.
func (rm *resourceManager) sdkFind(ctx context.Context, r *resource) (latest *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkFind")
	defer func() { exit(err) }()
	resp, err := rm.sdkapi.DescribeRegistry(ctx, &svcsdk.DescribeRegistryInput{})
	rm.metrics.RecordAPICall("READ_ONE", "DescribeRegistry", err)
	if err != nil {
		return nil, err
	}
	if err := registryMismatch(r.ko, aws.ToString(resp.RegistryId)); err != nil {
		return nil, err
	}
	rules := rulesFromSDK(resp.ReplicationConfiguration)
	if len(rules) == 0 {
		return nil, ackerr.NotFound
	}
	ko := r.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	ko.Spec.ReplicationConfiguration = &svcapitypes.ReplicationConfigurationForRegistry{Rules: rules}
	rm.setRegistry(ko, aws.ToString(resp.RegistryId))
	return &resource{ko}, nil
}

// setRegistry records the registry in status, with the registry's ARN form
// as the resource's ARN (ECR has no ARN for a replication configuration;
// it is an identifier, never sent to AWS).
func (rm *resourceManager) setRegistry(ko *svcapitypes.ReplicationConfiguration, registry string) {
	if registry == "" {
		return
	}
	ko.Status.RegistryID = aws.String(registry)
	arn := ackv1alpha1.AWSResourceName(rm.ARNFromName("registry/" + registry + "/replication-configuration"))
	ko.Status.ACKResourceMetadata.ARN = &arn
}

// put writes the spec's configuration and returns it as put.
func (rm *resourceManager) put(ctx context.Context, desired *resource, op string) (*resource, error) {
	resp, err := rm.sdkapi.PutReplicationConfiguration(ctx, &svcsdk.PutReplicationConfigurationInput{
		ReplicationConfiguration: configurationToSDK(desired.ko.Spec.ReplicationConfiguration),
	})
	rm.metrics.RecordAPICall(op, "PutReplicationConfiguration", err)
	if err != nil {
		return nil, err
	}
	ko := desired.ko.DeepCopy()
	rm.setStatusDefaults(ko)
	if resp.ReplicationConfiguration != nil {
		ko.Spec.ReplicationConfiguration = &svcapitypes.ReplicationConfigurationForRegistry{Rules: rulesFromSDK(resp.ReplicationConfiguration)}
	}
	return &resource{ko}, nil
}

func (rm *resourceManager) sdkCreate(ctx context.Context, desired *resource) (created *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkCreate")
	defer func() { exit(err) }()
	resp, err := rm.sdkapi.DescribeRegistry(ctx, &svcsdk.DescribeRegistryInput{})
	rm.metrics.RecordAPICall("READ_ONE", "DescribeRegistry", err)
	if err != nil {
		return nil, err
	}
	if err := registryMismatch(desired.ko, aws.ToString(resp.RegistryId)); err != nil {
		return nil, err
	}
	created, err = rm.put(ctx, desired, "CREATE")
	if err != nil {
		return nil, err
	}
	rm.setRegistry(created.ko, aws.ToString(resp.RegistryId))
	return created, nil
}

// sdkUpdate puts the whole configuration again: PutReplicationConfiguration
// replaces it.
func (rm *resourceManager) sdkUpdate(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta interface{},
) (updated *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkUpdate")
	defer func() { exit(err) }()
	updated, err = rm.put(ctx, desired, "UPDATE")
	if err != nil {
		return nil, err
	}
	updated.ko.Status = latest.ko.Status
	return updated, nil
}

// sdkDelete puts a configuration with no rules (PR #121, Delete): ECR has
// no call that deletes one.
func (rm *resourceManager) sdkDelete(ctx context.Context, r *resource) (deleted *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.sdkDelete")
	defer func() { exit(err) }()
	_, err = rm.sdkapi.PutReplicationConfiguration(ctx, &svcsdk.PutReplicationConfigurationInput{
		ReplicationConfiguration: &svcsdktypes.ReplicationConfiguration{Rules: []svcsdktypes.ReplicationRule{}},
	})
	rm.metrics.RecordAPICall("DELETE", "PutReplicationConfiguration", err)
	if err != nil {
		return nil, err
	}
	return nil, nil
}

func (rm *resourceManager) setStatusDefaults(ko *svcapitypes.ReplicationConfiguration) {
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

// terminalAWSError: a configuration ECR refuses (a destination that is the
// source registry, too many rules) is refused again until the spec
// changes.
func (rm *resourceManager) terminalAWSError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.ErrorCode() {
	case "InvalidParameterException", "ValidationException":
		return true
	default:
		return false
	}
}
