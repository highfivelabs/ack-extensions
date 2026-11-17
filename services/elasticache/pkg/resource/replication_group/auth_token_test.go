// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package replication_group

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/elasticache"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/elasticache/apis/v1alpha1"
)

type fakeSecretReconciler struct {
	acktypes.Reconciler
	secretValue string
}

func (f *fakeSecretReconciler) SecretValueFromReference(_ context.Context, _ *ackv1alpha1.SecretKeyReference) (string, error) {
	return f.secretValue, nil
}

func sha256Of(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestSyncAuthTokenForCompare(t *testing.T) {
	ref := &ackv1alpha1.SecretKeyReference{Key: "token"}
	ref.Name = "redis-auth"

	t.Run("same secret value aligns latest AuthToken", func(t *testing.T) {
		rm := &resourceManager{rr: &fakeSecretReconciler{secretValue: "secret-v1"}}
		desired := &resource{ko: &svcapitypes.ReplicationGroup{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{AnnotationLastAuthTokenSHA256: sha256Of("secret-v1")},
			},
			Spec: svcapitypes.ReplicationGroupSpec{AuthToken: ref},
		}}
		latest := &resource{ko: &svcapitypes.ReplicationGroup{}}
		if err := rm.syncAuthTokenForCompare(context.Background(), desired, latest); err != nil {
			t.Fatal(err)
		}
		if latest.ko.Spec.AuthToken == nil || latest.ko.Spec.AuthToken.Name != "redis-auth" {
			t.Fatalf("expected latest.Spec.AuthToken to match desired, got %+v", latest.ko.Spec.AuthToken)
		}
	})

	t.Run("changed secret value triggers delta", func(t *testing.T) {
		rm := &resourceManager{rr: &fakeSecretReconciler{secretValue: "secret-v2"}}
		desired := &resource{ko: &svcapitypes.ReplicationGroup{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{AnnotationLastAuthTokenSHA256: sha256Of("secret-v1")},
			},
			Spec: svcapitypes.ReplicationGroupSpec{AuthToken: ref},
		}}
		latest := &resource{ko: &svcapitypes.ReplicationGroup{
			Spec: svcapitypes.ReplicationGroupSpec{AuthToken: ref.DeepCopy()},
		}}
		if err := rm.syncAuthTokenForCompare(context.Background(), desired, latest); err != nil {
			t.Fatal(err)
		}
		if latest.ko.Spec.AuthToken != nil {
			t.Fatalf("expected latest.Spec.AuthToken to be nil on secret value change, got %+v", latest.ko.Spec.AuthToken)
		}
	})

	t.Run("removed AuthToken when enabled triggers delta", func(t *testing.T) {
		rm := &resourceManager{rr: &fakeSecretReconciler{}}
		desired := &resource{ko: &svcapitypes.ReplicationGroup{}}
		latest := &resource{ko: &svcapitypes.ReplicationGroup{
			Status: svcapitypes.ReplicationGroupStatus{AuthTokenEnabled: aws.Bool(true)},
		}}
		if err := rm.syncAuthTokenForCompare(context.Background(), desired, latest); err != nil {
			t.Fatal(err)
		}
		if latest.ko.Spec.AuthToken == nil {
			t.Fatal("expected latest.Spec.AuthToken to be non-nil when AuthTokenEnabled is true")
		}
	})
}

func TestApplyAuthTokenUpdatePayload(t *testing.T) {
	rm := &resourceManager{}
	ref := &ackv1alpha1.SecretKeyReference{Key: "token"}
	ref.Name = "redis-auth"

	t.Run("clears when Spec.AuthToken not different", func(t *testing.T) {
		in := &svcsdk.ModifyReplicationGroupInput{
			AuthToken:               aws.String("tok"),
			AuthTokenUpdateStrategy: svcsdktypes.AuthTokenUpdateStrategyTypeRotate,
		}
		d := ackcompare.NewDelta()
		rm.applyAuthTokenUpdatePayload(in, &resource{ko: &svcapitypes.ReplicationGroup{}}, &resource{ko: &svcapitypes.ReplicationGroup{}}, d)
		if in.AuthToken != nil || in.AuthTokenUpdateStrategy != "" {
			t.Fatalf("expected AuthToken and strategy cleared, got %v %q", in.AuthToken, in.AuthTokenUpdateStrategy)
		}
	})

	t.Run("defaults to ROTATE when already enabled", func(t *testing.T) {
		in := &svcsdk.ModifyReplicationGroupInput{AuthToken: aws.String("tok")}
		d := ackcompare.NewDelta()
		d.Add("Spec.AuthToken", ref, nil)
		desired := &resource{ko: &svcapitypes.ReplicationGroup{Spec: svcapitypes.ReplicationGroupSpec{AuthToken: ref}}}
		latest := &resource{ko: &svcapitypes.ReplicationGroup{Status: svcapitypes.ReplicationGroupStatus{AuthTokenEnabled: aws.Bool(true)}}}
		rm.applyAuthTokenUpdatePayload(in, desired, latest, d)
		if in.AuthTokenUpdateStrategy != svcsdktypes.AuthTokenUpdateStrategyTypeRotate {
			t.Fatalf("strategy = %q, want ROTATE", in.AuthTokenUpdateStrategy)
		}
	})

	t.Run("defaults to SET when not enabled", func(t *testing.T) {
		in := &svcsdk.ModifyReplicationGroupInput{AuthToken: aws.String("tok")}
		d := ackcompare.NewDelta()
		d.Add("Spec.AuthToken", ref, nil)
		desired := &resource{ko: &svcapitypes.ReplicationGroup{Spec: svcapitypes.ReplicationGroupSpec{AuthToken: ref}}}
		latest := &resource{ko: &svcapitypes.ReplicationGroup{}}
		rm.applyAuthTokenUpdatePayload(in, desired, latest, d)
		if in.AuthTokenUpdateStrategy != svcsdktypes.AuthTokenUpdateStrategyTypeSet {
			t.Fatalf("strategy = %q, want SET", in.AuthTokenUpdateStrategy)
		}
	})

	t.Run("defaults to DELETE when AuthToken removed", func(t *testing.T) {
		in := &svcsdk.ModifyReplicationGroupInput{AuthToken: aws.String("tok")}
		d := ackcompare.NewDelta()
		d.Add("Spec.AuthToken", nil, ref)
		desired := &resource{ko: &svcapitypes.ReplicationGroup{}}
		latest := &resource{ko: &svcapitypes.ReplicationGroup{Status: svcapitypes.ReplicationGroupStatus{AuthTokenEnabled: aws.Bool(true)}}}
		rm.applyAuthTokenUpdatePayload(in, desired, latest, d)
		if in.AuthToken != nil || in.AuthTokenUpdateStrategy != svcsdktypes.AuthTokenUpdateStrategyTypeDelete {
			t.Fatalf("got %v %q, want nil DELETE", in.AuthToken, in.AuthTokenUpdateStrategy)
		}
	})
}

func TestDeleteFinalSnapshotIdentifier(t *testing.T) {
	rm := &resourceManager{}
	in, err := rm.newDeleteRequestPayload(&resource{
		ko: &svcapitypes.ReplicationGroup{
			Spec: svcapitypes.ReplicationGroupSpec{
				ReplicationGroupID:      aws.String("rg-1"),
				FinalSnapshotIdentifier: aws.String("rg-1-final"),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if aws.ToString(in.FinalSnapshotIdentifier) != "rg-1-final" {
		t.Fatalf("FinalSnapshotIdentifier = %q, want rg-1-final", aws.ToString(in.FinalSnapshotIdentifier))
	}
}
