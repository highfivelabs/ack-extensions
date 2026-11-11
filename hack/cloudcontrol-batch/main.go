// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Command cloudcontrol-batch adds a batch of gap types to
// cloudcontrol/types.yaml in one run (docs/throughput.md): every type of the
// services named that the readiness survey (cloudcontrol/readiness.csv)
// calls a gap (no ACK kind, none of ours), provisionable and clean, and that
// cloudcontrol/excluded.yaml does not exclude. Each is marked unproven with
// what moto 5.2.5 has of its service (moto-services.txt), so it runs on the
// schema fake (TestEveryKindOnSchemaFake) until someone writes a moto
// backend. It prints each type's write-only strings, the candidates for
// `secrets:`, which stay a decision per field.
//
//	go run ./hack/cloudcontrol-batch --services macie,detective --note "F18: ..."
//	task cloudcontrol:generate && task coverage
package main

import (
	"archive/zip"
	"bufio"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/cfn"
)

// motoName is moto's module for a CloudFormation service, where the two
// differ.
var motoName = map[string]string{
	"certificatemanager":     "acm",
	"inspectorv2":            "inspector2",
	"macie":                  "macie2",
	"sso":                    "ssoadmin",
	"elasticloadbalancingv2": "elbv2",
	"lambda":                 "awslambda",
	"events":                 "events",
}

func main() {
	root := flag.String("root", ".", "the repository root")
	services := flag.String("services", "", "CloudFormation services, lower case, comma-separated (macie,detective)")
	types := flag.String("types", "", "or CloudFormation types, comma-separated")
	note := flag.String("note", "", "the comment above the batch in types.yaml")
	dry := flag.Bool("dry-run", false, "print the batch, change nothing")
	flag.Parse()
	if err := run(*root, *services, *types, *note, *dry); err != nil {
		fmt.Fprintln(os.Stderr, "cloudcontrol-batch:", err)
		os.Exit(1)
	}
}

func set(csvList string) map[string]bool {
	out := map[string]bool{}
	for _, s := range strings.Split(csvList, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out[s] = true
		}
	}
	return out
}

func run(root, services, types, note string, dry bool) error {
	wantSvc, wantType := set(services), set(types)
	if len(wantSvc) == 0 && len(wantType) == 0 {
		return fmt.Errorf("name --services or --types")
	}
	var excluded map[string]string
	if b, err := os.ReadFile(filepath.Join(root, "cloudcontrol", "excluded.yaml")); err == nil {
		var ex struct {
			Types map[string]string `json:"types"`
		}
		if err := yaml.Unmarshal(b, &ex); err != nil {
			return fmt.Errorf("excluded.yaml: %w", err)
		}
		excluded = ex.Types
	}
	moto := map[string]bool{}
	mf, err := os.Open(filepath.Join(root, "hack", "cloudcontrol-batch", "moto-services.txt"))
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(mf)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
			moto[l] = true
		}
	}
	mf.Close()

	typesYAML := filepath.Join(root, "cloudcontrol", "types.yaml")
	cur, err := os.ReadFile(typesYAML)
	if err != nil {
		return err
	}
	var cfg struct {
		Types []struct {
			TypeName string `json:"typeName"`
		} `json:"types"`
	}
	if err := yaml.Unmarshal(cur, &cfg); err != nil {
		return err
	}
	have := map[string]bool{}
	for _, t := range cfg.Types {
		have[t.TypeName] = true
	}

	f, err := os.Open(filepath.Join(root, "cloudcontrol", "readiness.csv"))
	if err != nil {
		return err
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return err
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[h] = i
	}
	zr, err := zip.OpenReader(filepath.Join(root, "docs", "coverage", "inputs", "CloudformationSchema.zip"))
	if err != nil {
		return err
	}
	defer zr.Close()
	files := map[string]*zip.File{}
	for _, zf := range zr.File {
		files[strings.ToLower(zf.Name)] = zf
	}

	var b strings.Builder
	if note != "" {
		for _, l := range strings.Split(note, "\n") {
			fmt.Fprintf(&b, "  # %s\n", l)
		}
	}
	var added, skipped []string
	for _, r := range rows[1:] {
		get := func(c string) string { return r[col[c]] }
		typ, svc := get("type"), get("service")
		if !wantSvc[svc] && !wantType[typ] {
			continue
		}
		why := ""
		switch {
		case have[typ]:
			why = "already in types.yaml"
		case get("ack_kinds") != "" || get("ours_kinds") != "":
			why = "has a kind: " + get("ack_kinds") + get("ours_kinds")
		case get("provisioning") == "NON_PROVISIONABLE":
			why = "not provisionable through Cloud Control"
		case get("crd") != "ok" || get("lossy_nodes") != "0" || get("cel_skipped") != "0":
			why = fmt.Sprintf("CRD not clean (crd %s, lossy %s, CEL skipped %s)", get("crd"), get("lossy_nodes"), get("cel_skipped"))
		case excluded[typ] != "":
			why = "excluded: " + excluded[typ]
		}
		if why != "" {
			skipped = append(skipped, typ+": "+why)
			continue
		}
		m := svc
		if n, ok := motoName[svc]; ok {
			m = n
		}
		reason := fmt.Sprintf("moto 5.2.5 has no %s; on the schema fake only", m)
		if moto[m] {
			reason = fmt.Sprintf("no moto backend yet (moto 5.2.5 has %s); on the schema fake only", m)
		}
		fmt.Fprintf(&b, "  - typeName: %s\n    unproven: %q\n", typ, reason)
		added = append(added, typ)
		if zf := files[strings.ToLower(strings.ReplaceAll(typ, "::", "-"))+".json"]; zf != nil {
			rc, err := zf.Open()
			if err != nil {
				return err
			}
			body, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return err
			}
			s, err := cfn.Parse(body)
			if err != nil {
				return fmt.Errorf("%s: %w", typ, err)
			}
			if c := s.SecretCandidates(); len(c) > 0 {
				fmt.Printf("secret candidates %s: %s\n", typ, strings.Join(c, " "))
			}
		}
	}
	sort.Strings(skipped)
	for _, s := range skipped {
		fmt.Println("skip", s)
	}
	fmt.Printf("%d types added\n", len(added))
	if dry || len(added) == 0 {
		fmt.Print(b.String())
		return nil
	}
	out := strings.TrimRight(string(cur), "\n") + "\n" + b.String()
	return os.WriteFile(typesYAML, []byte(out), 0o644)
}
