// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The Terraform comparison: how much of the same denominator the Terraform
// AWS provider's writable attributes reach, estimated by the catalog
// repository (catalog/docs/terraform-leverage.md, "Method and error") and
// committed here as a pinned copy (docs/coverage/inputs/terraform/).
const (
	tfProvider   = "aws"
	tfRepo       = "https://github.com/highfivelabs/catalog"
	tfCatalogDir = "docs/terraform/ack-extensions"
	tfMethodURL  = "https://github.com/highfivelabs/catalog/blob/%s/docs/terraform-leverage.md#method-and-error"
	colTerraform = "#7b42bc" // purple
	colTick      = "#1f2328"
)

var tfFiles = []string{"terraform.csv", "terraform-resources.csv"}

type terraformSource struct {
	Repo       string   `json:"repo"`
	Commit     string   `json:"commit"`
	CommitDate string   `json:"commitDate"`
	Fetched    string   `json:"fetched"`
	Provider   string   `json:"provider"`
	Files      []tfFile `json:"files"`
}

type tfFile struct {
	Path   string `json:"path"`   // the committed copy, relative to the repository root
	From   string `json:"from"`   // the path in the catalog repository at Commit
	SHA256 string `json:"sha256"` // of the committed copy
}

// tfCounts is the Terraform estimate for a service or a roll-up: attributes
// of the denominator matched by the strict (L1) and lenient (L2) rule, out
// of total.
type tfCounts struct {
	total, l1, l2 int
}

func (t *tfCounts) add(o tfCounts) {
	t.total += o.total
	t.l1 += o.l1
	t.l2 += o.l2
}

// tfRow is one row of terraform.csv for the aws provider.
type tfRow struct {
	service, category string
	tfCounts
}

// loadTerraform reads the pinned terraform.csv, verifying every pinned
// file's checksum, and returns its aws rows in file order.
func loadTerraform(c config, s *sources) ([]tfRow, error) {
	if s.Terraform.Commit == "" || len(s.Terraform.Files) == 0 {
		return nil, fmt.Errorf("%s has no terraform pin", c.sourcesPath())
	}
	var main []byte
	for _, f := range s.Terraform.Files {
		b, err := os.ReadFile(filepath.Join(c.root, f.Path))
		if err != nil {
			return nil, err
		}
		if got := sha256hex(b); got != f.SHA256 {
			return nil, fmt.Errorf("%s has checksum %s, sources.yaml pins %s", f.Path, got, f.SHA256)
		}
		if filepath.Base(f.Path) == "terraform.csv" {
			main = b
		}
	}
	if main == nil {
		return nil, fmt.Errorf("%s: the terraform pin has no terraform.csv", c.sourcesPath())
	}
	return parseTerraformCSV(main, s.Terraform.Provider)
}

// parseTerraformCSV reads terraform.csv (columns in catalog/docs/terraform/README.md)
// by header name, keeping the rows of one provider.
func parseTerraformCSV(b []byte, provider string) ([]tfRow, error) {
	rows, err := csv.NewReader(bytes.NewReader(b)).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("terraform.csv: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("terraform.csv is empty")
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[h] = i
	}
	for _, h := range []string{"service", "provider", "category", "attributes_total", "attributes_terraform_l1", "attributes_terraform_l2"} {
		if _, ok := col[h]; !ok {
			return nil, fmt.Errorf("terraform.csv has no column %q", h)
		}
	}
	num := func(r []string, h string, line int) (int, error) {
		n, err := strconv.Atoi(r[col[h]])
		if err != nil {
			return 0, fmt.Errorf("terraform.csv line %d: %s: %w", line, h, err)
		}
		return n, nil
	}
	var out []tfRow
	for i, r := range rows[1:] {
		if r[col["provider"]] != provider {
			continue
		}
		t := tfRow{service: r[col["service"]], category: r[col["category"]]}
		var err error
		if t.total, err = num(r, "attributes_total", i+2); err != nil {
			return nil, err
		}
		if t.l1, err = num(r, "attributes_terraform_l1", i+2); err != nil {
			return nil, err
		}
		if t.l2, err = num(r, "attributes_terraform_l2", i+2); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("terraform.csv has no rows for provider %q", provider)
	}
	return out, nil
}

// pinTerraform copies the two files for this repository from a local
// catalog checkout at commit into docs/coverage/inputs/terraform/ and
// rewrites the terraform section of sources.yaml.
func pinTerraform(c config, commit string) error {
	dir := os.Getenv("CATALOG_DIR")
	if dir == "" {
		dir = filepath.Join(c.root, "..", "catalog")
	}
	git := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("git -C %s %s: %w: %s", dir, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
	full, err := git("rev-parse", commit+"^{commit}")
	if err != nil {
		return err
	}
	commit = strings.TrimSpace(string(full))
	date, err := git("log", "-1", "--format=%cI", commit)
	if err != nil {
		return err
	}
	s, err := loadSources(c)
	if err != nil {
		return err
	}
	s.Terraform = terraformSource{Repo: tfRepo, Commit: commit, CommitDate: strings.TrimSpace(string(date)),
		Fetched: time.Now().UTC().Format("2006-01-02"), Provider: tfProvider}
	for _, f := range tfFiles {
		from := tfCatalogDir + "/" + f
		b, err := git("show", commit+":"+from)
		if err != nil {
			return err
		}
		rel := "docs/coverage/inputs/terraform/" + f
		p := filepath.Join(c.root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
		s.Terraform.Files = append(s.Terraform.Files, tfFile{Path: rel, From: from, SHA256: sha256hex(b)})
	}
	return writeSources(c, s)
}

// tfBar draws the Terraform estimate at (x, y), w by h: a purple bar to the
// L2 share with a dark tick at the L1 share, on a grey track.
func tfBar(b *strings.Builder, t tfCounts, x, y, w, h, fontPx int) {
	fmt.Fprintf(b, `<rect x="%d" y="%d" width="%d" height="%d" rx="3" fill="%s"/>`+"\n", x, y, w, h, colNone)
	if t.total > 0 {
		pw := w * 10 * t.l2 / t.total
		if pw > 0 {
			fmt.Fprintf(b, `<rect x="%s" y="%d" width="%s" height="%d" fill="%s"/>`+"\n", tenths(x*10), y, tenths(pw), h, colTerraform)
		}
		tx := x*10 + w*10*t.l1/t.total
		fmt.Fprintf(b, `<rect x="%s" y="%d" width="2" height="%d" fill="%s"/>`+"\n", tenths(tx-10), y, h, colTick)
		if fontPx > 0 {
			label := "~" + pct1(t.l2, t.total) + "%"
			if pw/10 >= 8*len(label)+6 {
				fmt.Fprintf(b, `<text x="%s" y="%d" font-family="%s" font-size="%d" font-weight="600" fill="%s" text-anchor="middle" dominant-baseline="central">%s</text>`+"\n",
					tenths(x*10+pw/2), y+h/2+1, fontFamily, fontPx, colTextOn, label)
			}
		}
	}
	fmt.Fprintf(b, `<rect x="%d.5" y="%d.5" width="%d" height="%d" rx="3" fill="none" stroke="%s"/>`+"\n", x, y, w-1, h-1, colBorder)
}

// tfTitle describes the estimate for a title or alt text.
func tfTitle(t tfCounts) string {
	return fmt.Sprintf("Terraform aws provider (estimate): %s%% of %d attributes, at least %s%%", pct1(t.l2, t.total), t.total, pct1(t.l1, t.total))
}

// tfCell is a table cell: the L2 share with the L1 floor, or a dash.
func tfCell(t tfCounts, ok bool) string {
	if !ok || t.total == 0 {
		return "—"
	}
	return fmt.Sprintf("~%s%% (≥%s%%)", pct1(t.l2, t.total), pct1(t.l1, t.total))
}

// tfLegend draws the Terraform legend item at (x, y) and returns the x after it.
func tfLegend(b *strings.Builder, x, y int) int {
	const text = "Terraform aws, estimate (tick: strict floor)"
	fmt.Fprintf(b, `<rect x="%d.5" y="%d.5" width="11" height="11" rx="2" fill="%s" stroke="%s"/>`+"\n", x, y, colTerraform, colBorder)
	fmt.Fprintf(b, `<text x="%d" y="%d" font-family="%s" font-size="12" fill="%s" dominant-baseline="central">%s</text>`+"\n", x+17, y+6, fontFamily, colTextDark, html.EscapeString(text))
	return x + 17 + 7*len(text) + 18
}

// tfMethodLink is the catalog's "Method and error" section at the pin.
func tfMethodLink(s *sources) string { return fmt.Sprintf(tfMethodURL, s.Terraform.Commit) }
