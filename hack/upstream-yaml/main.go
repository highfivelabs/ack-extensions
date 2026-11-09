// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Command upstream-yaml checks upstream.yaml on its own (pins, pull
// requests, patches, copied files; that the patch files and copied paths
// exist) and against UPSTREAM.md's tables: `task upstream:yaml-check`.
//
// pins/pins.go is a copy of highfivelabs/factory's internal/pins/pins.go
// (the factory's daily watch reads upstream.yaml with the same code);
// change it there and copy it here. This is its own Go module, so it
// builds without the upstream checkout the repository's module needs.
//
//	go run . [REPO_ROOT]   (default ../..)
package main

import (
	"fmt"
	"os"

	"upstreamyaml/pins"
)

func main() {
	root := "../.."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	p, err := pins.Check(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "upstream-yaml:", err)
		os.Exit(1)
	}
	if len(p) > 0 {
		fmt.Fprintln(os.Stderr, "upstream.yaml and UPSTREAM.md disagree; change both together:")
		for _, s := range p {
			fmt.Fprintln(os.Stderr, "  "+s)
		}
		os.Exit(1)
	}
	fmt.Println("ok: upstream.yaml agrees with UPSTREAM.md")
}
