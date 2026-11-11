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

// The additive rule (A3): what someone else enabled is kept and reported;
// only what the object owns is disabled.
func TestPlanList(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		listed, owned, enabled         []*string
		enable, disable                []string
		read, synced, unmanaged, owner []string
	}{
		{
			name:   "an entry enabled outside Kubernetes is kept and reported",
			listed: strs("SCP"), owned: strs("SCP"), enabled: strs("BACKUP", "SCP"),
			read: []string{"SCP"}, owner: []string{"SCP"}, synced: []string{"SCP"}, unmanaged: []string{"BACKUP"},
		},
		{
			name:   "an owned entry taken off the list is disabled",
			listed: strs("SCP"), owned: strs("SCP", "TAG"), enabled: strs("BACKUP", "SCP", "TAG"),
			disable: []string{"TAG"},
			read:    []string{"SCP", "TAG"}, owner: []string{"SCP", "TAG"}, synced: []string{"SCP"}, unmanaged: []string{"BACKUP"},
		},
		{
			name:   "a listed entry that is missing is enabled",
			listed: strs("SCP", "TAG"), owned: strs("SCP", "TAG"), enabled: strs("SCP"),
			enable: []string{"TAG"},
			read:   []string{"SCP"}, owner: []string{"SCP"}, synced: []string{"SCP", "TAG"},
		},
		{
			name:   "listing an entry found enabled takes it on",
			listed: strs("BACKUP", "SCP"), owned: strs("SCP"), enabled: strs("BACKUP", "SCP"),
			read: []string{"BACKUP", "SCP"}, owner: []string{"BACKUP", "SCP"}, synced: []string{"BACKUP", "SCP"},
		},
		{
			name:   "an owned entry disabled outside Kubernetes and no longer listed is let go",
			listed: strs("SCP"), owned: strs("SCP", "TAG"), enabled: strs("SCP"),
			read: []string{"SCP"}, owner: []string{"SCP"}, synced: []string{"SCP"},
		},
		{
			name:   "an empty list manages nothing and owns nothing",
			listed: nil, owned: strs("SCP"), enabled: strs("BACKUP", "SCP"),
			unmanaged: []string{"BACKUP", "SCP"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := planList(tc.listed, tc.owned, tc.enabled)
			for what, pair := range map[string][2][]string{
				"enable":    {p.enable, tc.enable},
				"disable":   {p.disable, tc.disable},
				"read":      {aws.ToStringSlice(p.read), tc.read},
				"owned":     {aws.ToStringSlice(p.owned), tc.owner},
				"synced":    {aws.ToStringSlice(p.synced), tc.synced},
				"unmanaged": {aws.ToStringSlice(p.unmanaged), tc.unmanaged},
			} {
				if len(pair[0]) != 0 || len(pair[1]) != 0 {
					if !reflect.DeepEqual(pair[0], pair[1]) {
						t.Errorf("%s: got %v, want %v", what, pair[0], pair[1])
					}
				}
			}
		})
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
