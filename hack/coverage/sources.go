// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"sigs.k8s.io/yaml"
)

const (
	ackOrg    = "aws-controllers-k8s"
	cfnURL    = "https://schema.cloudformation.us-east-1.amazonaws.com/CloudformationSchema.zip"
	rawPrefix = "https://raw.githubusercontent.com/"
)

// sources is docs/coverage/sources.yaml: every input the numbers depend on.
type sources struct {
	CloudFormation cfnSource `json:"cloudformation"`
	ACK            ackSource `json:"ack"`
}

type cfnSource struct {
	URL          string `json:"url"`
	File         string `json:"file"` // the committed copy
	Region       string `json:"region"`
	Fetched      string `json:"fetched"`
	LastModified string `json:"lastModified"`
	SHA256       string `json:"sha256"`
	Bytes        int64  `json:"bytes"`
}

type ackSource struct {
	Org         string       `json:"org"`
	Resolved    string       `json:"resolved"`
	Controllers []controller `json:"controllers"`
	// NoRelease lists controller repositories with no published release (or
	// no released CRDs): they cover nothing officially.
	NoRelease []string `json:"noRelease"`
}

type controller struct {
	Name     string   `json:"name"` // the ACK service id: rds, ec2, ...
	Repo     string   `json:"repo"`
	Release  string   `json:"release"`
	Commit   string   `json:"commit"`
	Archived bool     `json:"archived,omitempty"`
	CRDs     []string `json:"crds"` // paths in the repository at Commit
}

func (c config) sourcesPath() string { return filepath.Join(c.outDir, "sources.yaml") }

func loadSources(c config) (*sources, error) {
	b, err := os.ReadFile(c.sourcesPath())
	if err != nil {
		return nil, fmt.Errorf("%w (run hack/coverage/run.sh --update-pins)", err)
	}
	var s sources
	if err := yaml.UnmarshalStrict(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", c.sourcesPath(), err)
	}
	return &s, nil
}

// fetch returns the body of url, cached under .cache/coverage/<key>.
func fetch(c config, key, url string) ([]byte, error) {
	p := filepath.Join(c.cache, key)
	if b, err := os.ReadFile(p); err == nil {
		return b, nil
	}
	var lastErr error
	for attempt := 1; attempt <= 4; attempt++ {
		resp, err := http.Get(url)
		if err == nil {
			b, rerr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if rerr == nil && resp.StatusCode == 200 {
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					return nil, err
				}
				if err := os.WriteFile(p, b, 0o644); err != nil {
					return nil, err
				}
				return b, nil
			}
			if rerr != nil {
				lastErr = rerr
			} else {
				lastErr = fmt.Errorf("GET %s: %s", url, resp.Status)
			}
		} else {
			lastErr = err
		}
		time.Sleep(time.Duration(attempt) * 2 * time.Second)
	}
	return nil, lastErr
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (c config) zipPath() string {
	return filepath.Join(c.outDir, "inputs", "CloudformationSchema.zip")
}

// cfnZip returns the pinned CloudFormation schema zip, verifying its
// checksum. AWS rebuilds the file at the URL every hour, so the pinned copy
// is committed (docs/coverage/inputs/): the URL cannot reproduce it.
func cfnZip(c config, s *sources) ([]byte, error) {
	b, err := os.ReadFile(c.zipPath())
	if err != nil {
		return nil, err
	}
	if got := sha256hex(b); got != s.CloudFormation.SHA256 {
		return nil, fmt.Errorf("%s has checksum %s, sources.yaml pins %s", c.zipPath(), got, s.CloudFormation.SHA256)
	}
	return b, nil
}

// cfnSchemas reads every *.json in the zip, in file-name order.
func cfnSchemas(zipBytes []byte) (names []string, files map[string][]byte, err error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, nil, err
	}
	files = map[string][]byte{}
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".json") || strings.HasPrefix(path.Base(f.Name), "provider") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, nil, err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, nil, err
		}
		files[f.Name] = b
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return names, files, nil
}

func gh(args ...string) ([]byte, error) {
	cmd := exec.Command("gh", append([]string{"api"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh api %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// updatePins resolves the latest release of every ACK controller and the
// current CloudFormation schema zip, and writes sources.yaml. Read-only
// against GitHub and AWS.
func updatePins(c config) error {
	today := time.Now().UTC().Format("2006-01-02")
	s := &sources{}

	// CloudFormation schemas.
	resp, err := http.Get(cfnURL)
	if err != nil {
		return err
	}
	zb, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: %s", cfnURL, resp.Status)
	}
	s.CloudFormation = cfnSource{
		URL: cfnURL, File: "docs/coverage/inputs/CloudformationSchema.zip", Region: "us-east-1", Fetched: today,
		LastModified: resp.Header.Get("Last-Modified"),
		SHA256:       sha256hex(zb), Bytes: int64(len(zb)),
	}
	if err := os.MkdirAll(filepath.Dir(c.zipPath()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(c.zipPath(), zb, 0o644); err != nil {
		return err
	}

	// ACK controllers.
	out, err := gh("orgs/"+ackOrg+"/repos", "--paginate", "-q", `.[] | select(.name|endswith("-controller")) | [.name,.archived]|@tsv`)
	if err != nil {
		return err
	}
	type repo struct {
		name     string
		archived bool
	}
	var repos []repo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) == 2 {
			repos = append(repos, repo{f[0], f[1] == "true"})
		}
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].name < repos[j].name })

	type result struct {
		ctl       *controller
		noRelease string
		err       error
	}
	results := make([]result, len(repos))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, r := range repos {
		wg.Add(1)
		go func(i int, r repo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ctl, err := resolveController(r.name, r.archived)
			if err != nil {
				results[i] = result{err: err}
				return
			}
			if ctl == nil {
				results[i] = result{noRelease: r.name}
				return
			}
			results[i] = result{ctl: ctl}
		}(i, r)
	}
	wg.Wait()
	s.ACK = ackSource{Org: ackOrg, Resolved: today}
	for _, r := range results {
		switch {
		case r.err != nil:
			return r.err
		case r.ctl != nil:
			s.ACK.Controllers = append(s.ACK.Controllers, *r.ctl)
		default:
			s.ACK.NoRelease = append(s.ACK.NoRelease, r.noRelease)
		}
	}
	b, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	hdr := "# Written by hack/coverage/run.sh --update-pins; read by the generator. Every number in\n# docs/coverage/ comes from exactly these inputs plus this repository's CRDs.\n"
	if err := os.MkdirAll(c.outDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(c.sourcesPath(), append([]byte(hdr), b...), 0o644)
}

func resolveController(repoName string, archived bool) (*controller, error) {
	full := ackOrg + "/" + repoName
	out, err := gh("repos/"+full+"/releases/latest", "-q", ".tag_name")
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return nil, nil // no release
		}
		return nil, err
	}
	tag := strings.TrimSpace(string(out))
	sha, err := gh("repos/"+full+"/commits/"+tag, "-q", ".sha")
	if err != nil {
		return nil, err
	}
	commit := strings.TrimSpace(string(sha))
	tree, err := gh("repos/"+full+"/git/trees/"+commit+"?recursive=1", "-q", `.tree[]|select(.type=="blob")|.path`)
	if err != nil {
		return nil, err
	}
	var crds []string
	for _, p := range strings.Split(strings.TrimSpace(string(tree)), "\n") {
		if strings.HasPrefix(p, "helm/crds/") && strings.HasSuffix(p, ".yaml") && !strings.HasPrefix(path.Base(p), "services.k8s.aws_") {
			crds = append(crds, p)
		}
	}
	if len(crds) == 0 {
		// A release with no helm/crds: look in config/crd/bases.
		for _, p := range strings.Split(strings.TrimSpace(string(tree)), "\n") {
			if strings.HasPrefix(p, "config/crd/bases/") && strings.HasSuffix(p, ".yaml") && !strings.HasPrefix(path.Base(p), "services.k8s.aws_") {
				crds = append(crds, p)
			}
		}
	}
	if len(crds) == 0 {
		return nil, nil
	}
	sort.Strings(crds)
	return &controller{
		Name: strings.TrimSuffix(repoName, "-controller"), Repo: repoName,
		Release: tag, Commit: commit, Archived: archived, CRDs: crds,
	}, nil
}

// crdBytes returns one released CRD file of a controller.
func crdBytes(c config, ctl controller, p string) ([]byte, error) {
	key := fmt.Sprintf("ack/%s/%s/%s", ctl.Repo, ctl.Commit, path.Base(p))
	return fetch(c, key, rawPrefix+ackOrg+"/"+ctl.Repo+"/"+ctl.Commit+"/"+p)
}

// jsonDecode is json.Unmarshal with numbers kept as float64.
func jsonDecode(b []byte, v any) error { return json.Unmarshal(b, v) }
