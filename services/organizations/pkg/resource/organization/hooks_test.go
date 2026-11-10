// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package organization

import (
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/organizations/apis/v1alpha1"
)

func strs(s ...string) []*string { return aws.StringSlice(s) }

func TestSetDifference(t *testing.T) {
	add, remove := setDifference(strs("TAG_POLICY", "SERVICE_CONTROL_POLICY", "TAG_POLICY"), strs("SERVICE_CONTROL_POLICY", "BACKUP_POLICY"))
	if !reflect.DeepEqual(add, []string{"TAG_POLICY"}) || !reflect.DeepEqual(remove, []string{"BACKUP_POLICY"}) {
		t.Fatalf("add %v, remove %v", add, remove)
	}
	if !sameSet(strs("b", "a", "a"), strs("a", "b")) {
		t.Fatal("order and repeats are not a difference")
	}
}

func org(types, principals []*string) *resource {
	return &resource{ko: &svcapitypes.Organization{Spec: svcapitypes.OrganizationSpec{EnabledPolicyTypes: types, AWSServiceAccessPrincipals: principals}}}
}

func TestCustomPreCompare(t *testing.T) {
	d := newResourceDelta(org(strs("TAG_POLICY", "SERVICE_CONTROL_POLICY"), nil), org(strs("SERVICE_CONTROL_POLICY", "TAG_POLICY"), strs("sso.amazonaws.com")))
	if len(d.Differences) != 0 {
		t.Fatalf("same sets, and an unmanaged list: %v", d.Differences)
	}
	d = newResourceDelta(org(strs("SERVICE_CONTROL_POLICY"), strs("sso.amazonaws.com")), org(strs("SERVICE_CONTROL_POLICY", "TAG_POLICY"), nil))
	if !d.DifferentAt("Spec.EnabledPolicyTypes") || !d.DifferentAt("Spec.AWSServiceAccessPrincipals") {
		t.Fatalf("want both lists different: %v", d.Differences)
	}
}

func TestEnabledPolicyTypes(t *testing.T) {
	root := &svcsdktypes.Root{PolicyTypes: []svcsdktypes.PolicyTypeSummary{
		{Type: svcsdktypes.PolicyTypeTagPolicy, Status: svcsdktypes.PolicyTypeStatusEnabled},
		{Type: svcsdktypes.PolicyTypeBackupPolicy, Status: svcsdktypes.PolicyTypeStatusPendingDisable},
		{Type: svcsdktypes.PolicyTypeServiceControlPolicy, Status: svcsdktypes.PolicyTypeStatusPendingEnable},
	}}
	if got := aws.ToStringSlice(enabledPolicyTypes(root)); !reflect.DeepEqual(got, []string{"SERVICE_CONTROL_POLICY", "TAG_POLICY"}) {
		t.Fatalf("got %v", got)
	}
}
