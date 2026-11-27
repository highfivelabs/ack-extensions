// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	apiservervalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"sigs.k8s.io/yaml"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/cfn"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/test/fake"
)

type upjetFixture struct {
	TypeName string         `json:"typeName"`
	Group    string         `json:"group"`
	Kind     string         `json:"kind"`
	Plural   string         `json:"plural"`
	Example  string         `json:"example"`
	Upstream string         `json:"upstream"`
	Slice    string         `json:"slice"`
	Spec     map[string]any `json:"spec"`
}

// crdValidator is the API server's validation of an object of a
// generated CRD: its OpenAPI schema and its CEL rules.
func crdValidator(t *testing.T, path string) func(obj map[string]any) error {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var crd apiextv1.CustomResourceDefinition
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatal(err)
	}
	var internal apiextensions.CustomResourceValidation
	if err := apiextv1.Convert_v1_CustomResourceValidation_To_apiextensions_CustomResourceValidation(crd.Spec.Versions[0].Schema, &internal, nil); err != nil {
		t.Fatal(err)
	}
	sv, _, err := apiservervalidation.NewSchemaValidator(internal.OpenAPIV3Schema)
	if err != nil {
		t.Fatal(err)
	}
	ss, err := structuralschema.NewStructural(internal.OpenAPIV3Schema)
	if err != nil {
		t.Fatal(err)
	}
	cv := cel.NewValidator(ss, true, celconfig.PerCallLimit)
	return func(obj map[string]any) error {
		errs := apiservervalidation.ValidateCustomResource(nil, obj, sv)
		if cv != nil {
			more, _ := cv.Validate(context.Background(), field.NewPath(""), ss, obj, nil, celconfig.RuntimeCELCostBudget)
			errs = append(errs, more...)
		}
		if len(errs) > 0 {
			return errs.ToAggregate()
		}
		return nil
	}
}

// TestUpjetExamples runs every hand-written Upjet example a Cloud Control
// kind can express (HFL-700; hack/cloudcontrol-gen/upjet_examples.go,
// cloudcontrol/zz_generated.upjet.md lists the rest and why): the object
// is accepted by the kind's generated CRD (schema and CEL rules, as the
// API server would), its spec reaches the fake Cloud Control as the
// type's CloudFormation model and passes Cloud Control's own rules
// (unknown, read-only and required properties), reads back with every
// property it set, and is deleted. On the schema fake: it proves the
// example fits our kind, not what the service would make of its values.
func TestUpjetExamples(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "zz_generated.upjet-examples.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Examples []upjetFixture `json:"examples"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Examples) == 0 {
		t.Fatal("no Upjet example fixtures")
	}
	ctx := context.Background()
	srv := fake.New()
	srv.Polls = 0
	registered := map[string]bool{}
	for _, x := range doc.Examples {
		if !registered[x.TypeName] {
			ty := registry.ByTypeName(x.TypeName)
			if ty == nil {
				t.Fatalf("%s: not served", x.TypeName)
			}
			srv.Register(ty.Schema(), fake.NewSchemaStore(ty.Schema(), "000000000000", "us-west-2"))
			registered[x.TypeName] = true
		}
	}
	u := srv.Start()
	defer srv.Close()
	cfg, err := fake.AWSConfig(u, "us-west-2")
	if err != nil {
		t.Fatal(err)
	}
	c := cloudcontrol.NewFromConfig(cfg)
	validators := map[string]func(map[string]any) error{}
	for i, x := range doc.Examples {
		t.Run(fmt.Sprintf("%s/%d", x.Example, i), func(t *testing.T) {
			name := x.Group + "_" + x.Plural
			v := validators[name]
			if v == nil {
				v = crdValidator(t, filepath.Join(root, "config", "crds", name+".yaml"))
				validators[name] = v
			}
			obj := map[string]any{
				"apiVersion": x.Group + "/v1alpha1", "kind": x.Kind,
				"metadata": map[string]any{"name": "upjet-example", "namespace": "default"},
				"spec":     x.Spec,
			}
			if err := v(obj); err != nil {
				t.Fatalf("%s (%s, %s): the CRD refuses it: %v", x.Upstream, x.Slice, x.TypeName, err)
			}
			s := registry.ByTypeName(x.TypeName).Schema()
			model, _ := s.ToCFN(s.Root(), x.Spec).(map[string]any)
			body, _ := json.Marshal(model)
			typ := aws.String(x.TypeName)
			o, err := c.CreateResource(ctx, &cloudcontrol.CreateResourceInput{TypeName: typ, DesiredState: aws.String(string(body))})
			if err != nil {
				t.Fatal(err)
			}
			ev := o.ProgressEvent
			for ev.OperationStatus == cctypes.OperationStatusInProgress || ev.OperationStatus == cctypes.OperationStatusPending {
				st, err := c.GetResourceRequestStatus(ctx, &cloudcontrol.GetResourceRequestStatusInput{RequestToken: ev.RequestToken})
				if err != nil {
					t.Fatal(err)
				}
				ev = st.ProgressEvent
			}
			if ev.OperationStatus != cctypes.OperationStatusSuccess {
				t.Fatalf("%s (%s): create %s: %s %s", x.Upstream, x.Slice, ev.OperationStatus, ev.ErrorCode, aws.ToString(ev.StatusMessage))
			}
			id := ev.Identifier
			got, err := c.GetResource(ctx, &cloudcontrol.GetResourceInput{TypeName: typ, Identifier: id})
			if err != nil {
				t.Fatal(err)
			}
			var read map[string]any
			if err := json.Unmarshal([]byte(aws.ToString(got.ResourceDescription.Properties)), &read); err != nil {
				t.Fatal(err)
			}
			wo := s.Sets().WriteOnly
			for k, want := range model {
				if wo.Has(cfn.Pointer(k)) {
					continue
				}
				if !reflect.DeepEqual(cfn.Normalise(read[k]), cfn.Normalise(want)) {
					t.Errorf("%s: %s read back as %v, want %v", x.Upstream, k, read[k], want)
				}
			}
			d, err := c.DeleteResource(ctx, &cloudcontrol.DeleteResourceInput{TypeName: typ, Identifier: id})
			if err != nil || d.ProgressEvent.OperationStatus == cctypes.OperationStatusFailed {
				t.Fatalf("delete: %v %+v", err, d)
			}
		})
	}
}
