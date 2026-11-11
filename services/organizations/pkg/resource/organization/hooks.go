// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package organization

// What the generator cannot do for an Organization: the root's enabled
// policy types and the organization's trusted service access, which have
// their own calls and no field in DescribeOrganization, and the update,
// which has no UpdateOrganization.
//
// Both lists are additive (decision A3, 2026-11-10): an entry enabled
// outside Kubernetes is kept, never disabled, and reported in
// status.unmanaged<List> so Bundt can offer to import it. The object
// disables only what it owns, status.owned<List>: the entries it listed
// and found or enabled. listPlan is the rule; docs/plan.md section 6.28.
//
// There is no delete here, and none in sdk.go: DeleteOrganization is
// ignored in generator.yaml, so deleting the object only stops managing
// the organization, whatever its deletion policy (docs/plan.md section 9,
// question 12). The ValidatingAdmissionPolicy in config/admission also
// requires services.k8s.aws/deletion-policy: retain on every Organization.

import (
	"context"
	"errors"
	"fmt"
	"sort"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrtlog "github.com/aws-controllers-k8s/runtime/pkg/runtime/log"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/organizations"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/aws/smithy-go"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/organizations/apis/v1alpha1"
)

// setResourceAdditionalFields reads what DescribeOrganization does not
// return: the root's ID and, for each list, what is enabled now. The
// spec's list becomes what is enabled of the entries it lists or owns,
// so the comparison sees what to enable and what to disable and never
// what someone else enabled; that goes to the unmanaged status list.
func (rm *resourceManager) setResourceAdditionalFields(
	ctx context.Context,
	ko *svcapitypes.Organization,
) (err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.setResourceAdditionalFields")
	defer func() { exit(err) }()

	root, err := rm.getRoot(ctx)
	if err != nil {
		return err
	}
	ko.Status.RootID = root.Id
	types := planList(ko.Spec.EnabledPolicyTypes, ko.Status.OwnedEnabledPolicyTypes, enabledPolicyTypes(root))
	ko.Spec.EnabledPolicyTypes = types.read
	ko.Status.OwnedEnabledPolicyTypes = types.owned
	ko.Status.UnmanagedEnabledPolicyTypes = types.unmanaged

	current, err := rm.getServiceAccess(ctx)
	if err != nil {
		return err
	}
	access := planList(ko.Spec.AWSServiceAccessPrincipals, ko.Status.OwnedAWSServiceAccessPrincipals, current)
	ko.Spec.AWSServiceAccessPrincipals = access.read
	ko.Status.OwnedAWSServiceAccessPrincipals = access.owned
	ko.Status.UnmanagedAWSServiceAccessPrincipals = access.unmanaged
	return nil
}

// adoptExisting is the create when the account already has an
// organization: the object adopts it (its settings synced to the spec)
// rather than calling CreateOrganization. It returns nil when there is
// none to adopt.
func (rm *resourceManager) adoptExisting(
	ctx context.Context,
	desired *resource,
) (adopted *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.adoptExisting")
	defer func() { exit(err) }()

	// Past the read's guard: an ARN is what an adopted object has.
	probe := desired.ko.DeepCopy()
	if probe.Status.ACKResourceMetadata == nil {
		probe.Status.ACKResourceMetadata = &ackv1alpha1.ResourceMetadata{}
	}
	arn := ackv1alpha1.AWSResourceName("")
	probe.Status.ACKResourceMetadata.ARN = &arn
	found, err := rm.sdkFind(ctx, &resource{probe})
	if err == ackerr.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// The spec stays the object's: the read put the observed lists in it.
	ko := found.ko
	featureSet := ko.Spec.FeatureSet
	ko.Spec = *desired.ko.Spec.DeepCopy()
	if ko.Spec.FeatureSet == nil {
		ko.Spec.FeatureSet = featureSet
	}
	if err = rm.syncSettings(ctx, ko); err != nil {
		return nil, err
	}
	return &resource{ko}, nil
}

// customPreCompare compares the two lists as sets, and only when the spec
// lists something. The read has already left out what the object does not
// own (setResourceAdditionalFields).
func customPreCompare(
	delta *ackcompare.Delta,
	a *resource,
	b *resource,
) {
	if len(a.ko.Spec.EnabledPolicyTypes) > 0 && !sameSet(a.ko.Spec.EnabledPolicyTypes, b.ko.Spec.EnabledPolicyTypes) {
		delta.Add("Spec.EnabledPolicyTypes", a.ko.Spec.EnabledPolicyTypes, b.ko.Spec.EnabledPolicyTypes)
	}
	if len(a.ko.Spec.AWSServiceAccessPrincipals) > 0 && !sameSet(a.ko.Spec.AWSServiceAccessPrincipals, b.ko.Spec.AWSServiceAccessPrincipals) {
		delta.Add("Spec.AWSServiceAccessPrincipals", a.ko.Spec.AWSServiceAccessPrincipals, b.ko.Spec.AWSServiceAccessPrincipals)
	}
}

// customUpdateOrganization is the update: the feature set cannot change
// here (EnableAllFeatures is a handshake every member account must accept),
// and the two lists are synced through their own calls.
func (rm *resourceManager) customUpdateOrganization(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta *ackcompare.Delta,
) (updated *resource, err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.customUpdateOrganization")
	defer func() { exit(err) }()

	// An unset featureSet adopts the organization's (late initialization).
	if desired.ko.Spec.FeatureSet != nil && delta.DifferentAt("Spec.FeatureSet") {
		return nil, ackerr.NewTerminalError(fmt.Errorf(
			"the organization's feature set is %s, not %s; it is not changed from Kubernetes",
			aws.ToString(latest.ko.Spec.FeatureSet), aws.ToString(desired.ko.Spec.FeatureSet)))
	}
	if delta.DifferentAt("Spec.EnabledPolicyTypes") || delta.DifferentAt("Spec.AWSServiceAccessPrincipals") {
		if err = rm.syncSettings(ctx, desired.ko); err != nil {
			return nil, err
		}
	}
	updated = rm.concreteResource(desired.DeepCopy())
	updated.SetStatus(latest)
	updated.ko.Status.OwnedEnabledPolicyTypes = desired.ko.Status.OwnedEnabledPolicyTypes
	updated.ko.Status.UnmanagedEnabledPolicyTypes = desired.ko.Status.UnmanagedEnabledPolicyTypes
	updated.ko.Status.OwnedAWSServiceAccessPrincipals = desired.ko.Status.OwnedAWSServiceAccessPrincipals
	updated.ko.Status.UnmanagedAWSServiceAccessPrincipals = desired.ko.Status.UnmanagedAWSServiceAccessPrincipals
	return updated, nil
}

// syncSettings enables, for each list the spec has entries in, what it
// lists and is not enabled, and disables what the object owns and no
// longer lists. Nothing it does not own is disabled. It leaves the owned
// and unmanaged status lists as they are after the calls.
func (rm *resourceManager) syncSettings(
	ctx context.Context,
	ko *svcapitypes.Organization,
) (err error) {
	rlog := ackrtlog.FromContext(ctx)
	exit := rlog.Trace("rm.syncSettings")
	defer func() { exit(err) }()

	root, err := rm.getRoot(ctx)
	if err != nil {
		return err
	}
	ko.Status.RootID = root.Id
	types := planList(ko.Spec.EnabledPolicyTypes, ko.Status.OwnedEnabledPolicyTypes, enabledPolicyTypes(root))
	for _, t := range types.enable {
		_, err = rm.sdkapi.EnablePolicyType(ctx, &svcsdk.EnablePolicyTypeInput{RootId: root.Id, PolicyType: svcsdktypes.PolicyType(t)})
		rm.metrics.RecordAPICall("UPDATE", "EnablePolicyType", err)
		if err != nil && !isAWSError(err, "PolicyTypeAlreadyEnabledException") {
			return err
		}
	}
	for _, t := range types.disable {
		_, err = rm.sdkapi.DisablePolicyType(ctx, &svcsdk.DisablePolicyTypeInput{RootId: root.Id, PolicyType: svcsdktypes.PolicyType(t)})
		rm.metrics.RecordAPICall("UPDATE", "DisablePolicyType", err)
		if err != nil && !isAWSError(err, "PolicyTypeNotEnabledException") {
			return err
		}
	}
	ko.Status.OwnedEnabledPolicyTypes = types.synced
	ko.Status.UnmanagedEnabledPolicyTypes = types.unmanaged

	current, err := rm.getServiceAccess(ctx)
	if err != nil {
		return err
	}
	access := planList(ko.Spec.AWSServiceAccessPrincipals, ko.Status.OwnedAWSServiceAccessPrincipals, current)
	for _, p := range access.enable {
		_, err = rm.sdkapi.EnableAWSServiceAccess(ctx, &svcsdk.EnableAWSServiceAccessInput{ServicePrincipal: aws.String(p)})
		rm.metrics.RecordAPICall("UPDATE", "EnableAWSServiceAccess", err)
		if err != nil {
			return err
		}
	}
	for _, p := range access.disable {
		_, err = rm.sdkapi.DisableAWSServiceAccess(ctx, &svcsdk.DisableAWSServiceAccessInput{ServicePrincipal: aws.String(p)})
		rm.metrics.RecordAPICall("UPDATE", "DisableAWSServiceAccess", err)
		if err != nil {
			return err
		}
	}
	ko.Status.OwnedAWSServiceAccessPrincipals = access.synced
	ko.Status.UnmanagedAWSServiceAccessPrincipals = access.unmanaged
	return nil
}

// plan is what one additive list needs, from the spec's entries (listed),
// the entries the object owns (status) and what is enabled in AWS.
type plan struct {
	// enable is what is listed and not enabled; disable is what is owned,
	// no longer listed, and still enabled.
	enable, disable []string
	// read is the spec's list as read: what is enabled of the listed and
	// owned entries. owned is the status as read: the listed and owned
	// entries that are enabled. synced is the status once enable and
	// disable have run: the listed entries.
	read, owned, synced []*string
	// unmanaged is enabled, not listed and not owned: kept, and reported
	// for import.
	unmanaged []*string
}

// planList is the additive rule for one list. An unset or empty spec list
// manages nothing (an empty list cannot be told from an unset one once it
// has been through the API types): nothing is enabled or disabled, the
// object owns nothing, and everything enabled is unmanaged. Otherwise
// listing an entry that is already enabled takes it on, so taking it off
// the list later disables it.
func planList(listed, owned, enabled []*string) plan {
	l, o, e := toSet(listed), toSet(owned), toSet(enabled)
	if len(l) == 0 {
		return plan{unmanaged: sorted(e, nil)}
	}
	return plan{
		enable:    keys(sorted(l, func(s string) bool { return !e[s] })),
		disable:   keys(sorted(o, func(s string) bool { return !l[s] && e[s] })),
		read:      sorted(e, func(s string) bool { return l[s] || o[s] }),
		owned:     sorted(e, func(s string) bool { return l[s] || o[s] }),
		synced:    sorted(l, nil),
		unmanaged: sorted(e, func(s string) bool { return !l[s] && !o[s] }),
	}
}

func toSet(ss []*string) map[string]bool {
	m := map[string]bool{}
	for _, s := range ss {
		if s != nil {
			m[*s] = true
		}
	}
	return m
}

// sorted is the set's members that keep accepts (all of them for nil),
// sorted; nil when there are none, so an empty status list is left out.
func sorted(set map[string]bool, keep func(string) bool) []*string {
	var out []string
	for s := range set {
		if keep == nil || keep(s) {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return aws.StringSlice(out)
}

func keys(ss []*string) []string { return aws.ToStringSlice(ss) }

// getRoot is the organization's root (an organization has one).
func (rm *resourceManager) getRoot(ctx context.Context) (*svcsdktypes.Root, error) {
	resp, err := rm.sdkapi.ListRoots(ctx, &svcsdk.ListRootsInput{})
	rm.metrics.RecordAPICall("READ_MANY", "ListRoots", err)
	if err != nil {
		return nil, err
	}
	if len(resp.Roots) == 0 {
		return nil, errors.New("the organization has no root")
	}
	return &resp.Roots[0], nil
}

// enabledPolicyTypes is the root's policy types that are enabled or being
// enabled, sorted.
func enabledPolicyTypes(root *svcsdktypes.Root) []*string {
	out := []*string{}
	for _, t := range root.PolicyTypes {
		if t.Status == svcsdktypes.PolicyTypeStatusEnabled || t.Status == svcsdktypes.PolicyTypeStatusPendingEnable {
			out = append(out, aws.String(string(t.Type)))
		}
	}
	sort.Slice(out, func(i, j int) bool { return *out[i] < *out[j] })
	return out
}

// getServiceAccess is the service principals with trusted access, sorted.
func (rm *resourceManager) getServiceAccess(ctx context.Context) ([]*string, error) {
	out := []*string{}
	var next *string
	for {
		resp, err := rm.sdkapi.ListAWSServiceAccessForOrganization(ctx, &svcsdk.ListAWSServiceAccessForOrganizationInput{NextToken: next})
		rm.metrics.RecordAPICall("READ_MANY", "ListAWSServiceAccessForOrganization", err)
		if err != nil {
			return nil, err
		}
		for _, p := range resp.EnabledServicePrincipals {
			out = append(out, p.ServicePrincipal)
		}
		if resp.NextToken == nil || *resp.NextToken == "" {
			break
		}
		next = resp.NextToken
	}
	sort.Slice(out, func(i, j int) bool { return aws.ToString(out[i]) < aws.ToString(out[j]) })
	return out, nil
}

// sameSet reports whether a and b hold the same strings, in any order and
// with any repeats.
func sameSet(a, b []*string) bool {
	add, remove := setDifference(a, b)
	return len(add) == 0 && len(remove) == 0
}

// setDifference is what is in want and not in have, and what is in have
// and not in want, each sorted and without repeats.
func setDifference(want, have []*string) (missing, extra []string) {
	w, h := map[string]bool{}, map[string]bool{}
	for _, s := range want {
		if s != nil {
			w[*s] = true
		}
	}
	for _, s := range have {
		if s != nil {
			h[*s] = true
		}
	}
	for s := range w {
		if !h[s] {
			missing = append(missing, s)
		}
	}
	for s := range h {
		if !w[s] {
			extra = append(extra, s)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

func isAWSError(err error, code string) bool {
	var awsErr smithy.APIError
	return errors.As(err, &awsErr) && awsErr.ErrorCode() == code
}
