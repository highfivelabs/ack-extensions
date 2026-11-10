// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

//go:build live

package live

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/test/fake"
)

// Recorder stands where the fake stands in the fake run: a loopback URL
// that is the manager's one endpoint. It records every Cloud Control call
// as the fake does (so the tests' call-log checks hold on a live run too)
// and forwards it, signed again with the run's credentials, to real Cloud
// Control; the STS call ACK's runtime makes at start goes to real STS.
// Anything else is refused. So the manager itself still runs under
// pkg/fakeaws (fake keys, dead proxy, a loopback endpoint) and never holds
// real credentials: only this process does.
type Recorder struct {
	cfg    aws.Config
	region string
	srv    *httptest.Server
	client *http.Client

	mu      sync.Mutex
	calls   []fake.Call
	desired map[string]map[string]any // type -> the last CreateResource desired state
}

// NewRecorder makes a recorder for the run.
func NewRecorder(r *Run) *Recorder {
	return &Recorder{cfg: r.Config, region: r.Region, client: &http.Client{Timeout: 60 * time.Second}, desired: map[string]map[string]any{}}
}

// Start listens on loopback and returns the URL.
func (r *Recorder) Start() string {
	r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	return r.srv.URL
}

// Close stops it.
func (r *Recorder) Close() { r.srv.Close() }

// Calls is every Cloud Control call so far.
func (r *Recorder) Calls() []fake.Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]fake.Call(nil), r.calls...)
}

// WriteOnly is the last desired state sent for typeName (the fake keys
// write-only values by identifier, which a create's request does not
// have yet; the tests make one object of a type at a time).
func (r *Recorder) WriteOnly(typeName, _ string) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.desired[typeName]
}

var dropHeaders = []string{"Authorization", "X-Amz-Date", "X-Amz-Security-Token", "X-Amz-Content-Sha256", "Content-Length"}

func (r *Recorder) serve(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	target := req.Header.Get("X-Amz-Target")
	var host, service string
	switch {
	case strings.HasPrefix(target, "CloudApiService."):
		host, service = "cloudcontrolapi."+r.region+".amazonaws.com", "cloudcontrolapi"
		c := fake.ParseCall(target, body)
		r.mu.Lock()
		r.calls = append(r.calls, c)
		if c.Action == "CreateResource" {
			var m map[string]any
			if json.Unmarshal([]byte(c.Body), &m) == nil {
				r.desired[c.TypeName] = m
			}
		}
		r.mu.Unlock()
	case target == "" && bytes.Contains(body, []byte("Action=GetCallerIdentity")):
		host, service = "sts."+r.region+".amazonaws.com", "sts"
	default:
		http.Error(w, "live recorder: only Cloud Control and STS GetCallerIdentity are forwarded", http.StatusForbidden)
		return
	}
	out, err := http.NewRequestWithContext(req.Context(), req.Method, "https://"+host+req.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	out.Header = req.Header.Clone()
	for _, h := range dropHeaders {
		out.Header.Del(h)
	}
	if err := r.sign(req.Context(), out, body, service); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	resp, err := r.client.Do(out)
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

func (r *Recorder) sign(ctx context.Context, req *http.Request, body []byte, service string) error {
	creds, err := r.cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	return v4.NewSigner().SignHTTP(ctx, creds, req, hex.EncodeToString(sum[:]), service, r.region, time.Now())
}
