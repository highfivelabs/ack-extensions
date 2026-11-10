// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"encoding/json"
	"testing"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
)

// A nested write-only pointer takes out only its value: ConfigRule's
// Source stays, without Source.CustomPolicyDetails.PolicyText, and putting
// the write-only values back gives the model sent.
func TestSplitWriteOnlyNested(t *testing.T) {
	sc := registry.ByTypeName("AWS::Config::ConfigRule").Schema()
	sent := map[string]any{
		"ConfigRuleName": "r",
		"Source": map[string]any{
			"Owner":               "CUSTOM_POLICY",
			"CustomPolicyDetails": map[string]any{"PolicyRuntime": "guard-2.x.x", "PolicyText": "rule x {}"},
		},
	}
	model := deepCopy(sent)
	wo := splitWriteOnly(sc, model)
	js := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	if got, want := js(model), `{"ConfigRuleName":"r","Source":{"CustomPolicyDetails":{"PolicyRuntime":"guard-2.x.x"},"Owner":"CUSTOM_POLICY"}}`; got != want {
		t.Errorf("model %s\nwant %s", got, want)
	}
	if got, want := js(wo), `{"Source":{"CustomPolicyDetails":{"PolicyText":"rule x {}"}}}`; got != want {
		t.Errorf("write-only %s\nwant %s", got, want)
	}
	if got := js(withWriteOnly(model, wo)); got != js(sent) {
		t.Errorf("put back %s\nwant %s", got, js(sent))
	}
}
