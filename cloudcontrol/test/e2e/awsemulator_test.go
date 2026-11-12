// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/cfn"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
	"github.com/highfivelabs/ack-extensions/pkg/test/awsemulator"
)

// TestEveryKindOnAWSEmulator runs every Cloud Control kind, the moto-proven
// ones too, through one manager against AWS Emulator's Cloud Control
// (ACK_TEST_BACKEND=aws-emulator only; docs/aws-emulator.md). Each kind gets
// the schema fake test's example object (exampleSpec) and five steps:
//
//   - create: the object synced and ready, with an identifier;
//   - read: GetResource answers, with the spec's values;
//   - list: ListResources names the identifier;
//   - update: one mutable field of the spec changed, and GetResource shows it;
//   - delete: the object gone, and GetResource says NotFound.
//
// Beside them, what would hide a bug: whether the identifier has the shape
// the type's primaryIdentifier gives it on AWS, and how many of the type's
// read-only properties the emulator fills in.
//
// It is a survey, not a gate: a step's failure is a row in the report
// (ACK_EMULATOR_REPORT, a CSV path), not a test failure. With
// ACK_EMULATOR_EXPECT set to a CSV the test does fail where an outcome
// differs from it, so a pinned emulator release can be held to what it did.
func TestEveryKindOnAWSEmulator(t *testing.T) {
	if !awsemulator.Selected(t) {
		t.Skip("ACK_TEST_BACKEND is not aws-emulator (docs/aws-emulator.md)")
	}
	began := time.Now()
	e := start(t)
	root := repoRoot(t)
	cc := cloudcontrol.NewFromConfig(e.moto)

	type item struct {
		ty   *registry.Type
		u    *unstructured.Unstructured
		spec map[string]any
		id   string
		res  map[string]string
		// field and value: the update's change, in the spec's names.
		field, cfnName string
		value          any
	}
	only := os.Getenv("ACK_EMULATOR_TYPES")
	var items []*item
	for i, ty := range registry.Types {
		if only != "" && !strings.Contains(","+only+",", ","+ty.TypeName+",") {
			continue
		}
		spec, secrets := exampleSpec(t, root, ty, fmt.Sprintf("em-%d", i))
		for _, s := range secrets {
			if err := e.c.Create(e.ctx, s); err != nil {
				t.Fatal(err)
			}
		}
		u := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
		u.SetGroupVersionKind(ty.GVK())
		u.SetNamespace(envNS)
		u.SetName(fmt.Sprintf("%s-%d", strings.ToLower(ty.Kind), i))
		it := &item{ty: ty, u: u, spec: spec, res: map[string]string{}}
		if err := e.c.Create(e.ctx, u.DeepCopy()); err != nil {
			it.res["create"] = "fail: the API server refused the example: " + short(err.Error())
		}
		items = append(items, it)
	}
	t.Logf("%d kinds on aws-emulator", len(items))
	ok := func(it *item, steps ...string) bool {
		for _, s := range steps {
			if it.res[s] != "ok" {
				return false
			}
		}
		return true
	}
	waitAll := func(step string, timeout time.Duration, live func(*item) bool, f func(*item) error) {
		left := map[*item]error{}
		for deadline := time.Now().Add(timeout); ; time.Sleep(time.Second) {
			left = map[*item]error{}
			for _, it := range items {
				if !live(it) || it.res[step] != "" {
					continue
				}
				if err := f(it); err != nil {
					var term terminal
					if errors.As(err, &term) {
						it.res[step] = "fail: " + short(err.Error())
						continue
					}
					left[it] = err
				} else {
					it.res[step] = "ok"
				}
			}
			if len(left) == 0 || time.Now().After(deadline) {
				break
			}
		}
		for it, err := range left {
			it.res[step] = "fail: " + short(err.Error())
		}
	}
	getObj := func(it *item) (*unstructured.Unstructured, error) {
		got := &unstructured.Unstructured{}
		got.SetGroupVersionKind(it.u.GroupVersionKind())
		return got, e.c.Get(e.ctx, client.ObjectKeyFromObject(it.u), got)
	}
	ready := func(it *item) error {
		got, err := getObj(it)
		if err != nil {
			return err
		}
		if s, msg := condition(got, ackv1alpha1.ConditionTypeTerminal); s == "True" {
			return terminal{"terminal: " + msg}
		}
		s, msg := condition(got, ackv1alpha1.ConditionTypeResourceSynced)
		r, _ := condition(got, ackv1alpha1.ConditionTypeReady)
		if s != "True" || r != "True" {
			return fmt.Errorf("synced=%q ready=%q: %s", s, r, msg)
		}
		return nil
	}

	// Create: synced and ready with an identifier.
	createStart := time.Now()
	waitAll("create", 4*time.Minute, func(*item) bool { return true }, func(it *item) error {
		if err := ready(it); err != nil {
			return err
		}
		got, _ := getObj(it)
		it.id, _, _ = unstructured.NestedString(got.Object, "status", "cloudControl", "identifier")
		if it.id == "" {
			return fmt.Errorf("no status.cloudControl.identifier")
		}
		return nil
	})
	createTook := time.Since(createStart)

	// Read: the spec's values back, and what would hide a bug.
	for _, it := range items {
		if !ok(it, "create") {
			continue
		}
		sc := it.ty.Schema()
		model, err := getResource(e, cc, it.ty.TypeName, it.id)
		if err != nil {
			it.res["read"] = "fail: " + short(err.Error())
			continue
		}
		want, _ := sc.ToCFN(sc.Root(), cfn.Normalise(it.spec)).(map[string]any)
		skip := map[string]bool{}
		for _, p := range it.ty.SecretPaths() {
			skip[p[0]] = true
		}
		for _, p := range sc.WriteOnlyProperties {
			skip[strings.Split(strings.TrimPrefix(p, "/properties/"), "/")[0]] = true
		}
		if sc.Tagging != nil && sc.Tagging.TagProperty != "" {
			skip[strings.TrimPrefix(sc.Tagging.TagProperty, "/properties/")] = true
		}
		var diffs []string
		for k, v := range want {
			if !skip[k] && !reflect.DeepEqual(jsonRound(v), jsonRound(model[k])) {
				diffs = append(diffs, k)
			}
		}
		sort.Strings(diffs)
		if len(diffs) > 0 {
			it.res["read"] = "fail: differs from the spec in " + strings.Join(diffs, " ")
		} else {
			it.res["read"] = "ok"
		}
		// The read-only properties AWS fills in (an ARN, a server-assigned id).
		var ro, filled int
		for _, p := range sc.ReadOnlyProperties {
			name := strings.TrimPrefix(p, "/properties/")
			if strings.Contains(name, "/") {
				continue
			}
			ro++
			if _, ok := model[name]; ok {
				filled++
			}
		}
		it.res["readonly"] = fmt.Sprintf("%d/%d", filled, ro)
		it.res["identifier"] = identifierShape(sc, it.id, model)
	}

	// List: the identifier among the type's resources.
	for _, it := range items {
		if !ok(it, "create") {
			continue
		}
		ids, err := listResources(e, cc, it.ty.TypeName)
		switch {
		case err != nil:
			it.res["list"] = "fail: " + short(err.Error())
		case !ids[it.id]:
			it.res["list"] = fmt.Sprintf("fail: %d listed, not %q", len(ids), it.id)
		default:
			it.res["list"] = "ok"
		}
	}

	// Resyncs of unchanged objects make no update (as the schema fake test).
	time.Sleep(12 * time.Second)
	for _, it := range items {
		if ok(it, "create") {
			if n := len(e.calls("UpdateResource", it.ty.TypeName)); n != 0 {
				it.res["resync"] = fmt.Sprintf("%d update(s) of an unchanged object", n)
			}
		}
	}

	// Update: one mutable field through the object, seen in GetResource.
	for _, it := range items {
		if !ok(it, "create") {
			continue
		}
		it.field, it.cfnName, it.value = updatable(t, root, it.ty, it.spec)
		if it.field == "" {
			it.res["update"] = "n/a: no mutable string field"
			continue
		}
		err := e.update(t, it.u, func(cur *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(cur.Object, it.value, "spec", it.field)
		})
		if err != nil {
			it.res["update"] = "fail: the API server refused the change: " + short(err.Error())
		}
	}
	waitAll("update", 3*time.Minute, func(it *item) bool { return ok(it, "create") }, func(it *item) error {
		if err := ready(it); err != nil {
			return err
		}
		model, err := getResource(e, cc, it.ty.TypeName, it.id)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(jsonRound(model[it.cfnName]), jsonRound(it.value)) {
			return fmt.Errorf("%s is %s, not %s", it.cfnName, js(model[it.cfnName]), js(it.value))
		}
		return nil
	})

	// Delete: the object goes, and so does the resource.
	for _, it := range items {
		if !ok(it, "create") {
			continue
		}
		if it.ty.NeverDelete {
			it.res["delete"] = "n/a: neverDelete"
			continue
		}
		if err := e.c.Delete(e.ctx, it.u.DeepCopy()); err != nil {
			it.res["delete"] = "fail: " + short(err.Error())
		}
	}
	waitAll("delete", 3*time.Minute, func(it *item) bool { return ok(it, "create") }, func(it *item) error {
		if _, err := getObj(it); !apierrors.IsNotFound(err) {
			return fmt.Errorf("the object is still there (err %v)", err)
		}
		_, err := getResource(e, cc, it.ty.TypeName, it.id)
		var nf *cctypes.ResourceNotFoundException
		if !errors.As(err, &nf) {
			return fmt.Errorf("GetResource after the delete: %v", err)
		}
		return nil
	})

	// Two probes, straight to Cloud Control, of refusals AWS makes: a
	// desired state missing the type's required properties (AWS fails it
	// in model validation), and a patch to a create-only property (AWS
	// fails it NotUpdatable). An emulator that accepts either would pass a
	// test of a bug AWS catches.
	probeRefusals(t, e, cc, items2(items, func(it *item) (*registry.Type, map[string]any) { return it.ty, it.spec }), func(i int, k, v string) { items[i].res[k] = v })

	// The report.
	steps := []string{"create", "read", "list", "update", "delete"}
	counts := map[string]int{}
	rows := [][]string{{"type", "service", "outcome", "create", "read", "list", "update", "delete", "identifier", "readonly", "resync", "required", "createonly"}}
	for _, it := range items {
		outcome := "works"
		var failed []string
		for _, s := range steps {
			if r := it.res[s]; r != "ok" && !strings.HasPrefix(r, "n/a") {
				failed = append(failed, s)
			}
		}
		switch {
		case !ok(it, "create"):
			outcome = "unsupported"
		case len(failed) > 0:
			outcome = "partial: " + strings.Join(failed, " ")
		}
		counts[strings.SplitN(outcome, ":", 2)[0]]++
		row := []string{it.ty.TypeName, strings.Split(it.ty.TypeName, "::")[1], outcome}
		for _, s := range []string{"create", "read", "list", "update", "delete", "identifier", "readonly", "resync", "required", "createonly"} {
			v := it.res[s]
			if v == "" && (s == "create" || s == "read" || s == "list" || s == "update" || s == "delete") {
				v = "skipped"
			}
			row = append(row, v)
		}
		rows = append(rows, row)
	}
	t.Logf("aws-emulator: %d kinds: %v; emulator healthy in %s; creates settled in %s; whole test %s; emulator peak RSS %d MiB",
		len(items), counts, e.emulator.Started.Round(time.Millisecond), createTook.Round(time.Second),
		time.Since(began).Round(time.Second), e.emulator.MaxRSS()/1024)
	if p := os.Getenv("ACK_EMULATOR_REPORT"); p != "" {
		writeCSV(t, p, rows)
	}
	if p := os.Getenv("ACK_EMULATOR_EXPECT"); p != "" {
		compareOutcomes(t, p, rows)
	}
}

// terminal is a failure no wait will change.
type terminal struct{ msg string }

func (e terminal) Error() string { return e.msg }

func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

func getResource(e *env, cc *cloudcontrol.Client, typeName, id string) (map[string]any, error) {
	out, err := cc.GetResource(e.ctx, &cloudcontrol.GetResourceInput{TypeName: aws.String(typeName), Identifier: aws.String(id)})
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if out.ResourceDescription == nil || out.ResourceDescription.Properties == nil {
		return nil, fmt.Errorf("GetResource: no properties")
	}
	if err := json.Unmarshal([]byte(*out.ResourceDescription.Properties), &m); err != nil {
		return nil, fmt.Errorf("GetResource: properties: %v", err)
	}
	return m, nil
}

func listResources(e *env, cc *cloudcontrol.Client, typeName string) (map[string]bool, error) {
	ids := map[string]bool{}
	p := cloudcontrol.NewListResourcesPaginator(cc, &cloudcontrol.ListResourcesInput{TypeName: aws.String(typeName)})
	for p.HasMorePages() {
		out, err := p.NextPage(e.ctx)
		if err != nil {
			return ids, err
		}
		for _, d := range out.ResourceDescriptions {
			ids[aws.ToString(d.Identifier)] = true
		}
	}
	return ids, nil
}

// identifierShape compares the identifier with the one AWS would give: the
// values of the type's primaryIdentifier properties, joined by "|".
func identifierShape(sc *cfn.Schema, id string, model map[string]any) string {
	var parts []string
	for _, p := range sc.PrimaryIdentifier {
		v, ok := model[strings.TrimPrefix(p, "/properties/")]
		if !ok {
			return "unknown: the model has no " + p
		}
		parts = append(parts, fmt.Sprint(v))
	}
	if want := strings.Join(parts, "|"); want != id {
		return fmt.Sprintf("wrong: %q, AWS's shape is %q", id, want)
	}
	return "ok"
}

// updatable picks the update's change: a top-level string property that
// is neither create-only, read-only, write-only, a secret nor part of the
// identifier; one the example already sets first (its value changed),
// else any (the CRD's least value). Empty when the type has none.
func updatable(t *testing.T, root string, ty *registry.Type, spec map[string]any) (field, cfnName string, value any) {
	sc := ty.Schema()
	names, err := cfn.Names(sc.Properties)
	if err != nil {
		return "", "", nil
	}
	fixed := map[string]bool{}
	for _, l := range [][]string{sc.CreateOnlyProperties, sc.ReadOnlyProperties, sc.WriteOnlyProperties, sc.PrimaryIdentifier} {
		for _, p := range l {
			fixed[strings.Split(strings.TrimPrefix(p, "/properties/"), "/")[0]] = true
		}
	}
	for _, p := range ty.SecretPaths() {
		fixed[p[0]] = true
	}
	if sc.Tagging != nil && sc.Tagging.TagProperty != "" {
		fixed[strings.TrimPrefix(sc.Tagging.TagProperty, "/properties/")] = true
	}
	specSchema := crdSpecSchema(t, root, ty)
	var present, absent []string
	for name := range sc.Properties {
		f := names.ToField[name]
		cp, ok := specSchema.Properties[f]
		if fixed[name] || !ok || cp.Type != "string" || len(cp.Enum) > 0 || cp.Format != "" {
			continue
		}
		if _, ok := spec[f]; ok {
			present = append(present, name)
		} else {
			absent = append(absent, name)
		}
	}
	sort.Strings(present)
	sort.Strings(absent)
	pick := func(name string) (string, string, any) {
		f := names.ToField[name]
		cp := specSchema.Properties[f]
		if cur, ok := spec[f].(string); ok {
			v := cur + "u"
			if cp.MaxLength != nil && int64(len(v)) > *cp.MaxLength {
				v = cur[:len(cur)-1] + "u"
			}
			return f, name, v
		}
		return f, name, synth(&cp, "spec."+f)
	}
	switch {
	case len(present) > 0:
		return pick(present[0])
	case len(absent) > 0:
		return pick(absent[0])
	}
	return "", "", nil
}

func crdSpecSchema(t *testing.T, root string, ty *registry.Type) apiextv1.JSONSchemaProps {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "cloudcontrol", "config", "crds", ty.Group+"_"+ty.Plural+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var crd apiextv1.CustomResourceDefinition
	if err := yaml.Unmarshal(b, &crd); err != nil {
		t.Fatalf("%s: %v", ty.TypeName, err)
	}
	return crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
}

func writeCSV(t *testing.T, path string, rows [][]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := csv.NewWriter(f)
	if err := w.WriteAll(rows); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("report: %s", path)
}

// compareOutcomes fails where a kind's outcome differs from the one
// recorded at path (a report this test wrote).
func compareOutcomes(t *testing.T, path string, rows [][]string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	want, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{}
	for _, r := range want[1:] {
		expected[r[0]] = r[2]
	}
	for _, r := range rows[1:] {
		if w, ok := expected[r[0]]; ok && w != r[2] {
			t.Errorf("%s: %s, recorded as %s in %s", r[0], r[2], w, path)
		}
	}
}

type probeItem struct {
	ty   *registry.Type
	spec map[string]any
}

func items2[T any](in []T, f func(T) (*registry.Type, map[string]any)) []probeItem {
	out := make([]probeItem, len(in))
	for i, x := range in {
		out[i].ty, out[i].spec = f(x)
	}
	return out
}

// requestOutcome waits for a Cloud Control request to end: "SUCCESS", or
// "FAILED <code>: <message>".
func requestOutcome(e *env, cc *cloudcontrol.Client, token string) string {
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		out, err := cc.GetResourceRequestStatus(e.ctx, &cloudcontrol.GetResourceRequestStatusInput{RequestToken: aws.String(token)})
		if err != nil {
			return "error: " + short(err.Error())
		}
		pe := out.ProgressEvent
		switch pe.OperationStatus {
		case cctypes.OperationStatusSuccess:
			return "SUCCESS " + aws.ToString(pe.Identifier)
		case cctypes.OperationStatusFailed:
			return fmt.Sprintf("FAILED %s: %s", pe.ErrorCode, short(aws.ToString(pe.StatusMessage)))
		}
	}
	return "error: no end in 90s"
}

// probeRefusals runs the two refusal probes for every kind at once and
// sets "required" and "createonly" through set: "refused" (as AWS), or
// "accepted" (a gap), or "n/a".
func probeRefusals(t *testing.T, e *env, cc *cloudcontrol.Client, items []probeItem, set func(i int, k, v string)) {
	t.Helper()
	type pending struct {
		i     int
		token string
	}
	// Deletes of what a probe made: sent at once, waited for at the end.
	var deletes []string
	defer func() {
		for _, tok := range deletes {
			requestOutcome(e, cc, tok)
		}
	}()
	cleanup := func(typeName, outcome string) {
		if id, ok := strings.CutPrefix(outcome, "SUCCESS "); ok && id != "" {
			if out, err := cc.DeleteResource(e.ctx, &cloudcontrol.DeleteResourceInput{TypeName: aws.String(typeName), Identifier: aws.String(id)}); err == nil {
				deletes = append(deletes, aws.ToString(out.ProgressEvent.RequestToken))
			}
		}
	}
	verdict := func(err error, outcome string) string {
		switch {
		case err != nil:
			return "refused: " + short(err.Error())
		case strings.HasPrefix(outcome, "FAILED"):
			return "refused: " + outcome
		case strings.HasPrefix(outcome, "SUCCESS"):
			return "accepted"
		}
		return outcome
	}

	// Required: an empty desired state.
	var reqs []pending
	for i, it := range items {
		if len(it.ty.Schema().Required) == 0 {
			set(i, "required", "n/a: none required")
			continue
		}
		out, err := cc.CreateResource(e.ctx, &cloudcontrol.CreateResourceInput{TypeName: aws.String(it.ty.TypeName), DesiredState: aws.String("{}")})
		if err != nil {
			set(i, "required", verdict(err, ""))
			continue
		}
		reqs = append(reqs, pending{i, aws.ToString(out.ProgressEvent.RequestToken)})
	}
	for _, p := range reqs {
		o := requestOutcome(e, cc, p.token)
		set(p.i, "required", verdict(nil, o))
		cleanup(items[p.i].ty.TypeName, o)
	}

	// Create-only: the example made again, then one create-only property
	// that is not the identifier changed.
	type made struct {
		pending
		prop  string
		value any
	}
	var creates []made
	for i, it := range items {
		sc := it.ty.Schema()
		want, _ := sc.ToCFN(sc.Root(), cfn.Normalise(it.spec)).(map[string]any)
		ident := map[string]bool{}
		for _, p := range sc.PrimaryIdentifier {
			ident[strings.TrimPrefix(p, "/properties/")] = true
		}
		prop := ""
		for _, p := range sc.CreateOnlyProperties {
			name := strings.TrimPrefix(p, "/properties/")
			if _, ok := want[name].(string); ok && !ident[name] && !strings.Contains(name, "/") {
				prop = name
				break
			}
		}
		if prop == "" {
			set(i, "createonly", "n/a: no create-only string in the example")
			continue
		}
		b, _ := json.Marshal(want)
		out, err := cc.CreateResource(e.ctx, &cloudcontrol.CreateResourceInput{TypeName: aws.String(it.ty.TypeName), DesiredState: aws.String(string(b))})
		if err != nil {
			set(i, "createonly", "n/a: the create failed: "+short(err.Error()))
			continue
		}
		creates = append(creates, made{pending{i, aws.ToString(out.ProgressEvent.RequestToken)}, prop, want[prop].(string) + "x"})
	}
	type update struct {
		made
		id, token string
	}
	var updates []update
	for _, c := range creates {
		ty := items[c.i].ty
		o := requestOutcome(e, cc, c.token)
		id, ok := strings.CutPrefix(o, "SUCCESS ")
		if !ok {
			set(c.i, "createonly", "n/a: the create "+o)
			continue
		}
		patch, _ := json.Marshal([]map[string]any{{"op": "replace", "path": "/" + c.prop, "value": c.value}})
		out, err := cc.UpdateResource(e.ctx, &cloudcontrol.UpdateResourceInput{TypeName: aws.String(ty.TypeName), Identifier: aws.String(id), PatchDocument: aws.String(string(patch))})
		if err != nil {
			set(c.i, "createonly", c.prop+": "+verdict(err, ""))
			cleanup(ty.TypeName, "SUCCESS "+id)
			continue
		}
		updates = append(updates, update{c, id, aws.ToString(out.ProgressEvent.RequestToken)})
	}
	for _, u := range updates {
		set(u.i, "createonly", u.prop+": "+verdict(nil, requestOutcome(e, cc, u.token)))
		cleanup(items[u.i].ty.TypeName, "SUCCESS "+u.id)
	}
}
