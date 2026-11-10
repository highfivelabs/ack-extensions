// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package fake is a thin AWS Cloud Control API for tests: the protocol and
// Cloud Control's own rules, generic and schema-driven, over per-type
// backends that do the work in moto (moto 5.2.5 has no Cloud Control at
// all: IMPLEMENTATION_COVERAGE.md lists cloudcontrol among the services
// with nothing implemented).
//
// What the server does itself, for every type, from the schema:
//
//   - the AWS JSON 1.0 protocol (X-Amz-Target: CloudApiService.<Action>)
//     and its exceptions;
//   - asynchronous requests: each create, update and delete returns a
//     ProgressEvent IN_PROGRESS and answers GetResourceRequestStatus with
//     IN_PROGRESS Polls times before SUCCESS or FAILED; the identifier is
//     only given at the end, the hardest case for a reconciler;
//   - client tokens: the same token returns the same request;
//   - a desired state with an unknown property, a read-only one or a
//     missing required one fails with InvalidRequest;
//   - a patch that touches a read-only or create-only property fails with
//     NotUpdatable;
//   - write-only properties are never returned; the server keeps what it was
//     sent (WriteOnly) so tests can check a Secret's value arrived;
//   - a second request on a resource whose request is still in progress is
//     refused with ConcurrentOperationException.
//
// Everything that is not a Cloud Control call (STS's GetCallerIdentity at
// the manager's start, any other service) is passed to the upstream, moto,
// so the fake's one URL is the manager's every endpoint (pkg/fakeaws).
//
// What it does not do: hooks, RoleArn, ListResources, CancelResourceRequest,
// resource-level IAM. What real Cloud Control does that this cannot show is
// in docs/cloudcontrol.md ("The fake").
package fake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/cfn"
	"github.com/highfivelabs/ack-extensions/pkg/fakeaws"
)

// Backend does one type's work in moto, on CloudFormation models.
type Backend interface {
	Create(ctx context.Context, model map[string]any) (identifier string, err error)
	Read(ctx context.Context, identifier string) (map[string]any, error)
	Update(ctx context.Context, identifier string, prev, next map[string]any) error
	Delete(ctx context.Context, identifier string) error
}

// HandlerError is a handler's failure: a HandlerErrorCode and message.
type HandlerError struct{ Code, Message string }

func (e *HandlerError) Error() string { return e.Code + ": " + e.Message }

// NotFound, InvalidRequest and the like make handler errors.
func NotFound(format string, a ...any) error {
	return &HandlerError{"NotFound", fmt.Sprintf(format, a...)}
}
func InvalidRequest(format string, a ...any) error {
	return &HandlerError{"InvalidRequest", fmt.Sprintf(format, a...)}
}

func handlerCode(err error) (string, string) {
	var he *HandlerError
	if errors.As(err, &he) {
		return he.Code, he.Message
	}
	return "GeneralServiceException", err.Error()
}

// Call is one API call the server received, for assertions.
type Call struct {
	Action, TypeName, Identifier, ClientToken, Body string
}

type progressEvent struct {
	TypeName        string  `json:"TypeName,omitempty"`
	Identifier      string  `json:"Identifier,omitempty"`
	RequestToken    string  `json:"RequestToken,omitempty"`
	Operation       string  `json:"Operation,omitempty"`
	OperationStatus string  `json:"OperationStatus,omitempty"`
	EventTime       float64 `json:"EventTime,omitempty"`
	ResourceModel   string  `json:"ResourceModel,omitempty"`
	StatusMessage   string  `json:"StatusMessage,omitempty"`
	ErrorCode       string  `json:"ErrorCode,omitempty"`
}

type pending struct {
	key       string // type|identifier, for concurrency
	pollsLeft int
	progress  progressEvent
	final     progressEvent
}

// Server is the fake.
type Server struct {
	// Polls is how many GetResourceRequestStatus answers say IN_PROGRESS
	// before a request's outcome. Set before use.
	Polls int

	mu        sync.Mutex
	schemas   map[string]*cfn.Schema
	backends  map[string]Backend
	requests  map[string]*pending
	byToken   map[string]string
	writeOnly map[string]map[string]any
	calls     []Call
	seq       int
	http      *httptest.Server
	upstream  *httputil.ReverseProxy
}

// New makes a server for the given types.
func New() *Server {
	return &Server{
		Polls:    2,
		schemas:  map[string]*cfn.Schema{},
		backends: map[string]Backend{},
		requests: map[string]*pending{}, byToken: map[string]string{},
		writeOnly: map[string]map[string]any{},
	}
}

// Register serves a type.
func (s *Server) Register(schema *cfn.Schema, b Backend) {
	s.schemas[schema.TypeName] = schema
	s.backends[schema.TypeName] = b
}

// Upstream passes every request that is not a Cloud Control call to
// endpoint (moto), which must be on loopback, through the guarded client.
func (s *Server) Upstream(endpoint string) error {
	e, err := fakeaws.Parse(endpoint, fakeaws.Loopback)
	if err != nil {
		return err
	}
	u, _ := url.Parse(e.String())
	rp := httputil.NewSingleHostReverseProxy(u)
	rp.Transport = fakeaws.HTTPClient().Transport
	s.upstream = rp
	return nil
}

// Start listens on a loopback port and returns the endpoint URL.
func (s *Server) Start() string {
	s.http = httptest.NewServer(http.HandlerFunc(s.serve))
	return s.http.URL
}

// Close stops the server.
func (s *Server) Close() {
	if s.http != nil {
		s.http.Close()
	}
}

// Calls is every call so far.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// WriteOnly is the write-only values the server was last sent for a
// resource (Cloud Control never returns them).
func (s *Server) WriteOnly(typeName, identifier string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeOnly[typeName+"|"+identifier]
}

type apiError struct {
	status       int
	code, detail string
}

func (e *apiError) Error() string { return e.code + ": " + e.detail }

func exception(code, format string, a ...any) *apiError {
	return &apiError{http.StatusBadRequest, code, fmt.Sprintf(format, a...)}
}

// ParseCall is the Call a Cloud Control request (its X-Amz-Target and
// JSON body) records; the live recorder (-tags live) records the same.
func ParseCall(target string, body []byte) Call {
	var in map[string]any
	_ = json.Unmarshal(body, &in)
	str := func(k string) string { v, _ := in[k].(string); return v }
	action := strings.TrimPrefix(target, "CloudApiService.")
	call := Call{Action: action, TypeName: str("TypeName"), Identifier: str("Identifier"), ClientToken: str("ClientToken")}
	switch action {
	case "CreateResource":
		call.Body = str("DesiredState")
	case "UpdateResource":
		call.Body = str("PatchDocument")
	case "GetResourceRequestStatus":
		call.Identifier = str("RequestToken")
	}
	return call
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	target := r.Header.Get("X-Amz-Target")
	if !strings.HasPrefix(target, "CloudApiService.") && s.upstream != nil {
		s.upstream.ServeHTTP(w, r)
		return
	}
	action := strings.TrimPrefix(target, "CloudApiService.")
	body, _ := io.ReadAll(r.Body)
	var in map[string]any
	_ = json.Unmarshal(body, &in)
	str := func(k string) string { v, _ := in[k].(string); return v }

	s.mu.Lock()
	s.calls = append(s.calls, ParseCall(target, body))
	s.mu.Unlock()

	ctx := r.Context()
	var out any
	var err error
	switch action {
	case "CreateResource":
		out, err = s.create(ctx, str("TypeName"), str("DesiredState"), str("ClientToken"))
	case "GetResource":
		out, err = s.get(ctx, str("TypeName"), str("Identifier"))
	case "UpdateResource":
		out, err = s.update(ctx, str("TypeName"), str("Identifier"), str("PatchDocument"), str("ClientToken"))
	case "DeleteResource":
		out, err = s.delete(ctx, str("TypeName"), str("Identifier"), str("ClientToken"))
	case "GetResourceRequestStatus":
		out, err = s.status(str("RequestToken"))
	default:
		err = exception("UnsupportedActionException", "the fake does not implement %s", action)
	}
	w.Header().Set("Content-Type", "application/x-amz-json-1.0")
	if err != nil {
		var ae *apiError
		if !errors.As(err, &ae) {
			ae = &apiError{http.StatusInternalServerError, "ServiceInternalErrorException", err.Error()}
		}
		w.Header().Set("X-Amzn-Errortype", ae.code)
		w.WriteHeader(ae.status)
		_ = json.NewEncoder(w).Encode(map[string]string{"__type": ae.code, "Message": ae.detail})
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) typ(name string) (*cfn.Schema, Backend, error) {
	sc, ok := s.schemas[name]
	if !ok {
		return nil, nil, exception("TypeNotFoundException", "type %s is not served by the fake", name)
	}
	return sc, s.backends[name], nil
}

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// validate applies Cloud Control's checks on a whole desired state.
func validate(sc *cfn.Schema, model map[string]any) error {
	sets := sc.Sets()
	var keys []string
	for k := range model {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if sc.Properties[k] == nil {
			return InvalidRequest("#: extraneous key [%s] is not permitted", k)
		}
		if sets.ReadOnly.Has(cfn.Pointer(k)) {
			return InvalidRequest("#: [%s] is read-only and cannot be set", k)
		}
	}
	for _, k := range sc.Required {
		if _, ok := model[k]; !ok && !sets.ReadOnly.Has(cfn.Pointer(k)) {
			return InvalidRequest("#: required key [%s] not found", k)
		}
	}
	return nil
}

// splitWriteOnly removes top-level write-only properties from a model.
func splitWriteOnly(sc *cfn.Schema, model map[string]any) map[string]any {
	wo := map[string]any{}
	for _, name := range cfn.NewPointerSet(sc.WriteOnlyProperties).TopLevel() {
		if v, ok := model[name]; ok {
			wo[name] = v
			delete(model, name)
		}
	}
	return wo
}

// begin records a request; caller holds no lock.
func (s *Server) begin(key, typeName, op, clientToken string, final progressEvent) progressEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	tok := fmt.Sprintf("req-%06d-%s", s.seq, strings.ToLower(op))
	final.TypeName, final.RequestToken, final.Operation, final.EventTime = typeName, tok, op, now()
	p := &pending{key: key, pollsLeft: s.Polls, final: final,
		progress: progressEvent{TypeName: typeName, RequestToken: tok, Operation: op, OperationStatus: "IN_PROGRESS", EventTime: now()}}
	s.requests[tok] = p
	if clientToken != "" {
		s.byToken[clientToken] = tok
	}
	if p.pollsLeft <= 0 {
		return p.final
	}
	return p.progress
}

// replay answers a repeated client token with the request's state.
func (s *Server) replay(clientToken string) (progressEvent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, ok := s.byToken[clientToken]
	if !ok || clientToken == "" {
		return progressEvent{}, false
	}
	p := s.requests[tok]
	if p.pollsLeft > 0 {
		return p.progress, true
	}
	return p.final, true
}

func (s *Server) busy(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.requests {
		if p.key == key && p.pollsLeft > 0 {
			return true
		}
	}
	return false
}

func failed(err error) progressEvent {
	code, msg := handlerCode(err)
	return progressEvent{OperationStatus: "FAILED", ErrorCode: code, StatusMessage: msg}
}

func (s *Server) create(ctx context.Context, typeName, desired, clientToken string) (any, error) {
	sc, b, err := s.typ(typeName)
	if err != nil {
		return nil, err
	}
	if ev, ok := s.replay(clientToken); ok {
		return map[string]any{"ProgressEvent": ev}, nil
	}
	var model map[string]any
	if err := json.Unmarshal([]byte(desired), &model); err != nil {
		return nil, exception("ValidationException", "DesiredState is not a JSON object: %v", err)
	}
	final := progressEvent{OperationStatus: "SUCCESS"}
	if err := validate(sc, model); err != nil {
		final = failed(err)
	} else {
		wo := splitWriteOnly(sc, model)
		id, err := b.Create(ctx, model)
		if err != nil {
			final = failed(err)
		} else {
			final.Identifier = id
			s.mu.Lock()
			s.writeOnly[typeName+"|"+id] = wo
			s.mu.Unlock()
			if m, err := b.Read(ctx, id); err == nil {
				mb, _ := json.Marshal(m)
				final.ResourceModel = string(mb)
			}
		}
	}
	ev := s.begin(typeName+"|"+final.Identifier, typeName, "CREATE", clientToken, final)
	return map[string]any{"ProgressEvent": ev}, nil
}

func (s *Server) get(ctx context.Context, typeName, id string) (any, error) {
	sc, b, err := s.typ(typeName)
	if err != nil {
		return nil, err
	}
	m, err := b.Read(ctx, id)
	if err != nil {
		if code, _ := handlerCode(err); code == "NotFound" {
			return nil, exception("ResourceNotFoundException", "Resource of type '%s' with identifier '%s' was not found.", typeName, id)
		}
		return nil, exception("GeneralServiceException", "%v", err)
	}
	splitWriteOnly(sc, m)
	mb, _ := json.Marshal(m)
	return map[string]any{"TypeName": typeName, "ResourceDescription": map[string]any{"Identifier": id, "Properties": string(mb)}}, nil
}

func (s *Server) update(ctx context.Context, typeName, id, patch, clientToken string) (any, error) {
	sc, b, err := s.typ(typeName)
	if err != nil {
		return nil, err
	}
	if ev, ok := s.replay(clientToken); ok {
		return map[string]any{"ProgressEvent": ev}, nil
	}
	key := typeName + "|" + id
	if s.busy(key) {
		return nil, exception("ConcurrentOperationException", "another request on %s is in progress", id)
	}
	cur, err := b.Read(ctx, id)
	if err != nil {
		return nil, exception("ResourceNotFoundException", "Resource of type '%s' with identifier '%s' was not found.", typeName, id)
	}
	var ops []struct {
		Op, Path string
	}
	if err := json.Unmarshal([]byte(patch), &ops); err != nil {
		return nil, exception("ValidationException", "PatchDocument is not a JSON Patch: %v", err)
	}
	final := progressEvent{OperationStatus: "SUCCESS", Identifier: id}
	sets := sc.Sets()
	for _, op := range ops {
		ptr := "/properties" + op.Path
		if sets.ReadOnly.Covers(ptr) || sets.CreateOnly.Covers(ptr) {
			final = failed(&HandlerError{"NotUpdatable", fmt.Sprintf("Invalid patch update: createOnlyProperties and readOnlyProperties cannot be updated [%s]", op.Path)})
		}
	}
	if final.OperationStatus == "SUCCESS" {
		s.mu.Lock()
		doc := map[string]any{}
		for k, v := range cur {
			doc[k] = v
		}
		for k, v := range s.writeOnly[key] {
			doc[k] = v
		}
		s.mu.Unlock()
		for _, ro := range sets.ReadOnly.TopLevel() {
			delete(doc, ro)
		}
		db, _ := json.Marshal(doc)
		p, err := jsonpatch.DecodePatch([]byte(patch))
		var nb []byte
		if err == nil {
			nb, err = p.Apply(db)
		}
		if err != nil {
			final = failed(InvalidRequest("patch: %v", err))
		} else {
			var next map[string]any
			_ = json.Unmarshal(nb, &next)
			if err := validate(sc, next); err != nil {
				final = failed(err)
			} else {
				wo := splitWriteOnly(sc, next)
				prev := map[string]any{}
				for k, v := range cur {
					prev[k] = v
				}
				splitWriteOnly(sc, prev)
				if err := b.Update(ctx, id, prev, next); err != nil {
					final = failed(err)
				} else {
					s.mu.Lock()
					s.writeOnly[key] = wo
					s.mu.Unlock()
				}
			}
		}
	}
	ev := s.begin(key, typeName, "UPDATE", clientToken, final)
	return map[string]any{"ProgressEvent": ev}, nil
}

func (s *Server) delete(ctx context.Context, typeName, id, clientToken string) (any, error) {
	_, b, err := s.typ(typeName)
	if err != nil {
		return nil, err
	}
	if ev, ok := s.replay(clientToken); ok {
		return map[string]any{"ProgressEvent": ev}, nil
	}
	key := typeName + "|" + id
	if s.busy(key) {
		return nil, exception("ConcurrentOperationException", "another request on %s is in progress", id)
	}
	final := progressEvent{OperationStatus: "SUCCESS", Identifier: id}
	if err := b.Delete(ctx, id); err != nil {
		final = failed(err)
	} else {
		s.mu.Lock()
		delete(s.writeOnly, key)
		s.mu.Unlock()
	}
	ev := s.begin(key, typeName, "DELETE", clientToken, final)
	return map[string]any{"ProgressEvent": ev}, nil
}

func (s *Server) status(tok string) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.requests[tok]
	if !ok {
		return nil, exception("RequestTokenNotFoundException", "request %s not found", tok)
	}
	if p.pollsLeft > 0 {
		p.pollsLeft--
		ev := p.progress
		ev.EventTime = now()
		return map[string]any{"ProgressEvent": ev}, nil
	}
	return map[string]any{"ProgressEvent": p.final}, nil
}
