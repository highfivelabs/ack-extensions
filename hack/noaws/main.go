// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// noaws keeps every test path off real AWS (docs/cloudcontrol.md section 6).
//
//	go run ./hack/noaws [root]            the static check task check runs
//	go run ./hack/noaws env URL [HOST...]  a fake endpoint's environment (NAME=value lines) for kubectl set env
//	go run ./hack/noaws verify-deployments  every AWS client in `kubectl get deploy -A -o json` (stdin) is verified
//
// The static check fails if anything in a test path could reach a real AWS
// endpoint without going through pkg/fakeaws:
//
//   - G1: an SDK configuration made by hand (an aws.Config literal,
//     config.LoadDefaultConfig), or a guarded one's HTTPClient or
//     BaseEndpoint changed: SDK clients come from fakeaws.Config;
//   - G2: a process started (os/exec) other than `go`, whose environment
//     is not set from pkg/fakeaws in the same function (fakeaws.Command
//     does both);
//   - G3: net/http's default client or transport (http.Get and the like):
//     fakeaws.HTTPClient dials loopback only;
//   - G4: an AWS endpoint written out (a URL under an AWS domain, or a
//     regional AWS host name), except an XML namespace; tests that show a
//     real endpoint is refused use fakeaws.RealEndpoint;
//   - S1: in the shell scripts under tests/, Python (which runs boto3) run
//     other than through fake_py, which gives it an environment built from
//     nothing with the dead proxy, or the AWS CLI run at all;
//   - L1: the live tests (docs/live-runs.md) stay apart: only the tasks
//     test-live, live-sweep and live-list mention hack/live or -tags live,
//     no other task calls them (go vet -tags live, which compiles and runs
//     nothing, is allowed), and the Taskfile sets
//     CLOUDCONTROL_E2E_TARGET to fake for every task;
//   - L2: Go files built only with -tags live (the only code that may reach
//     real AWS) live only in cloudcontrol/test/live, hack/live and
//     cloudcontrol/test/e2e/live_test.go; the rules above skip them, since
//     no task check builds them.
//
// Test paths are every _test.go file and everything under pkg/test,
// tests, cloudcontrol/test and config/fake-aws. pkg/fakeaws (the guard
// itself) and hack/noaws/testdata are not scanned.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/highfivelabs/ack-extensions/pkg/fakeaws"
	"github.com/highfivelabs/ack-extensions/pkg/operator"
)

func main() {
	args := os.Args[1:]
	var err error
	switch {
	case len(args) > 0 && args[0] == "env":
		err = printEnv(args[1:])
	case len(args) > 0 && args[0] == "verify-deployments":
		err = verifyDeployments(os.Stdin, os.Stdout)
	default:
		root := "."
		if len(args) > 0 {
			root = args[0]
		}
		var r *Report
		if r, err = Scan(root); err == nil {
			fmt.Printf("noaws: %d Go files and %d other files in test paths scanned; %d live-only Go files, all in their place; the Taskfiles checked\n", r.GoFiles, r.OtherFiles, r.LiveFiles)
			if len(r.Findings) > 0 {
				for _, f := range r.Findings {
					fmt.Println(f)
				}
				err = fmt.Errorf("%d places in test paths could reach real AWS; go through pkg/fakeaws (hack/noaws/main.go says how)", len(r.Findings))
			} else if r.GoFiles == 0 {
				err = fmt.Errorf("no Go test files found under %s: refusing to pass vacuously", root)
			}
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "noaws:", err)
		os.Exit(1)
	}
}

func printEnv(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: noaws env URL [NO_PROXY host...]")
	}
	e, err := fakeaws.Parse(args[0], fakeaws.Cluster)
	if err != nil {
		return err
	}
	for _, kv := range e.Env(args[1:]...) {
		fmt.Println(kv)
	}
	return nil
}

// verifyDeployments checks every container that is an AWS client (an
// --aws-region argument, or AWS_REGION or AWS_ENDPOINT_URL in its
// environment) and fails if one could reach real AWS, or if there is none.
func verifyDeployments(in io.Reader, out io.Writer) error {
	var list appsv1.DeploymentList
	if err := json.NewDecoder(in).Decode(&list); err != nil {
		return fmt.Errorf("reading the Deployments: %w", err)
	}
	var checked, errs []string
	for _, d := range list.Items {
		for i := range d.Spec.Template.Spec.Containers {
			c := &d.Spec.Template.Spec.Containers[i]
			if !awsClient(c.Args, c.Env) {
				continue
			}
			name := d.Namespace + "/" + d.Name + "/" + c.Name
			checked = append(checked, name)
			if err := operator.VerifyContainer(c); err != nil {
				errs = append(errs, name+": "+err.Error())
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "\n"))
	}
	if len(checked) == 0 {
		return fmt.Errorf("no AWS client among the Deployments: refusing to pass vacuously")
	}
	fmt.Fprintf(out, "every AWS client reaches only the fake: %s\n", strings.Join(checked, ", "))
	return nil
}

func awsClient(args []string, env []corev1.EnvVar) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "--aws-region") {
			return true
		}
	}
	for _, e := range env {
		if e.Name == "AWS_REGION" || e.Name == "AWS_ENDPOINT_URL" {
			return true
		}
	}
	return false
}

// Report is what Scan found.
type Report struct {
	GoFiles, OtherFiles, LiveFiles int
	Findings                       []string
}

func (r *Report) add(path string, line int, rule, msg string) {
	r.Findings = append(r.Findings, fmt.Sprintf("%s:%d: %s: %s", path, line, rule, msg))
}

var testDirs = []string{"pkg/test/", "tests/", "cloudcontrol/test/", "config/fake-aws/"}

func skipDir(rel string) bool {
	switch rel {
	case ".git", "third_party", ".cache", "bin", "node_modules", "pkg/fakeaws", "hack/noaws/testdata", "offload-artifacts":
		return true
	}
	return false
}

func testPath(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	for _, d := range testDirs {
		if strings.HasPrefix(rel, d) {
			return true
		}
	}
	return false
}

// Scan checks every test path under root.
func Scan(root string) (*Report, error) {
	r := &Report{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && skipDir(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(rel, ".go") {
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if liveFile(src) {
				r.LiveFiles++
				if !liveAllowed(rel) {
					r.add(rel, 1, "L2", "built with -tags live outside cloudcontrol/test/live, hack/live and cloudcontrol/test/e2e/live_test.go")
				}
				return nil
			}
		}
		if !testPath(rel) {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		switch filepath.Ext(rel) {
		case ".go":
			r.GoFiles++
			if err := scanGo(r, rel, src); err != nil {
				return err
			}
		case ".sh", ".py", ".yaml", ".yml", ".json":
			r.OtherFiles++
			if filepath.Ext(rel) == ".sh" {
				scanShell(r, rel, src)
			}
		default:
			return nil
		}
		scanEndpoints(r, rel, src)
		return nil
	})
	if err == nil {
		err = scanTaskfiles(r, root)
	}
	sort.Strings(r.Findings)
	return r, err
}

var buildLive = regexp.MustCompile(`(?m)^//go:build .*\blive\b`)

func liveFile(src []byte) bool { return buildLive.Match(src) }

func liveAllowed(rel string) bool {
	return strings.HasPrefix(rel, "cloudcontrol/test/live/") || strings.HasPrefix(rel, "hack/live/") || rel == "cloudcontrol/test/e2e/live_test.go"
}

var liveTasks = map[string]bool{"test-live": true, "live-sweep": true, "live-list": true}

var liveMention = regexp.MustCompile(`hack/live|-tags[ =]live\b|CLOUDCONTROL_E2E_TARGET=real`)

// scanTaskfiles checks L1 on Taskfile.yml and taskfiles/*.yml.
func scanTaskfiles(r *Report, root string) error {
	files, _ := filepath.Glob(filepath.Join(root, "taskfiles", "*.yml"))
	files = append([]string{filepath.Join(root, "Taskfile.yml")}, files...)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		var doc struct {
			Env   map[string]any `json:"env"`
			Tasks map[string]any `json:"tasks"`
		}
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if rel == "Taskfile.yml" && fmt.Sprint(doc.Env["CLOUDCONTROL_E2E_TARGET"]) != "fake" {
			r.add(rel, 1, "L1", "the global env must set CLOUDCONTROL_E2E_TARGET: fake")
		}
		for name, t := range doc.Tasks {
			var strs, calls []string
			walk(t, &strs, &calls)
			if liveTasks[name] && rel == "Taskfile.yml" {
				continue
			}
			for _, s := range strs {
				// go vet compiles the live code so it does not rot; it runs nothing.
				if strings.HasPrefix(strings.TrimSpace(s), "go vet ") && !strings.Contains(s, "hack/live/run.sh") {
					continue
				}
				if liveMention.MatchString(s) {
					r.add(rel, 1, "L1", "task "+name+" reaches the live tests ("+liveMention.FindString(s)+"); only test-live, live-sweep and live-list may")
				}
			}
			for _, c := range calls {
				if liveTasks[c] {
					r.add(rel, 1, "L1", "task "+name+" calls "+c)
				}
			}
		}
	}
	return nil
}

// walk collects a task's strings and the tasks it calls ({task: name}).
func walk(v any, strs, calls *[]string) {
	switch x := v.(type) {
	case string:
		*strs = append(*strs, x)
	case []any:
		for _, e := range x {
			walk(e, strs, calls)
		}
	case map[string]any:
		if n, ok := x["task"].(string); ok {
			*calls = append(*calls, n)
		}
		for k, e := range x {
			if k == "desc" || k == "summary" {
				continue
			}
			walk(e, strs, calls)
		}
	}
}

func scanGo(r *Report, rel string, src []byte) error {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
	if err != nil {
		return err
	}
	imports := map[string]string{} // local name -> path
	for _, im := range f.Imports {
		path, _ := strconv.Unquote(im.Path.Value)
		name := filepath.Base(path)
		if im.Name != nil {
			name = im.Name.Name
		}
		imports[name] = path
	}
	sel := func(e ast.Expr) (pkg, name string) {
		if s, ok := e.(*ast.SelectorExpr); ok {
			if id, ok := s.X.(*ast.Ident); ok {
				return imports[id.Name], s.Sel.Name
			}
		}
		return "", ""
	}
	line := func(n ast.Node) int { return fset.Position(n.Pos()).Line }

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			// Package-level declarations are walked below with the rest.
			continue
		}
		// G2: does this function set a process's Env from pkg/fakeaws?
		envFromFake := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, l := range as.Lhs {
				if s, ok := l.(*ast.SelectorExpr); ok && s.Sel.Name == "Env" && i < len(as.Rhs) {
					if c, ok := as.Rhs[i].(*ast.CallExpr); ok {
						if p, _ := sel(c.Fun); strings.HasSuffix(p, "/pkg/fakeaws") {
							envFromFake = true
						}
					}
				}
			}
			return true
		})
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			p, name := sel(c.Fun)
			if p == "os/exec" && (name == "Command" || name == "CommandContext") {
				prog := 0
				if name == "CommandContext" {
					prog = 1
				}
				if len(c.Args) > prog {
					if lit, ok := c.Args[prog].(*ast.BasicLit); ok && lit.Value == `"go"` {
						return true
					}
				}
				if !envFromFake {
					r.add(rel, line(c), "G2", "a process started without pkg/fakeaws's environment; use fakeaws.Command, or set its Env from fakeaws")
				}
			}
			return true
		})
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CompositeLit:
			if p, name := sel(n.Type); p == "github.com/aws/aws-sdk-go-v2/aws" && name == "Config" {
				r.add(rel, line(n), "G1", "an aws.Config made by hand; use fakeaws.Config")
			}
		case *ast.CallExpr:
			p, name := sel(n.Fun)
			if p == "github.com/aws/aws-sdk-go-v2/config" && strings.HasPrefix(name, "Load") {
				r.add(rel, line(n), "G1", "config."+name+" reads the machine's AWS configuration; use fakeaws.Config")
			}
			if p == "net/http" && (name == "Get" || name == "Post" || name == "Head" || name == "PostForm") {
				r.add(rel, line(n), "G3", "http."+name+" uses the default client; use fakeaws.HTTPClient")
			}
		case *ast.SelectorExpr:
			if p, name := sel(n); p == "net/http" && (name == "DefaultClient" || name == "DefaultTransport") {
				r.add(rel, line(n), "G3", "http."+name+"; use fakeaws.HTTPClient")
			}
		case *ast.AssignStmt:
			for _, l := range n.Lhs {
				if s, ok := l.(*ast.SelectorExpr); ok && (s.Sel.Name == "HTTPClient" || s.Sel.Name == "BaseEndpoint") {
					r.add(rel, line(n), "G1", "changes an SDK configuration's "+s.Sel.Name+", undoing fakeaws.Config's guard")
				}
			}
		case *ast.KeyValueExpr:
			if k, ok := n.Key.(*ast.Ident); ok && (k.Name == "HTTPClient" || k.Name == "BaseEndpoint") {
				r.add(rel, line(n), "G1", "sets an SDK configuration's "+k.Name+" by hand; use fakeaws.Config")
			}
		}
		return true
	})
	return nil
}

var (
	awsDomain    = `(?:` + strings.ReplaceAll(strings.Join(fakeaws.AWSDomains, "|"), ".", `\.`) + `)`
	endpointURL  = regexp.MustCompile(`(xmlns=\\?")?https?://[A-Za-z0-9.-]*\.` + awsDomain + `\b`)
	regionalHost = regexp.MustCompile(`\b[a-z0-9-]+(?:\.[a-z0-9-]+)*\.(?:us|eu|ap|sa|ca|me|af|il|mx|cn)-(?:gov-|iso[a-z]*-)?[a-z]+-\d+\.` + awsDomain + `\b`)
	pythonRun    = regexp.MustCompile(`(?:"?\$\{?PY\}?"?|\bpython3?)\s`)
	fakePyDef    = regexp.MustCompile(`^\s*fake_py\(\)\s*\{`)
	awsCLI       = regexp.MustCompile(`(?:^|[;&|(]\s*|\$\(\s*)aws\s+[a-z]`)
	shellComment = regexp.MustCompile(`^\s*#`)
)

func scanEndpoints(r *Report, rel string, src []byte) {
	for i, l := range strings.Split(string(src), "\n") {
		for _, m := range endpointURL.FindAllStringSubmatch(l, -1) {
			if m[1] == "" {
				r.add(rel, i+1, "G4", "a real AWS endpoint written out: "+m[0])
			}
		}
		if !endpointURL.MatchString(l) {
			for _, m := range regionalHost.FindAllString(l, -1) {
				r.add(rel, i+1, "G4", "a real AWS endpoint written out: "+m)
			}
		}
	}
}

func scanShell(r *Report, rel string, src []byte) {
	if !strings.HasPrefix(rel, "tests/") {
		return
	}
	inDef := false
	for i, l := range strings.Split(string(src), "\n") {
		if shellComment.MatchString(l) {
			continue
		}
		if fakePyDef.MatchString(l) {
			inDef = !strings.Contains(l, "}")
			continue
		}
		if inDef {
			if strings.TrimSpace(l) == "}" {
				inDef = false
			}
			continue
		}
		if pythonRun.MatchString(l) && !strings.Contains(l, "hack/moto.sh") {
			r.add(rel, i+1, "S1", "Python run other than through fake_py (its boto3 could reach real AWS)")
		}
		if awsCLI.MatchString(l) {
			r.add(rel, i+1, "S1", "the AWS CLI")
		}
	}
}
