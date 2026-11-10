// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package iamgen

import (
	"encoding/json"
	"fmt"

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
	merged := Merge(results)
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
	if t.SystemNamespace == "" {
		t.SystemNamespace = v1alpha1.DefaultSystemNamespace
	}
	return map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{map[string]any{
			"Sid":       "EKSPodIdentityCloudControlManager",
			"Effect":    "Allow",
			"Principal": map[string]any{"Service": "pods.eks.amazonaws.com"},
			"Action":    []string{"sts:AssumeRole", "sts:TagSession"},
			"Condition": map[string]any{
				"StringEquals": map[string]any{
					"aws:SourceAccount":                         t.AccountID,
					"aws:RequestTag/kubernetes-namespace":       t.SystemNamespace,
					"aws:RequestTag/kubernetes-service-account": v1alpha1.ManagerName(t.Environment),
				},
				"ArnEquals": map[string]any{"aws:SourceArn": t.ClusterARN},
			},
		}},
	}
}
