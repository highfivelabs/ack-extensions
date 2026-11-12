// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package awsemulator runs AWS Emulator (github.com/aws/aws-emulator, CC0)
// as a third fake backend for the end-to-end tests, beside moto and the
// schema fake (docs/aws-emulator.md). It is opt-in: only ACK_TEST_BACKEND=
// aws-emulator selects it, and task check never sets that.
//
// The emulator serves HTTPS on loopback with a certificate and a key pair
// of its own for each run. The manager and the tests keep the one plain,
// loopback endpoint and the fake keys pkg/fakeaws gives them: a Proxy on
// loopback takes every request, records the Cloud Control calls as the
// fake does (so the tests' call-log checks hold), signs it again with the
// run's emulator keys and forwards it over HTTPS, trusting only the
// emulator's certificate and dialling only loopback. The emulator itself
// runs with telemetry off and the dead proxy, so it reaches nothing either.
package awsemulator

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/test/fake"
	"github.com/highfivelabs/ack-extensions/pkg/fakeaws"
)

// Account is the one account the emulator answers as.
const Account = "000000000000"

// Backend is the value of ACK_TEST_BACKEND that selects the emulator.
const Backend = "aws-emulator"

// Selected reports whether ACK_TEST_BACKEND asks for the emulator. Any
// value but "", "moto" and "aws-emulator" fails the test.
func Selected(t testing.TB) bool {
	switch v := os.Getenv("ACK_TEST_BACKEND"); v {
	case "", "moto":
		return false
	case Backend:
		return true
	default:
		t.Fatalf("ACK_TEST_BACKEND=%q: moto (the default) or %s", v, Backend)
		return false
	}
}

// Emulator is one running emulator and the proxy in front of it.
type Emulator struct {
	// URL is the proxy's loopback URL: the one endpoint for the manager
	// and the tests' SDK clients.
	URL string
	// Endpoint is the emulator's own HTTPS URL.
	Endpoint string
	// PID is the emulator's process, for its memory (MaxRSS).
	PID int
	// Started is how long the emulator took from exec to healthy.
	Started time.Duration

	creds  aws.Credentials
	client *http.Client
	srv    *httptest.Server

	mu      sync.Mutex
	calls   []fake.Call
	desired map[string]map[string]any
}

type startup struct {
	Message     string `json:"message"`
	EndpointURL string `json:"endpoint_url"`
	CAFile      string `json:"ca_file"`
	ConfigFile  string `json:"config_file"`
}

// parseStartup finds the startup line (its message is "emulator server
// information"), its fields at the top level or under "fields".
func parseStartup(line []byte) (startup, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(line, &m) != nil {
		return startup{}, false
	}
	var s startup
	_ = json.Unmarshal(line, &s)
	if f, ok := m["fields"]; ok {
		var inner startup
		if json.Unmarshal(f, &inner) == nil {
			if s.Message == "" {
				s.Message = inner.Message
			}
			if s.EndpointURL == "" {
				s.EndpointURL, s.CAFile, s.ConfigFile = inner.EndpointURL, inner.CAFile, inner.ConfigFile
			}
		}
	}
	return s, s.Message == "emulator server information" && s.EndpointURL != ""
}

// syncLines keeps the first lines of a log.
type syncLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *syncLines) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.lines) < 40 {
		l.lines = append(l.lines, s)
	}
}

func (l *syncLines) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// Binary is the emulator's command: $AWS_EMULATOR_BIN, or aws-emulator on
// PATH (the Debian package installs /usr/bin/aws-emulator).
func Binary() (string, error) {
	if b := os.Getenv("AWS_EMULATOR_BIN"); b != "" {
		return b, nil
	}
	return exec.LookPath("aws-emulator")
}

// Start runs an emulator on a free loopback port with telemetry off, and
// the proxy in front of it. Both stop when the test ends.
func Start(t testing.TB) *Emulator {
	t.Helper()
	bin, err := Binary()
	if err != nil {
		t.Fatalf("ACK_TEST_BACKEND=%s: no emulator binary (install the Debian package, or set AWS_EMULATOR_BIN): %v", Backend, err)
	}
	home := t.TempDir()
	begin := time.Now()
	cmd := exec.Command(bin, "--port", "0", "--telemetry", "off", "--idle-timeout", "3600")
	// Not an AWS client: the fake keys, no shared configuration, the dead
	// proxy; its own files under a home of its own.
	cmd.Env = fakeaws.Isolated("HOME="+home, "EMULATOR_TELEMETRY=off", "PATH="+os.Getenv("PATH"))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	t.Cleanup(func() { _ = cmd.Process.Signal(os.Interrupt); _ = cmd.Wait() })

	found := make(chan startup, 1)
	var first syncLines
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		sent := false
		for sc.Scan() {
			first.add(sc.Text())
			if s, ok := parseStartup(sc.Bytes()); ok && !sent {
				found <- s
				sent = true
			}
			if os.Getenv("AWS_EMULATOR_LOG") != "" {
				fmt.Fprintln(os.Stderr, "aws-emulator:", sc.Text())
			}
		}
	}()
	var info startup
	select {
	case info = <-found:
	case <-time.After(60 * time.Second):
		t.Fatalf("the emulator wrote no startup line in 60s; stdout:\n%s\nstderr:\n%s", first.String(), stderr.String())
	}
	if err := fake.LocalOnly(info.EndpointURL); err != nil {
		t.Fatal(err)
	}
	ca, err := os.ReadFile(info.CAFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		t.Fatalf("no certificate in %s", info.CAFile)
	}
	hc := fakeaws.HTTPClient()
	hc.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	hc.Timeout = 60 * time.Second

	creds, err := readCredentials(filepath.Join(filepath.Dir(info.CAFile), "emulator_credentials"))
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		resp, err := hc.Get(info.EndpointURL + "/__admin/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the emulator at %s is not healthy: %v", info.EndpointURL, err)
		}
	}
	e := &Emulator{Endpoint: info.EndpointURL, PID: cmd.Process.Pid, Started: time.Since(begin),
		creds: creds, client: hc, desired: map[string]map[string]any{}}
	e.srv = httptest.NewServer(http.HandlerFunc(e.serve))
	t.Cleanup(e.srv.Close)
	e.URL = e.srv.URL
	return e
}

var credLine = regexp.MustCompile(`^\s*(aws_access_key_id|aws_secret_access_key)\s*=\s*(\S+)`)

func readCredentials(path string) (aws.Credentials, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return aws.Credentials{}, err
	}
	c := aws.Credentials{Source: "aws-emulator"}
	for _, l := range strings.Split(string(b), "\n") {
		if m := credLine.FindStringSubmatch(l); m != nil {
			if m[1] == "aws_access_key_id" {
				c.AccessKeyID = m[2]
			} else {
				c.SecretAccessKey = m[2]
			}
		}
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return c, fmt.Errorf("%s: no key pair", path)
	}
	return c, nil
}

// Calls is every Cloud Control call so far.
func (e *Emulator) Calls() []fake.Call {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]fake.Call(nil), e.calls...)
}

// WriteOnly is the last desired state sent for typeName, as the live
// recorder keeps it (the emulator, like Cloud Control, never returns
// write-only values).
func (e *Emulator) WriteOnly(typeName, _ string) map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.desired[typeName]
}

// MaxRSS is the emulator's peak resident memory in KiB (Linux only; 0
// elsewhere).
func (e *Emulator) MaxRSS() int {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", e.PID))
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VmHWM:") {
			f := strings.Fields(l)
			if len(f) >= 2 {
				n, _ := strconv.Atoi(f[1])
				return n
			}
		}
	}
	return 0
}

var scope = regexp.MustCompile(`Credential=[^/]+/\d{8}/([^/]+)/([^/]+)/aws4_request`)

var dropHeaders = []string{"Authorization", "X-Amz-Date", "X-Amz-Security-Token", "X-Amz-Content-Sha256", "Content-Length"}

func (e *Emulator) serve(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	m := scope.FindStringSubmatch(req.Header.Get("Authorization"))
	if m == nil {
		http.Error(w, "aws-emulator proxy: an unsigned request (no SigV4 scope to sign it again with)", http.StatusForbidden)
		return
	}
	region, service := m[1], m[2]
	if target := req.Header.Get("X-Amz-Target"); strings.HasPrefix(target, "CloudApiService.") {
		c := fake.ParseCall(target, body)
		e.mu.Lock()
		e.calls = append(e.calls, c)
		if c.Action == "CreateResource" {
			var d map[string]any
			if json.Unmarshal([]byte(c.Body), &d) == nil {
				e.desired[c.TypeName] = d
			}
		}
		e.mu.Unlock()
	}
	out, err := http.NewRequestWithContext(req.Context(), req.Method, e.Endpoint+req.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	out.Header = req.Header.Clone()
	for _, h := range dropHeaders {
		out.Header.Del(h)
	}
	sum := sha256.Sum256(body)
	if err := v4.NewSigner().SignHTTP(context.Background(), e.creds, out, hex.EncodeToString(sum[:]), service, region, time.Now()); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	resp, err := e.client.Do(out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
