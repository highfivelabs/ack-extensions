// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package policy

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func TestSameContent(t *testing.T) {
	scp := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"s3:DeleteBucket","Resource":"*"}]}`
	cases := []struct {
		name string
		a, b *string
		want bool
	}{
		{"both unset", nil, nil, true},
		{"one unset", aws.String(scp), nil, false},
		{"identical", aws.String(scp), aws.String(scp), true},
		{"white space and key order", aws.String(scp), aws.String(`{
  "Statement": [{"Resource": "*", "Action": "s3:DeleteBucket", "Effect": "Deny"}],
  "Version": "2012-10-17"
}`), true},
		{"a different action", aws.String(scp), aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"s3:DeleteObject","Resource":"*"}]}`), false},
		{"statements in another order", aws.String(`{"Statement":[1,2]}`), aws.String(`{"Statement":[2,1]}`), false},
		{"not JSON, equal", aws.String("a: b"), aws.String("a: b"), true},
		{"not JSON, different", aws.String("a: b"), aws.String("a: c"), false},
	}
	for _, c := range cases {
		if got := sameContent(c.a, c.b); got != c.want {
			t.Errorf("%s: sameContent = %v, want %v", c.name, got, c.want)
		}
	}
}
