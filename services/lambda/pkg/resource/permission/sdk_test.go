// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package permission

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/lambda/apis/v1alpha1"
)

// Statements as Lambda writes them for AddPermission (Lambda developer
// guide, "Viewing resource-based IAM policies"): a service with a source
// ARN and account, an account (its root ARN), anyone in an organization,
// and a list-valued action.
const policy = `{"Version":"2012-10-17","Id":"default","Statement":[
 {"Sid":"s3-invoke","Effect":"Allow","Principal":{"Service":"s3.amazonaws.com"},"Action":"lambda:InvokeFunction","Resource":"arn:aws:lambda:us-west-2:123456789012:function:app",
  "Condition":{"StringEquals":{"AWS:SourceAccount":"123456789012"},"ArnLike":{"AWS:SourceArn":"arn:aws:s3:::uploads"}}},
 {"Sid":"other-account","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::210987654321:root"},"Action":["lambda:GetFunction"],"Resource":"arn:aws:lambda:us-west-2:123456789012:function:app"},
 {"Sid":"org","Effect":"Allow","Principal":"*","Action":"lambda:InvokeFunction","Resource":"arn:aws:lambda:us-west-2:123456789012:function:app",
  "Condition":{"StringEquals":{"aws:PrincipalOrgID":"o-abc123"}}},
 {"Sid":"url","Effect":"Allow","Principal":"*","Action":"lambda:InvokeFunctionUrl","Resource":"arn:aws:lambda:us-west-2:123456789012:function:app",
  "Condition":{"StringEquals":{"lambda:FunctionUrlAuthType":"NONE"}}}
]}`

func TestObservedSpec(t *testing.T) {
	for _, c := range []struct {
		sid                                                    string
		action, principal, sourceARN, sourceAccount, org, auth string
	}{
		{"s3-invoke", "lambda:InvokeFunction", "s3.amazonaws.com", "arn:aws:s3:::uploads", "123456789012", "", ""},
		{"other-account", "lambda:GetFunction", "arn:aws:iam::210987654321:root", "", "", "", ""},
		{"org", "lambda:InvokeFunction", "*", "", "", "o-abc123", ""},
		{"url", "lambda:InvokeFunctionUrl", "*", "", "", "", "NONE"},
	} {
		st, ok, err := findStatement(policy, c.sid)
		if err != nil || !ok {
			t.Fatalf("%s: found %v, err %v", c.sid, ok, err)
		}
		got := st.observedSpec()
		for _, f := range []struct {
			name string
			got  *string
			want string
		}{
			{"action", got.Action, c.action},
			{"principal", got.Principal, c.principal},
			{"sourceARN", got.SourceARN, c.sourceARN},
			{"sourceAccount", got.SourceAccount, c.sourceAccount},
			{"principalOrgID", got.PrincipalOrgID, c.org},
			{"functionURLAuthType", got.FunctionURLAuthType, c.auth},
		} {
			if aws.ToString(f.got) != f.want {
				t.Errorf("%s: %s = %q, want %q", c.sid, f.name, aws.ToString(f.got), f.want)
			}
		}
	}
}

func TestFindStatementMissing(t *testing.T) {
	if _, ok, err := findStatement(policy, "nope"); ok || err != nil {
		t.Fatalf("found %v, err %v", ok, err)
	}
	if _, _, err := findStatement("not json", "x"); err == nil {
		t.Fatal("no error for a policy that is not JSON")
	}
}

// A field the spec sets is kept as written (Lambda stores an account ID
// as its root ARN); an unset one is filled, as adoption needs.
func TestFillUnset(t *testing.T) {
	spec := svcapitypes.PermissionSpec{Principal: aws.String("210987654321")}
	st, _, _ := findStatement(policy, "other-account")
	fillUnset(&spec, st.observedSpec())
	if aws.ToString(spec.Principal) != "210987654321" {
		t.Errorf("principal rewritten to %q", aws.ToString(spec.Principal))
	}
	if aws.ToString(spec.Action) != "lambda:GetFunction" {
		t.Errorf("action not filled: %q", aws.ToString(spec.Action))
	}
	if spec.SourceARN != nil {
		t.Errorf("sourceARN filled with %q from nothing", aws.ToString(spec.SourceARN))
	}
}

func TestPermissionARN(t *testing.T) {
	rm := &resourceManager{awsPartition: "aws", awsRegion: "us-west-2", awsAccountID: "123456789012"}
	for _, c := range []struct{ fn, q, want string }{
		{"app", "", "arn:aws:lambda:us-west-2:123456789012:function:app/policy/s"},
		{"app", "live", "arn:aws:lambda:us-west-2:123456789012:function:app:live/policy/s"},
		{"arn:aws:lambda:us-west-2:123456789012:function:app:live", "live", "arn:aws:lambda:us-west-2:123456789012:function:app:live/policy/s"},
	} {
		ko := &svcapitypes.Permission{Spec: svcapitypes.PermissionSpec{FunctionName: aws.String(c.fn), StatementID: aws.String("s")}}
		if c.q != "" {
			ko.Spec.Qualifier = aws.String(c.q)
		}
		if got := rm.permissionARN(ko); got != c.want {
			t.Errorf("%s %s: %s, want %s", c.fn, c.q, got, c.want)
		}
	}
}
