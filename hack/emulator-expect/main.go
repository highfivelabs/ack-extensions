// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// emulator-expect judges a run of the ACK-kind tests (tests/e2e) on AWS
// Emulator against the list of tests expected to fail there
// (tests/e2e/aws-emulator-expected-failures.yaml; docs/aws-emulator.md).
//
//	go run ./hack/emulator-expect -expect FILE -list FILE -json FILE -version V
//
// -list is `go test -list` of the run's tests, -json the run's `go test
// -json` output, -version the installed emulator's release. The run
// passes only when every listed test ran and finished, each expected
// failure failed, and every other test passed; the list itself must name
// tests that exist, each with its missing operation and a link to
// docs/aws-emulator.md, for the release installed. go test's own exit
// status is not read: the expected failures make it fail.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"
)

// Expectations is the expected-failures file.
type Expectations struct {
	Release  string `json:"release"`
	Expected []struct {
		Test    string `json:"test"`
		Missing string `json:"missing"`
		Doc     string `json:"doc"`
	} `json:"expected"`
}

type event struct {
	Action string `json:"Action"`
	Test   string `json:"Test"`
	Output string `json:"Output"`
}

// tail is the most a failure's output printed for the log.
const tail = 80

// output is each top-level test's output (its subtests' too), for the log.
func output(events string) map[string][]string {
	out := map[string][]string{}
	for _, line := range strings.Split(events, "\n") {
		var ev event
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Test == "" || ev.Action != "output" {
			continue
		}
		top, _, _ := strings.Cut(ev.Test, "/")
		out[top] = append(out[top], strings.TrimRight(ev.Output, "\n"))
	}
	return out
}

var testName = regexp.MustCompile(`^Test[A-Za-z0-9_]*$`)

// check returns the run's problems; none means it passed, and passed
// counts the tests that passed. err is for input it cannot read at all.
func check(expectYAML []byte, list, events, version string) (problems []string, err error) {
	problems, _, err = judge(expectYAML, list, events, version)
	return problems, err
}

func judge(expectYAML []byte, list, events, version string) (problems []string, passed int, err error) {
	problems, passed, _, err = judgeTests(expectYAML, list, events, version)
	return problems, passed, err
}

// judgeTests is judge, and the tests whose output the log should show.
func judgeTests(expectYAML []byte, list, events, version string) (problems []string, passed int, show []string, err error) {
	var exp Expectations
	if err := yaml.UnmarshalStrict(expectYAML, &exp); err != nil {
		return nil, 0, nil, fmt.Errorf("the expected-failures file: %w", err)
	}
	add := func(f string, a ...any) { problems = append(problems, fmt.Sprintf(f, a...)) }

	tests := map[string]bool{}
	var order []string
	for _, l := range strings.Split(list, "\n") {
		l = strings.TrimSpace(l)
		if testName.MatchString(l) && !tests[l] {
			tests[l] = true
			order = append(order, l)
		}
	}
	if len(order) == 0 {
		add("go test -list gave no tests")
	}

	if exp.Release != version {
		add("emulator %s is installed, the list is for %s: run the pinned release, or move the pin and reread the list", version, exp.Release)
	}
	xfail := map[string]string{}
	for _, e := range exp.Expected {
		switch {
		case e.Test == "":
			add("an expected failure without a test")
			continue
		case xfail[e.Test] != "":
			add("%s is listed twice", e.Test)
		case !tests[e.Test]:
			add("%s is not a test in this run: take it off the list", e.Test)
		}
		if strings.TrimSpace(e.Missing) == "" {
			add("%s: no missing operation named", e.Test)
		}
		if !strings.HasPrefix(e.Doc, "docs/aws-emulator.md") {
			add("%s: doc %q does not link docs/aws-emulator.md", e.Test, e.Doc)
		}
		xfail[e.Test] = e.Missing + " "
	}

	// The last word on each top-level test; subtests are their parent's.
	result := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(events))
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "{") {
			continue // go test's own lines (build errors) outside the events
		}
		var ev event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return nil, 0, nil, fmt.Errorf("go test -json: %w", err)
		}
		if ev.Test == "" || strings.Contains(ev.Test, "/") {
			continue
		}
		switch ev.Action {
		case "pass", "fail", "skip":
			result[ev.Test] = ev.Action
			if !tests[ev.Test] {
				tests[ev.Test] = true
				order = append(order, ev.Test)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, 0, nil, err
	}

	for _, name := range order {
		missing, expected := xfail[name]
		switch r := result[name]; {
		case r == "":
			add("%s did not finish (a panic, the binary's timeout or a build failure)", name)
			show = append(show, name)
		case r == "skip":
			add("%s skipped", name)
		case expected && r == "pass":
			add("%s passes on the emulator now (expected to fail: %s): take it off the list", name, strings.TrimSpace(missing))
		case !expected && r == "fail":
			add("%s failed", name)
			show = append(show, name)
		case r == "pass":
			passed++
		}
	}
	return problems, passed, show, nil
}

func main() {
	expect := flag.String("expect", "tests/e2e/aws-emulator-expected-failures.yaml", "the expected-failures file")
	list := flag.String("list", "", "go test -list output")
	events := flag.String("json", "", "go test -json output")
	version := flag.String("version", "", "the installed emulator's release")
	flag.Parse()
	if *list == "" || *events == "" || *version == "" {
		fmt.Fprintln(os.Stderr, "emulator-expect: -list, -json and -version are required")
		os.Exit(2)
	}
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, "emulator-expect:", err)
			os.Exit(2)
		}
		return string(b)
	}
	e := read(*expect)
	j := read(*events)
	problems, passed, show, err := judgeTests([]byte(e), read(*list), j, *version)
	if err != nil {
		fmt.Fprintln(os.Stderr, "emulator-expect:", err)
		os.Exit(2)
	}
	var exp Expectations
	_ = yaml.Unmarshal([]byte(e), &exp)
	fmt.Printf("emulator-expect: AWS Emulator %s: %d passed, %d expected to fail (%s)\n", *version, passed, len(exp.Expected), *expect)
	if len(problems) > 0 {
		out := output(j)
		for _, name := range show {
			lines := out[name]
			if len(lines) > tail {
				lines = lines[len(lines)-tail:]
			}
			fmt.Printf("--- %s: the last %d lines of its output\n%s\n", name, len(lines), strings.Join(lines, "\n"))
		}
		for _, p := range problems {
			fmt.Println("emulator-expect: FAIL:", p)
		}
		os.Exit(1)
	}
	fmt.Println("emulator-expect: ok: every expected failure failed, every other test passed")
}
