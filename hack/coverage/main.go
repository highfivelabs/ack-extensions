// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Command coverage measures how much of AWS the ACK controllers (official)
// and the kinds in this repository (ours) cover, and writes docs/coverage/.
//
//	hack/coverage/run.sh               regenerate docs/coverage/ and the README section
//	hack/coverage/run.sh --check       fail if the committed outputs differ from a regeneration
//	hack/coverage/run.sh --update-pins re-resolve the pinned inputs (network, gh) into sources.yaml
//	hack/coverage/run.sh -terraform-pin COMMIT  pin the catalog's Terraform estimate at COMMIT
//
// See docs/coverage/README.md for the method.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	root := flag.String("root", ".", "repository root")
	check := flag.Bool("check", false, "fail if the committed outputs differ from a regeneration from the pinned inputs")
	update := flag.Bool("update-pins", false, "resolve the latest ACK releases and the CloudFormation schema zip, write docs/coverage/sources.yaml, then regenerate")
	tfPin := flag.String("terraform-pin", "", "copy docs/terraform/ack-extensions/ of the catalog repository at this commit (a local checkout: $CATALOG_DIR, default ../catalog) into docs/coverage/inputs/terraform/, pin it in sources.yaml, then regenerate")
	explainRes := flag.String("explain", "", "print how one resource (service/Name, e.g. rds/DBInstance) was matched, and exit")
	flag.Parse()

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		fatal(err)
	}
	cfg := config{
		root:    absRoot,
		cache:   filepath.Join(absRoot, ".cache", "coverage"),
		outDir:  filepath.Join(absRoot, "docs", "coverage"),
		mapping: filepath.Join(absRoot, "hack", "coverage", "mappings.yaml"),
	}

	if *update {
		if err := updatePins(cfg); err != nil {
			fatal(err)
		}
	}
	if *tfPin != "" {
		if err := pinTerraform(cfg, *tfPin); err != nil {
			fatal(err)
		}
	}
	if *explainRes != "" {
		m, err := build(cfg)
		if err != nil {
			fatal(err)
		}
		if err := explain(os.Stdout, m, m.compute(), *explainRes); err != nil {
			fatal(err)
		}
		return
	}
	if err := run(cfg, *check); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "coverage:", err)
	os.Exit(1)
}

type config struct {
	root    string
	cache   string
	outDir  string
	mapping string
}
