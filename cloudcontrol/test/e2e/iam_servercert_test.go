// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// F03, IAM: a server certificate with its private key from a Kubernetes Secret,
// made, updated (tags), refused a create-only change, and deleted.
func TestIAMServerCertificate(t *testing.T) {
	e := start(t)
	ic := iam.NewFromConfig(e.moto)
	cert, key := samlCert()
	if err := e.c.Create(e.ctx, secret("servercert-key", map[string]string{"private.pem": key})); err != nil {
		t.Fatal(err)
	}

	u := objOf("AWS::IAM::ServerCertificate", "app-tls", map[string]any{
		"serverCertificateName": "app-tls",
		"certificateBody":       cert,
		"privateKey":            map[string]any{"name": "servercert-key", "key": "private.pem"},
		"path":                  "/apps/",
		"tags":                  []any{map[string]any{"key": "env", "value": "dev"}},
	})
	if err := e.c.Create(e.ctx, u); err != nil {
		t.Fatal(err)
	}
	got := e.waitSynced(t, u)
	arn, _, _ := unstructured.NestedString(got.Object, "status", "arn")
	if !strings.Contains(arn, ":server-certificate/apps/app-tls") {
		t.Fatalf("status.arn %q, want :server-certificate/apps/app-tls", arn)
	}
	out, err := ic.GetServerCertificate(e.ctx, &iam.GetServerCertificateInput{ServerCertificateName: aws.String("app-tls")})
	if err != nil || aws.ToString(out.ServerCertificate.ServerCertificateMetadata.Path) != "/apps/" {
		t.Fatalf("moto's server certificate: %v %+v", err, out)
	}

	if err := e.update(t, u, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedSlice(o.Object, []any{map[string]any{"key": "env", "value": "prod"}}, "spec", "tags")
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "updated tags in fake", func() error {
		if len(e.calls("UpdateResource", "AWS::IAM::ServerCertificate")) == 0 {
			return fmt.Errorf("no UpdateResource call yet")
		}
		return nil
	})

	err = e.update(t, u, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, "other-tls", "spec", "serverCertificateName")
	})
	if err == nil || !strings.Contains(err.Error(), "create-only") {
		t.Errorf("a new serverCertificateName was not refused as create-only: %v", err)
	}

	if err := e.c.Delete(e.ctx, u); err != nil {
		t.Fatal(err)
	}
	e.waitGone(t, u)
	if _, err := ic.GetServerCertificate(e.ctx, &iam.GetServerCertificateInput{ServerCertificateName: aws.String("app-tls")}); err == nil {
		t.Errorf("server certificate still in moto after delete")
	}
}
