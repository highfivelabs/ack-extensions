// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package file_system_policy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackmetrics "github.com/aws-controllers-k8s/runtime/pkg/metrics"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/efs"
	"github.com/aws/smithy-go"

	"github.com/highfivelabs/ack-extensions/pkg/fakeaws"
	svcapitypes "github.com/highfivelabs/ack-extensions/services/efs/apis/v1alpha1"
)

const (
	tmpl = `{"Statement":[{"Effect":"Deny","Principal":{"AWS":"*"},"Action":"*","Resource":"${fileSystemARN}","Condition":{"Bool":{"aws:SecureTransport":"false"}}}],"Version":"2012-10-17"}`
	arn  = "arn:aws:elasticfilesystem:us-east-2:111122223333:file-system/fs-0123456789abcdef0"
)

func manager() *resourceManager {
	return &resourceManager{
		awsPartition: ackv1alpha1.AWSPartition("aws"),
		awsRegion:    ackv1alpha1.AWSRegion("us-east-2"),
		awsAccountID: ackv1alpha1.AWSAccountID("111122223333"),
	}
}

func fsp(id, policy string) *svcapitypes.FileSystemPolicy {
	return &svcapitypes.FileSystemPolicy{Spec: svcapitypes.FileSystemPolicySpec{
		FileSystemID: aws.String(id), Policy: aws.String(policy),
	}}
}

func TestFileSystemARN(t *testing.T) {
	if got := manager().fileSystemARN("fs-0123456789abcdef0"); got != arn {
		t.Fatalf("fileSystemARN = %s, want %s", got, arn)
	}
}

func TestRenderRequestPolicy(t *testing.T) {
	in := &svcsdk.PutFileSystemPolicyInput{}
	renderRequestPolicy(manager(), &resource{fsp("fs-0123456789abcdef0", tmpl)}, in)
	want := `{"Statement":[{"Effect":"Deny","Principal":{"AWS":"*"},"Action":"*","Resource":"` + arn + `","Condition":{"Bool":{"aws:SecureTransport":"false"}}}],"Version":"2012-10-17"}`
	if aws.ToString(in.Policy) != want {
		t.Fatalf("Policy = %s, want %s", aws.ToString(in.Policy), want)
	}
	// No file system ID yet: nothing is written into the request.
	in = &svcsdk.PutFileSystemPolicyInput{}
	d := fsp("", tmpl)
	d.Spec.FileSystemID = nil
	renderRequestPolicy(manager(), &resource{d}, in)
	if in.Policy != nil {
		t.Fatalf("Policy = %s, want none", aws.ToString(in.Policy))
	}
}

func TestKeepPolicyTemplate(t *testing.T) {
	rendered := renderPolicy(tmpl, arn)
	// The file system holds the template's rendering (key order differs, as
	// EFS may reorder): the template stays in the spec.
	read := fsp("fs-0123456789abcdef0", `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":{"AWS":"*"},"Action":"*","Resource":"`+arn+`","Condition":{"Bool":{"aws:SecureTransport":"false"}}}]}`)
	keepPolicyTemplate(manager(), &resource{fsp("fs-0123456789abcdef0", tmpl)}, read)
	if aws.ToString(read.Spec.Policy) != tmpl {
		t.Fatalf("policy = %s, want the template", aws.ToString(read.Spec.Policy))
	}
	// The file system holds something else (a change, or a drift): the read
	// document stays, so the difference shows.
	other := fsp("fs-0123456789abcdef0", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"*","Resource":"`+arn+`"}]}`)
	keepPolicyTemplate(manager(), &resource{fsp("fs-0123456789abcdef0", tmpl)}, other)
	if aws.ToString(other.Spec.Policy) == tmpl {
		t.Fatal("template kept over a different document")
	}
	// A document with no placeholder is never replaced.
	plain := fsp("fs-0123456789abcdef0", rendered)
	keepPolicyTemplate(manager(), &resource{fsp("fs-0123456789abcdef0", rendered)}, plain)
	if aws.ToString(plain.Spec.Policy) != rendered {
		t.Fatal("document changed")
	}
}

type apiErr string

func (e apiErr) Error() string                 { return string(e) }
func (e apiErr) ErrorCode() string             { return string(e) }
func (e apiErr) ErrorMessage() string          { return string(e) }
func (e apiErr) ErrorFault() smithy.ErrorFault { return smithy.FaultUnknown }

func TestIsPolicyNotFound(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("x"), false},
		{apiErr("PolicyNotFound"), true},
		{apiErr("FileSystemNotFound"), true},
		{apiErr("AccessDenied"), false},
	} {
		if got := isPolicyNotFound(tc.err); got != tc.want {
			t.Errorf("isPolicyNotFound(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

// TestSdkDeleteFileSystemPolicy covers the delete moto cannot (it has no
// DeleteFileSystemPolicy): the call is made for the file system's ID, and a
// policy or file system already gone is a success.
func TestSdkDeleteFileSystemPolicy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		code    string
		wantErr bool
	}{
		{name: "deleted", status: 200},
		{name: "no policy", status: 404, code: "PolicyNotFound"},
		{name: "no file system", status: 404, code: "FileSystemNotFound"},
		{name: "refused", status: 403, code: "AccessDeniedException", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var path, method string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path, method = r.URL.Path, r.Method
				w.Header().Set("Content-Type", "application/json")
				if tc.code != "" {
					w.Header().Set("x-amzn-ErrorType", tc.code)
				}
				w.WriteHeader(tc.status)
				if tc.code != "" {
					_, _ = w.Write([]byte(`{"ErrorCode":"` + tc.code + `","Message":"x"}`))
				}
			}))
			defer srv.Close()
			cfg, err := fakeaws.Config(srv.URL, "us-east-2")
			if err != nil {
				t.Fatal(err)
			}
			rm := &resourceManager{sdkapi: svcsdk.NewFromConfig(cfg), metrics: ackmetrics.NewMetrics("efs")}
			_, err = rm.sdkDelete(context.Background(), &resource{fsp("fs-0123456789abcdef0", tmpl)})
			if (err != nil) != tc.wantErr {
				t.Fatalf("sdkDelete error = %v, want error %v", err, tc.wantErr)
			}
			if method != http.MethodDelete || !strings.HasSuffix(path, "/file-systems/fs-0123456789abcdef0/policy") {
				t.Fatalf("request = %s %s", method, path)
			}
		})
	}
}
