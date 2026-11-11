// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func secretPolicy(principal string) map[string]any {
	return map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{map[string]any{
			"Effect":    "Allow",
			"Principal": map[string]any{"AWS": principal},
			"Action":    "secretsmanager:GetSecretValue",
			"Resource":  "*",
		}},
	}
}

// F17, Secrets Manager: a secret's resource policy (a free-form document),
// a resync of an unchanged policy, a change, drift made in Secrets Manager
// put back, a create-only secret refused; its delete leaves the secret.
func TestSecretResourcePolicy(t *testing.T) {
	e := start(t)
	sm := secretsmanager.NewFromConfig(e.moto)
	const typ = "AWS::SecretsManager::ResourcePolicy"
	reader := fmt.Sprintf("arn:aws:iam::%s:role/reader", e.account)
	auditor := fmt.Sprintf("arn:aws:iam::%s:role/auditor", e.account)

	s, err := sm.CreateSecret(e.ctx, &secretsmanager.CreateSecretInput{Name: aws.String("db-password"), SecretString: aws.String("not-a-real-password")})
	if err != nil {
		t.Fatal(err)
	}
	arn := aws.ToString(s.ARN)
	t.Cleanup(func() {
		_, _ = sm.DeleteSecret(e.ctx, &secretsmanager.DeleteSecretInput{SecretId: aws.String(arn), ForceDeleteWithoutRecovery: aws.Bool(true)})
	})
	inMoto := func() (string, error) {
		out, err := sm.GetResourcePolicy(e.ctx, &secretsmanager.GetResourcePolicyInput{SecretId: aws.String(arn)})
		if err != nil {
			return "", err
		}
		return aws.ToString(out.ResourcePolicy), nil
	}

	u := obj("ResourcePolicy", "db-password-policy", map[string]any{
		"secretId":          arn,
		"resourcePolicy":    secretPolicy(reader),
		"blockPublicPolicy": true,
	})
	if err := e.c.Create(e.ctx, u); err != nil {
		t.Fatal(err)
	}
	got := e.waitSynced(t, u)
	if id, _, _ := unstructured.NestedString(got.Object, "status", "id"); id != arn {
		t.Errorf("status.id %q, want the secret's ARN %q", id, arn)
	}
	if p, err := inMoto(); err != nil || !strings.Contains(p, reader) {
		t.Fatalf("moto's policy: %v %s", err, p)
	}

	// Resyncs of an unchanged policy (a document compared whole; the
	// write-only BlockPublicPolicy never read back) make no update.
	time.Sleep(12 * time.Second)
	if n := len(e.calls("UpdateResource", typ)); n != 0 {
		t.Errorf("%d update(s) of an unchanged policy: %v", n, patchPaths(t, e.calls("UpdateResource", typ)))
	}

	// A new principal: one replace of the document.
	if err := e.update(t, u, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, secretPolicy(auditor), "spec", "resourcePolicy")
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "the new principal in moto", func() error {
		p, err := inMoto()
		if err != nil {
			return err
		}
		if !strings.Contains(p, auditor) {
			return fmt.Errorf("%s", p)
		}
		return nil
	})
	if p := patchPaths(t, e.calls("UpdateResource", typ)); strings.Join(p, ",") != "replace /ResourcePolicy" {
		t.Errorf("patches: %v, want one replace of /ResourcePolicy", p)
	}
	e.waitSynced(t, u)

	// Drift: another policy put in Secrets Manager; the resync puts ours back.
	before := len(e.calls("UpdateResource", typ))
	other, _ := json.Marshal(secretPolicy(reader))
	if _, err := sm.PutResourcePolicy(e.ctx, &secretsmanager.PutResourcePolicyInput{SecretId: aws.String(arn), ResourcePolicy: aws.String(string(other))}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "drift put back in moto", func() error {
		p, err := inMoto()
		if err != nil {
			return err
		}
		if !strings.Contains(p, auditor) {
			return fmt.Errorf("%s", p)
		}
		return nil
	})
	if p := patchPaths(t, e.calls("UpdateResource", typ)[before:]); strings.Join(p, ",") != "replace /ResourcePolicy" {
		t.Errorf("drift patches: %v, want one replace of /ResourcePolicy", p)
	}

	// The secret is create-only.
	err = e.update(t, u, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, "another-secret", "spec", "secretId")
	})
	if err == nil || !strings.Contains(err.Error(), "create-only") {
		t.Errorf("a new secretId was not refused as create-only: %v", err)
	}

	if err := e.c.Delete(e.ctx, u); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, u)
	if p, err := inMoto(); err != nil || p != "" {
		t.Errorf("after delete: policy %q, err %v; want the secret without a policy", p, err)
	}
}
