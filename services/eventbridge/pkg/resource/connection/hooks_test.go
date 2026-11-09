// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package connection

import (
	"testing"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	corev1 "k8s.io/api/core/v1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/eventbridge/apis/v1alpha1"
)

func basicAuth(user string, password *ackv1alpha1.SecretKeyReference, header *string) *svcapitypes.Connection {
	ko := &svcapitypes.Connection{}
	ko.Spec.Name = aws.String("partner")
	ko.Spec.AuthorizationType = aws.String("BASIC")
	ko.Spec.AuthParameters = &svcapitypes.CreateConnectionAuthRequestParameters{
		BasicAuthParameters: &svcapitypes.CreateConnectionBasicAuthRequestParameters{Username: aws.String(user), Password: password},
		InvocationHTTPParameters: &svcapitypes.ConnectionHTTPParameters{HeaderParameters: []*svcapitypes.ConnectionHeaderParameter{
			{Key: aws.String("x-team"), Value: aws.String("payments"), IsValueSecret: aws.Bool(false)},
			{Key: aws.String("x-token"), Value: header, IsValueSecret: aws.Bool(true)},
		}},
	}
	return ko
}

var password = &ackv1alpha1.SecretKeyReference{SecretReference: corev1.SecretReference{Name: "partner-auth"}, Key: "password"}

// What DescribeConnection returns has no secrets; with them kept, the
// spec as read is the spec as written.
func TestKeepSecretsIsNoDrift(t *testing.T) {
	desired := basicAuth("app", password, aws.String("s3cr3t"))
	latest := basicAuth("app", nil, nil)
	if d := newResourceDelta(&resource{desired}, &resource{latest}); len(d.Differences) == 0 {
		t.Fatal("the read without secrets is already equal: the test shows nothing")
	}
	keepSecrets(desired, latest)
	if d := newResourceDelta(&resource{desired}, &resource{latest}); len(d.Differences) != 0 {
		t.Fatalf("secrets kept, still different at %v", d.Differences)
	}
}

// A non-secret field read back is still compared.
func TestKeepSecretsKeepsOtherDrift(t *testing.T) {
	desired := basicAuth("app", password, aws.String("s3cr3t"))
	latest := basicAuth("someone-else", nil, nil)
	keepSecrets(desired, latest)
	if d := newResourceDelta(&resource{desired}, &resource{latest}); !d.DifferentAt("Spec.AuthParameters") {
		t.Fatal("a changed username is no drift")
	}
}
