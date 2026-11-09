// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package resource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	ackcfg "github.com/aws-controllers-k8s/runtime/pkg/config"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrequeue "github.com/aws-controllers-k8s/runtime/pkg/requeue"
	ackrt "github.com/aws-controllers-k8s/runtime/pkg/runtime"
	acktags "github.com/aws-controllers-k8s/runtime/pkg/tags"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/cfn"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
)

// PollDelay is how long the reconciler waits before asking again about a
// Cloud Control request still in progress. The reconciler never blocks a
// worker on a request: it records the request token in status and requeues.
var PollDelay = 3 * time.Second

// CloudControlAPI is the part of the Cloud Control client the manager uses.
type CloudControlAPI interface {
	CreateResource(context.Context, *cloudcontrol.CreateResourceInput, ...func(*cloudcontrol.Options)) (*cloudcontrol.CreateResourceOutput, error)
	GetResource(context.Context, *cloudcontrol.GetResourceInput, ...func(*cloudcontrol.Options)) (*cloudcontrol.GetResourceOutput, error)
	UpdateResource(context.Context, *cloudcontrol.UpdateResourceInput, ...func(*cloudcontrol.Options)) (*cloudcontrol.UpdateResourceOutput, error)
	DeleteResource(context.Context, *cloudcontrol.DeleteResourceInput, ...func(*cloudcontrol.Options)) (*cloudcontrol.DeleteResourceOutput, error)
	GetResourceRequestStatus(context.Context, *cloudcontrol.GetResourceRequestStatusInput, ...func(*cloudcontrol.Options)) (*cloudcontrol.GetResourceRequestStatusOutput, error)
}

type manager struct {
	t       *registry.Type
	s       *cfn.Schema
	cfg     ackcfg.Config
	log     logr.Logger
	rr      acktypes.Reconciler
	account ackv1alpha1.AWSAccountID
	region  ackv1alpha1.AWSRegion
	api     CloudControlAPI
}

var _ acktypes.AWSResourceManager = &manager{}

func (m *manager) typeName() *string { return aws.String(m.t.TypeName) }

// token is a Cloud Control client token: the same operation with the same
// input from the same object gets the same token, so a request retried
// after its status could not be written is not made twice. A failed
// request's token is mixed in, so a retry after a failure is a new request.
func token(r *resource, op string, body []byte) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", r.u.GetUID(), op)
	h.Write(body)
	if q := r.request(); q.Status == string(cctypes.OperationStatusFailed) {
		fmt.Fprintf(h, "\x00%s", q.Token)
	}
	return hex.EncodeToString(h.Sum(nil))[:64]
}

func toRequest(ev *cctypes.ProgressEvent) request {
	if ev == nil {
		return request{}
	}
	q := request{
		Token:     aws.ToString(ev.RequestToken),
		Operation: string(ev.Operation),
		Status:    string(ev.OperationStatus),
		ErrorCode: string(ev.ErrorCode),
		Message:   aws.ToString(ev.StatusMessage),
	}
	if ev.EventTime != nil {
		q.EventTime = ev.EventTime.UTC().Format(time.RFC3339)
	}
	return q
}

func handlerError(ev *cctypes.ProgressEvent) *HandlerError {
	return &HandlerError{Operation: string(ev.Operation), Code: string(ev.ErrorCode), Message: aws.ToString(ev.StatusMessage)}
}

func inProgress(ev *cctypes.ProgressEvent) error {
	return ackrequeue.NeededAfter(
		fmt.Errorf("Cloud Control %s request %s is %s", ev.Operation, aws.ToString(ev.RequestToken), ev.OperationStatus),
		PollDelay,
	)
}

// model is the object's spec as a CloudFormation model, with write-only
// Secret references resolved, and a digest of the write-only values.
func (m *manager) model(ctx context.Context, r *resource) (map[string]any, string, error) {
	spec := cfn.Normalise(r.spec()).(map[string]any)
	writeOnly := map[string]any{}
	for _, path := range m.t.SecretPaths() {
		fields := m.s.FieldPath(path)
		parent := spec
		for _, f := range fields[:len(fields)-1] {
			next, _ := parent[f].(map[string]any)
			parent = next
			if parent == nil {
				break
			}
		}
		if parent == nil {
			continue
		}
		leaf := fields[len(fields)-1]
		raw, ok := parent[leaf].(map[string]any)
		if !ok {
			continue
		}
		var ref ackv1alpha1.SecretKeyReference
		b, _ := json.Marshal(raw)
		if err := json.Unmarshal(b, &ref); err != nil {
			return nil, "", ackerr.NewTerminalError(fmt.Errorf("spec.%s: %w", strings.Join(fields, "."), err))
		}
		val, err := m.rr.SecretValueFromReference(ctx, &ref)
		if err != nil {
			return nil, "", fmt.Errorf("spec.%s: Secret %s key %s: %w", strings.Join(fields, "."), ref.Name, ref.Key, err)
		}
		parent[leaf] = val
		writeOnly[strings.Join(path, "/")] = val
	}
	for _, ptr := range m.s.WriteOnlyProperties {
		name := strings.TrimPrefix(ptr, "/properties/")
		if !strings.Contains(name, "/") {
			if v, ok := spec[cfn.FieldName(name)]; ok {
				if _, done := writeOnly[name]; !done {
					writeOnly[name] = v
				}
			}
		}
	}
	model := m.s.ToCFN(m.s.Root(), spec).(map[string]any)
	digest := ""
	if len(writeOnly) > 0 {
		b, _ := json.Marshal(writeOnly)
		sum := sha256.Sum256(b)
		digest = "sha256:" + hex.EncodeToString(sum[:])
	}
	return model, digest, nil
}

// observe fills latest from Cloud Control's model: the writable properties
// into spec (keeping desired's write-only fields, which Cloud Control never
// returns), the read-only ones into status, and the identifiers.
func (m *manager) observe(latest *resource, desired *resource, id string, props map[string]any) {
	sets := m.s.Sets()
	names, _ := cfn.Names(m.s.Properties)
	if !latest.IsBeingDeleted() {
		spec := map[string]any{}
		for k, v := range props {
			if sets.ReadOnly.Has(cfn.Pointer(k)) || m.s.Properties[k] == nil {
				continue
			}
			spec[names.ToField[k]] = m.s.FromCFN(m.s.Properties[k], v)
		}
		dspec := desired.spec()
		for _, ptr := range m.s.WriteOnlyProperties {
			name := strings.TrimPrefix(ptr, "/properties/")
			f := names.ToField[strings.Split(name, "/")[0]]
			if v, ok := dspec[f]; ok {
				spec[f] = v
			}
		}
		latest.setSpec(spec)
	}
	st := latest.status()
	arn := ""
	for k, v := range props {
		if strings.EqualFold(k, "arn") {
			arn, _ = v.(string)
		}
		if sets.ReadOnly.Has(cfn.Pointer(k)) && m.s.Properties[k] != nil {
			st[names.ToField[k]] = jsonValue(m.s.FromCFN(m.s.Properties[k], v))
		}
	}
	latest.setIdentifier(id)
	latest.setMetadata(m.account, m.region, arn)
}

// identifierFromSpec is the identifier when every primary-identifier
// property is writable and set (a name the user chose), else "".
func (m *manager) identifierFromSpec(r *resource) string {
	names, _ := cfn.Names(m.s.Properties)
	readOnly := m.s.Sets().ReadOnly
	spec := r.spec()
	var parts []string
	for _, p := range m.s.PrimaryIdentifierNames() {
		if readOnly.Has(cfn.Pointer(p)) {
			return ""
		}
		v, ok := spec[names.ToField[p]].(string)
		if !ok || v == "" {
			return ""
		}
		parts = append(parts, v)
	}
	return strings.Join(parts, "|")
}

// ReadOne finishes a pending request if there is one, then reads the
// resource by its identifier.
func (m *manager) ReadOne(ctx context.Context, res acktypes.AWSResource) (acktypes.AWSResource, error) {
	r := res.(*resource)
	latest := r.DeepCopy().(*resource)
	if q := latest.request(); q.pending() {
		out, err := m.api.GetResourceRequestStatus(ctx, &cloudcontrol.GetResourceRequestStatusInput{RequestToken: aws.String(q.Token)})
		if err != nil {
			if apiCode(err) == "RequestTokenNotFoundException" {
				// Expired (Cloud Control keeps requests for a while); read
				// the resource instead.
				latest.setRequest(request{})
			} else {
				return m.onError(latest, err)
			}
		} else {
			ev := out.ProgressEvent
			latest.setRequest(toRequest(ev))
			switch ev.OperationStatus {
			case cctypes.OperationStatusSuccess:
				if ev.Operation == cctypes.OperationDelete {
					latest.setIdentifier("")
					return latest, ackerr.NotFound
				}
				if id := aws.ToString(ev.Identifier); id != "" {
					latest.setIdentifier(id)
				}
			case cctypes.OperationStatusFailed, cctypes.OperationStatusCancelComplete:
				if ev.Operation == cctypes.OperationDelete && ev.ErrorCode == cctypes.HandlerErrorCodeNotFound {
					return latest, ackerr.NotFound
				}
				if ev.Operation == cctypes.OperationCreate {
					latest.setIdentifier("")
				}
				return m.onError(latest, handlerError(ev))
			default:
				return latest, inProgress(ev)
			}
		}
	}
	id := latest.identifier()
	if id == "" {
		id = m.identifierFromSpec(latest)
	}
	if id == "" {
		return latest, ackerr.NotFound
	}
	out, err := m.api.GetResource(ctx, &cloudcontrol.GetResourceInput{TypeName: m.typeName(), Identifier: aws.String(id)})
	if err != nil {
		if isNotFound(err) {
			return latest, ackerr.NotFound
		}
		return m.onError(latest, err)
	}
	var props map[string]any
	if out.ResourceDescription != nil && out.ResourceDescription.Properties != nil {
		if err := json.Unmarshal([]byte(*out.ResourceDescription.Properties), &props); err != nil {
			return m.onError(latest, fmt.Errorf("Cloud Control returned properties that are not JSON: %w", err))
		}
	}
	if out.ResourceDescription != nil && out.ResourceDescription.Identifier != nil {
		id = *out.ResourceDescription.Identifier
	}
	m.observe(latest, r, id, props)

	// Cloud Control never returns write-only values, so a changed Secret is
	// found by its digest; leaving the field out of latest makes the delta
	// see it and Update send it.
	if len(m.s.WriteOnlyProperties) > 0 && !latest.IsBeingDeleted() {
		_, digest, err := m.model(ctx, r)
		if err != nil {
			return m.onError(latest, err)
		}
		if digest != r.digest() {
			spec := latest.spec()
			names, _ := cfn.Names(m.s.Properties)
			for _, ptr := range m.s.WriteOnlyProperties {
				name := strings.Split(strings.TrimPrefix(ptr, "/properties/"), "/")[0]
				if !m.s.Sets().CreateOnly.Has(cfn.Pointer(name)) {
					delete(spec, names.ToField[name])
				}
			}
			latest.setSpec(spec)
		}
	}
	return m.onSuccess(latest)
}

// Create asks Cloud Control to create the resource and returns at once:
// done, failed, or in progress with the request token in status.
func (m *manager) Create(ctx context.Context, res acktypes.AWSResource) (acktypes.AWSResource, error) {
	r := res.(*resource)
	out := r.DeepCopy().(*resource)
	model, digest, err := m.model(ctx, r)
	if err != nil {
		return m.onError(out, err)
	}
	body, _ := json.Marshal(model)
	o, err := m.api.CreateResource(ctx, &cloudcontrol.CreateResourceInput{
		TypeName: m.typeName(), DesiredState: aws.String(string(body)), ClientToken: aws.String(token(r, "create", body)),
	})
	if err != nil {
		return m.onError(out, err)
	}
	ev := o.ProgressEvent
	out.setRequest(toRequest(ev))
	out.setDigest(digest)
	out.setIdentifier(aws.ToString(ev.Identifier))
	out.setMetadata(m.account, m.region, "")
	switch ev.OperationStatus {
	case cctypes.OperationStatusSuccess:
		return m.onSuccess(out)
	case cctypes.OperationStatusFailed:
		return m.onError(out, handlerError(ev))
	}
	// The runtime then calls ReadOne, which asks about the request.
	return out, ackerr.WrapPostCreateError(inProgress(ev))
}

// Update sends a JSON Patch of what differs (Patch has the rules).
func (m *manager) Update(ctx context.Context, desiredRes, latestRes acktypes.AWSResource, _ *ackcompare.Delta) (acktypes.AWSResource, error) {
	desired, latest := desiredRes.(*resource), latestRes.(*resource)
	out := desired.DeepCopy().(*resource)
	out.SetStatus(latest)
	id := latest.identifier()
	if id == "" {
		return m.onError(out, fmt.Errorf("no Cloud Control identifier to update"))
	}
	model, digest, err := m.model(ctx, desired)
	if err != nil {
		return m.onError(out, err)
	}
	observed := m.s.ToCFN(m.s.Root(), cfn.Normalise(latest.spec())).(map[string]any)
	ops, err := Patch(m.s, model, observed, digest != latest.digest())
	if err != nil {
		return m.onError(out, err)
	}
	out.setDigest(digest)
	if len(ops) == 0 {
		return m.onSuccess(out)
	}
	body, _ := json.Marshal(ops)
	o, err := m.api.UpdateResource(ctx, &cloudcontrol.UpdateResourceInput{
		TypeName: m.typeName(), Identifier: aws.String(id), PatchDocument: aws.String(string(body)),
		ClientToken: aws.String(token(desired, "update", body)),
	})
	if err != nil {
		out.setDigest(latest.digest())
		return m.onError(out, err)
	}
	ev := o.ProgressEvent
	out.setRequest(toRequest(ev))
	switch ev.OperationStatus {
	case cctypes.OperationStatusSuccess:
		return m.onSuccess(out)
	case cctypes.OperationStatusFailed:
		out.setDigest(latest.digest())
		return m.onError(out, handlerError(ev))
	}
	return out, inProgress(ev)
}

// Delete asks Cloud Control to delete the resource. In progress, the
// request token goes to status and ReadOne finishes it on the next pass.
func (m *manager) Delete(ctx context.Context, res acktypes.AWSResource) (acktypes.AWSResource, error) {
	r := res.(*resource)
	id := r.identifier()
	if id == "" {
		return nil, nil
	}
	out := r.DeepCopy().(*resource)
	o, err := m.api.DeleteResource(ctx, &cloudcontrol.DeleteResourceInput{
		TypeName: m.typeName(), Identifier: aws.String(id), ClientToken: aws.String(token(r, "delete", []byte(id))),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return m.onError(out, err)
	}
	ev := o.ProgressEvent
	out.setRequest(toRequest(ev))
	switch ev.OperationStatus {
	case cctypes.OperationStatusSuccess:
		return nil, nil
	case cctypes.OperationStatusFailed:
		if ev.ErrorCode == cctypes.HandlerErrorCodeNotFound {
			return nil, nil
		}
		return m.onError(out, handlerError(ev))
	}
	return out, inProgress(ev)
}

func (m *manager) ARNFromName(string) string { return "" }

func (m *manager) LateInitialize(_ context.Context, latest acktypes.AWSResource) (acktypes.AWSResource, error) {
	return latest, nil
}

// IsSynced is false while a Cloud Control request is in progress.
func (m *manager) IsSynced(_ context.Context, res acktypes.AWSResource) (bool, error) {
	return !res.(*resource).request().pending(), nil
}

// EnsureTags adds ACK's default tags (--resource-tags) to the type's tag
// property, when it is a top-level list of Key/Value objects.
func (m *manager) EnsureTags(_ context.Context, res acktypes.AWSResource, md acktypes.ServiceControllerMetadata) error {
	tp := m.s.TagProperty()
	if tp == "" {
		return nil
	}
	r := res.(*resource)
	defaults := ackrt.GetDefaultTags(&m.cfg, r.u, md)
	if len(defaults) == 0 {
		return nil
	}
	spec := r.spec()
	f := cfn.FieldName(tp)
	existing, _ := spec[f].([]any)
	have := acktags.NewTags()
	for _, t := range existing {
		if tm, ok := t.(map[string]any); ok {
			k, _ := tm["key"].(string)
			v, _ := tm["value"].(string)
			have[k] = v
		}
	}
	for k, v := range defaults {
		if _, ok := have[k]; !ok {
			existing = append(existing, map[string]any{"key": k, "value": v})
		}
	}
	spec[f] = existing
	r.setSpec(spec)
	return nil
}

// FilterSystemTags drops ACK's default tags and aws: tags from the tag
// property, so they are not written into the object's spec.
func (m *manager) FilterSystemTags(res acktypes.AWSResource, systemTags []string) {
	tp := m.s.TagProperty()
	if tp == "" || res == nil {
		return
	}
	r := res.(*resource)
	spec := r.spec()
	f := cfn.FieldName(tp)
	existing, ok := spec[f].([]any)
	if !ok {
		return
	}
	drop := map[string]bool{}
	for _, k := range systemTags {
		drop[strings.SplitN(k, "=", 2)[0]] = true
	}
	var kept []any
	for _, t := range existing {
		tm, _ := t.(map[string]any)
		k, _ := tm["key"].(string)
		if drop[k] || strings.HasPrefix(k, "aws:") {
			continue
		}
		kept = append(kept, t)
	}
	if len(kept) == 0 {
		delete(spec, f)
	} else {
		spec[f] = kept
	}
	r.setSpec(spec)
}

// ResolveReferences: Cloud Control kinds have no references yet (the
// schemas' relationshipRef could generate them; docs/cloudcontrol.md).
func (m *manager) ResolveReferences(_ context.Context, _ client.Reader, res acktypes.AWSResource) (acktypes.AWSResource, bool, error) {
	return res, false, nil
}

// ClearResolvedReferences returns a copy, as ACK's generated managers do:
// the runtime patches the object it gets back and writes the API server's
// answer into it, which must not overwrite the caller's observed state.
func (m *manager) ClearResolvedReferences(res acktypes.AWSResource) acktypes.AWSResource {
	return res.DeepCopy()
}

// onError and onSuccess set ACK.Terminal and ACK.Recoverable as ACK's
// generated managers do.
func (m *manager) onError(r *resource, err error) (acktypes.AWSResource, error) {
	err = classify(err)
	var requeue *ackrequeue.RequeueNeededAfter
	if errors.As(err, &requeue) {
		return r, err
	}
	r = m.conditions(r, err)
	for _, c := range r.Conditions() {
		if c.Type == ackv1alpha1.ConditionTypeTerminal && c.Status == corev1.ConditionTrue {
			return r, ackerr.Terminal
		}
	}
	return r, err
}

func (m *manager) onSuccess(r *resource) (acktypes.AWSResource, error) {
	return m.conditions(r, nil), nil
}

func (m *manager) conditions(r *resource, err error) *resource {
	out := r.DeepCopy().(*resource)
	cs := out.Conditions()
	var terminal, recoverable *ackv1alpha1.Condition
	for _, c := range cs {
		switch c.Type {
		case ackv1alpha1.ConditionTypeTerminal:
			terminal = c
		case ackv1alpha1.ConditionTypeRecoverable:
			recoverable = c
		}
	}
	var te *ackerr.TerminalError
	if errors.As(err, &te) {
		if terminal == nil {
			terminal = &ackv1alpha1.Condition{Type: ackv1alpha1.ConditionTypeTerminal}
			cs = append(cs, terminal)
		}
		msg := err.Error()
		terminal.Status, terminal.Message = corev1.ConditionTrue, &msg
	} else {
		if terminal != nil {
			terminal.Status, terminal.Message = corev1.ConditionFalse, nil
		}
		if err != nil {
			if recoverable == nil {
				recoverable = &ackv1alpha1.Condition{Type: ackv1alpha1.ConditionTypeRecoverable}
				cs = append(cs, recoverable)
			}
			msg := err.Error()
			recoverable.Status, recoverable.Message = corev1.ConditionTrue, &msg
		} else if recoverable != nil {
			recoverable.Status, recoverable.Message = corev1.ConditionFalse, nil
		}
	}
	out.ReplaceConditions(cs)
	return out
}
