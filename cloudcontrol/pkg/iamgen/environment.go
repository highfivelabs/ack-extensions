// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package iamgen

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/apis/v1alpha1"
)

// MaxManagedPolicySize is IAM's limit on a customer managed policy
// document, in characters not counting white space.
const MaxManagedPolicySize = 6144

// Environment is the permission policy of one environment's role: the
// Cloud Control actions once, then each enabled type's handler statements
// (ForType), split into as many managed policy documents as IAM's size
// limit needs, in order, so a role with a modest set of kinds needs one
// or two. The builder (2026-11-09): "the scoping should be at the
// environment level, so each environment gets an identity attached".
func Environment(results []Result, limit int) ([]Policy, error) {
	return Split(Merge(results), limit)
}

// Split is a policy's statements, in order, in as few documents as IAM's
// size limit allows; a statement is never split. The ACK-generated
// managers' environment policies (pkg/ackiam) use it too.
func Split(merged Policy, limit int) ([]Policy, error) {
	var out []Policy
	cur := Policy{Version: "2012-10-17"}
	for _, st := range merged.Statement {
		next := cur
		next.Statement = append(append([]Statement(nil), cur.Statement...), st)
		if size(next) <= limit {
			cur = next
			continue
		}
		if len(cur.Statement) == 0 {
			return nil, fmt.Errorf("statement %s alone is %d characters, over the limit of %d", st.Sid, size(next), limit)
		}
		out = append(out, cur)
		cur = Policy{Version: "2012-10-17", Statement: []Statement{st}}
		if size(cur) > limit {
			return nil, fmt.Errorf("statement %s alone is %d characters, over the limit of %d", st.Sid, size(cur), limit)
		}
	}
	if len(cur.Statement) > 0 {
		out = append(out, cur)
	}
	return out, nil
}

// size is a policy's length as IAM counts it: compact JSON, no white space
// outside strings.
func size(p Policy) int {
	b, _ := json.Marshal(p)
	return len(b)
}

// Trust is what an environment role's trust policy names.
type Trust struct {
	// AccountID is the account the cluster runs in (aws:SourceAccount).
	AccountID string
	// ClusterARN is the EKS cluster's ARN (aws:SourceArn).
	ClusterARN string
	// Environment is the environment's namespace.
	Environment string
	// SystemNamespace is the operator's namespace, where the environment's
	// manager runs (v1alpha1.DefaultSystemNamespace when empty).
	SystemNamespace string
}

// TrustPolicy is an environment role's trust policy for EKS Pod Identity:
// the Pod Identity service may assume it, and tag the session, only for
// the cluster named, and only when the session tags Pod Identity sets name
// the operator's namespace and this environment's manager ServiceAccount.
// So an association made by mistake for another ServiceAccount (another
// environment's manager, a pod in the environment's own namespace), or in
// another cluster, cannot use the role. The tags are also in CloudTrail,
// so every call is attributed to the environment's manager.
func TrustPolicy(t Trust) map[string]any {
	return PodIdentityTrust(t, "EKSPodIdentityCloudControlManager", v1alpha1.ManagerName(t.Environment))
}

// PodIdentityTrust is a trust policy for EKS Pod Identity that lets only
// the ServiceAccounts named, in the operator's namespace of the cluster
// named, use the role (t.Environment is not read). One ServiceAccount is
// a string, several a list, as IAM writes them.
func PodIdentityTrust(t Trust, sid string, serviceAccounts ...string) map[string]any {
	if t.SystemNamespace == "" {
		t.SystemNamespace = v1alpha1.DefaultSystemNamespace
	}
	var sa any = serviceAccounts
	if len(serviceAccounts) == 1 {
		sa = serviceAccounts[0]
	}
	return map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{map[string]any{
			"Sid":       sid,
			"Effect":    "Allow",
			"Principal": map[string]any{"Service": "pods.eks.amazonaws.com"},
			"Action":    []string{"sts:AssumeRole", "sts:TagSession"},
			"Condition": map[string]any{
				"StringEquals": map[string]any{
					"aws:SourceAccount":                         t.AccountID,
					"aws:RequestTag/kubernetes-namespace":       t.SystemNamespace,
					"aws:RequestTag/kubernetes-service-account": sa,
				},
				"ArnEquals": map[string]any{"aws:SourceArn": t.ClusterARN},
			},
		}},
	}
}

// Compact is the same permissions in fewer characters: every Allow
// statement on Resource "*" (all ForType writes) folded into one statement
// per service, its actions deduplicated and sorted; any other statement is
// kept as it is. Nothing is widened: the set of allowed (action, resource)
// pairs is unchanged. The live-test role uses it (hack/cloudcontrol-gen,
// live.go), whose policies cover every type and would otherwise pass IAM's
// ten managed policies per role.
func Compact(p Policy) Policy {
	out := Policy{Version: p.Version}
	byService := map[string][]string{}
	var services []string
	for _, st := range p.Statement {
		if st.Effect != "Allow" || st.Resource != "*" {
			out.Statement = append(out.Statement, st)
			continue
		}
		for _, a := range st.Action {
			svc, _, ok := strings.Cut(a, ":")
			if !ok || svc == "" {
				svc = "-"
			}
			if _, ok := byService[svc]; !ok {
				services = append(services, svc)
			}
			byService[svc] = append(byService[svc], a)
		}
	}
	sort.Strings(services)
	for _, svc := range services {
		out.Statement = append(out.Statement, Statement{
			Sid: "Service" + sid(strings.ToUpper(svc[:1])+svc[1:]), Effect: "Allow",
			Action: dedupe(byService[svc]), Resource: "*",
		})
	}
	return out
}
