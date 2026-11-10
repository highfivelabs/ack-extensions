// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// ack-iam writes one environment's IAM role for its ACK-generated
// managers, from its ACKContext (docs/plan.md, section 7.6):
//
//	go run ./hack/ack-iam --context team-a-prod.yaml --account 111122223333 \
//	    --cluster-arn arn:aws:eks:us-west-2:111122223333:cluster/platform --out iam/team-a-prod
//
// writes permissions-<n>.json, the permissions of every kind the context
// enables (pkg/ackiam: read from the generated code, plus the curated
// extras), split into as few managed policies as IAM's size limit allows,
// and trust.json, the role's trust policy for EKS Pod Identity: only the
// context's managers' ServiceAccounts, in the operator's namespace, of
// that cluster. --list prints every kind's actions instead.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	corev1alpha1 "github.com/highfivelabs/ack-extensions/apis/core/v1alpha1"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/iamgen"
	"github.com/highfivelabs/ack-extensions/pkg/ackiam"
	"github.com/highfivelabs/ack-extensions/pkg/operator"
)

func main() {
	var ctxFile, account, clusterARN, systemNS, out string
	list := flag.Bool("list", false, "print every kind's actions (Kind.service, actions, sensitive) and exit")
	flag.StringVar(&ctxFile, "context", "", "the environment's ACKContext, a YAML file")
	flag.StringVar(&account, "account", "", "the AWS account of the cluster")
	flag.StringVar(&clusterARN, "cluster-arn", "", "the EKS cluster's ARN")
	flag.StringVar(&systemNS, "system-namespace", corev1alpha1.DefaultSystemNamespace, "the operator's namespace, where the managers run")
	flag.StringVar(&out, "out", "", "the directory to write to")
	flag.Parse()
	kinds, err := ackiam.Scan(".")
	if err == nil {
		if *list {
			for _, k := range kinds {
				fmt.Printf("%s\t%s\t%s\n", k.Ref(), strings.Join(k.Actions(), " "), strings.Join(k.Sensitive(), " "))
			}
			return
		}
		err = run(kinds, ctxFile, account, clusterARN, systemNS, out)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ack-iam:", err)
		os.Exit(1)
	}
}

func run(all []ackiam.Kind, ctxFile, account, clusterARN, systemNS, out string) error {
	if ctxFile == "" || account == "" || clusterARN == "" || out == "" {
		return fmt.Errorf("needs --context, --account, --cluster-arn and --out")
	}
	b, err := os.ReadFile(ctxFile)
	if err != nil {
		return err
	}
	var ac corev1alpha1.ACKContext
	if err := yaml.UnmarshalStrict(b, &ac); err != nil {
		return fmt.Errorf("%s: %w", ctxFile, err)
	}
	if ac.Kind != "ACKContext" || ac.Namespace == "" {
		return fmt.Errorf("%s: not an ACKContext with a namespace", ctxFile)
	}
	var chosen []ackiam.Kind
	var sas, served []string
	for _, ctl := range ac.Spec.Controllers {
		if operator.OrganizationScoped[ctl.Service] {
			return fmt.Errorf("%s: refused per environment; its role is the organization's (docs/plan.md, section 7.5)", ctl.Service)
		}
		var have []string
		for _, k := range all {
			if k.Service == ctl.Service && (len(ctl.Kinds) == 0 || slices.Contains(ctl.Kinds, k.Kind)) {
				chosen = append(chosen, k)
				have = append(have, k.Kind)
				served = append(served, k.Ref())
			}
		}
		if len(have) == 0 {
			return fmt.Errorf("no kinds of service %q", ctl.Service)
		}
		for _, k := range ctl.Kinds {
			if !slices.Contains(have, k) {
				return fmt.Errorf("no such %s kind: %s", ctl.Service, k)
			}
		}
		sas = append(sas, corev1alpha1.ManagerName(ctl.Service, ac.Namespace))
	}
	slices.Sort(sas)
	docs, err := iamgen.Split(ackiam.Policy(chosen), iamgen.MaxManagedPolicySize)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	for i, d := range docs {
		b, _ := json.MarshalIndent(d, "", "  ")
		if err := os.WriteFile(filepath.Join(out, fmt.Sprintf("permissions-%d.json", i+1)), append(b, '\n'), 0o644); err != nil {
			return err
		}
	}
	trust := iamgen.PodIdentityTrust(iamgen.Trust{AccountID: account, ClusterARN: clusterARN, SystemNamespace: systemNS}, "EKSPodIdentityACKManagers", sas...)
	tb, _ := json.MarshalIndent(trust, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "trust.json"), append(tb, '\n'), 0o644); err != nil {
		return err
	}
	var sens []string
	for _, k := range chosen {
		sens = append(sens, k.Sensitive()...)
	}
	fmt.Printf("%s: %d kinds (%s); managers %s; %d permission policies and trust.json in %s; sensitive: %s\n",
		ac.Namespace, len(served), strings.Join(served, ", "), strings.Join(sas, ", "), len(docs), out, strings.Join(slices.Compact(slices.Sorted(slices.Values(sens))), ", "))
	return nil
}
