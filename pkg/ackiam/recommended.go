// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package ackiam

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// ACK's own controllers per environment (docs/plan.md, section 7.7): the
// role of an environment whose ACKContext runs ACK's controller of a
// service also gets ACK's own IAM recommendation for it, copied at the pin
// by hack/ack-controllers.sh into pkg/ackiam/recommended/<service>/:
// recommended-policy-arn (AWS managed policies, one ARN per line) and
// recommended-inline-policy (a policy document), as ACK ships them.

// RecommendedDir is where the copies are, under the repository root.
const RecommendedDir = "pkg/ackiam/recommended"

// ACKRoleDir is where ACK's controllers' ClusterRoles are, under the
// repository root (config/ack-controllers).
const ACKRoleDir = "config/ack-controllers"

// Recommendation is ACK's IAM recommendation for its controller of one
// service.
type Recommendation struct {
	Service string
	// ManagedPolicyARNs are the AWS managed policies to attach.
	ManagedPolicyARNs []string
	// InlinePolicy is the policy document, as ACK wrote it; nil if none.
	InlinePolicy []byte
}

// ACKServices are the services whose ACK controller has a recommendation
// here, sorted.
func ACKServices(root string) ([]string, error) {
	es, err := os.ReadDir(filepath.Join(root, RecommendedDir))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range es {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	slices.Sort(out)
	return out, nil
}

// Recommended reads ACK's recommendation for its controller of service;
// ok is false when there is none (the operator runs no ACK controller of
// that service).
func Recommended(root, service string) (rec Recommendation, ok bool, err error) {
	dir := filepath.Join(root, RecommendedDir, service)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return rec, false, nil
	}
	rec.Service = service
	if b, err := os.ReadFile(filepath.Join(dir, "recommended-policy-arn")); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				if !strings.HasPrefix(l, "arn:aws:iam::aws:policy/") {
					return rec, false, fmt.Errorf("%s: %q is not an AWS managed policy", service, l)
				}
				rec.ManagedPolicyARNs = append(rec.ManagedPolicyARNs, l)
			}
		}
	} else if !os.IsNotExist(err) {
		return rec, false, err
	}
	if b, err := os.ReadFile(filepath.Join(dir, "recommended-inline-policy")); err == nil {
		rec.InlinePolicy = b
	} else if !os.IsNotExist(err) {
		return rec, false, err
	}
	if len(rec.ManagedPolicyARNs) == 0 && rec.InlinePolicy == nil {
		return rec, false, fmt.Errorf("%s: %s has no recommendation", service, dir)
	}
	return rec, true, nil
}

// ACKResources are the resources (plural, lower case) of ACK's own kinds
// of service, read from the rules of its ClusterRole on
// <service>.services.k8s.aws, sorted.
func ACKResources(root, service string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(root, ACKRoleDir, service+"-controller-role.yaml"))
	if err != nil {
		return nil, err
	}
	var role rbacv1.ClusterRole
	if err := yaml.Unmarshal(b, &role); err != nil {
		return nil, fmt.Errorf("%s: %w", service, err)
	}
	var out []string
	for _, r := range role.Rules {
		if !slices.Contains(r.APIGroups, service+".services.k8s.aws") {
			continue
		}
		for _, res := range r.Resources {
			if !strings.Contains(res, "/") {
				out = append(out, res)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// IsACKKind says whether kind is one of ACK's kinds of service, by its
// resource name in resources (ACKResources): the kind in lower case and
// made plural as Kubernetes' generators do (Bucket: buckets, Policy:
// policies, Alias: aliases).
func IsACKKind(resources []string, kind string) bool {
	k := strings.ToLower(kind)
	cands := []string{k + "s", k + "es"}
	if strings.HasSuffix(k, "y") {
		cands = append(cands, strings.TrimSuffix(k, "y")+"ies")
	}
	for _, c := range cands {
		if slices.Contains(resources, c) {
			return true
		}
	}
	return false
}
