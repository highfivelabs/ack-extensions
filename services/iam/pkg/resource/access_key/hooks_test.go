// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package access_key

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

func key(name string, secretName *string) *svcapitypes.AccessKey {
	return &svcapitypes.AccessKey{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team", UID: "uid-1"},
		Spec:       svcapitypes.AccessKeySpec{SecretName: secretName},
	}
}

func TestSecretName(t *testing.T) {
	empty, set := "", "ci-aws"
	for _, c := range []struct {
		spec *string
		want string
	}{{nil, "ci"}, {&empty, "ci"}, {&set, "ci-aws"}} {
		if got := secretName(key("ci", c.spec)); got != c.want {
			t.Errorf("secretName(%v) = %q, want %q", c.spec, got, c.want)
		}
	}
}

func TestNewSecret(t *testing.T) {
	ko := key("ci", nil)
	s := newSecret(ko, "AKIAEXAMPLE", "not-a-real-secret")
	if s.Name != "ci" || s.Namespace != "team" || s.Type != corev1.SecretTypeOpaque {
		t.Fatalf("secret %s/%s type %s", s.Namespace, s.Name, s.Type)
	}
	if s.Immutable == nil || !*s.Immutable {
		t.Error("not immutable")
	}
	if len(s.Data) != 2 || string(s.Data[KeyIDField]) != "AKIAEXAMPLE" || string(s.Data[SecretKeyField]) != "not-a-real-secret" {
		t.Errorf("data %v", s.Data)
	}
	if len(s.OwnerReferences) != 1 {
		t.Fatalf("owners %+v", s.OwnerReferences)
	}
	o := s.OwnerReferences[0]
	if o.UID != "uid-1" || o.Kind != "AccessKey" || o.APIVersion != "iam.aws.hfl.dev/v1alpha1" || o.Controller == nil || !*o.Controller || o.BlockOwnerDeletion != nil {
		t.Errorf("owner %+v", o)
	}
	if !ownedBy(s, ko) {
		t.Error("not owned by its object")
	}
	other := key("ci", nil)
	other.UID = "uid-2"
	if ownedBy(s, other) {
		t.Error("owned by another object of the same name")
	}
}
