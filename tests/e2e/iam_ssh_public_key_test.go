// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	iamv1 "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

// Real RSA public keys (their private halves were thrown away): moto does
// not parse the body, but IAM and the AWS Emulator do.
const (
	sshKeyA = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQCk7xyokQLNoSE57rj+wskTOOUNn2CEeu6EtjcFlAmPSVatbPp0N3XL0yCwue1LkPP7Sh9fw/k7XICR5Rbi1DgL3pBPKNLwPBh0yfVca/AA+RiGX4mCB+Mq1nqRdfixOQGc1GdNfhkVtBe2T+VRRngBPWyhavGij9B3dmBrWseXKE0Axb0V9VBWRyzRn597WCkbmIQ/Js/ZIDQqqhFnYBLk4TC9FfzeepNjejjZSmE1PjrTV4zG3Oi4Xytkfo2S6mCU6wr6ILKKwCpcNDc0QM57rbrMatxf4An4dt+yQG1Wmamv2AZ+qGLws4KAH4CffOCCLbTk7gFS7+Zo2udpFkv5 deploy@example"
	sshKeyB = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQCvFlIA/vWgqKYCnmaEcf0b21NLCXEJKMYOZnaA5yRb2c/c2InPf0G1tPARZmyI7VhqwOeRJbeibuNhwrEwwRcLQCRiKDbSSFF2SApMQplJKbav07sXnb4XGDy7wOvspvZezGvVnneIsUDTyjagpiBNAkXYOj+53TCpw54cV9iTvq00TWIbTXwkksA5PL9PHxK8TkTi+2/M47WkedfAZ6UynAeB3HNx4UZt/eLud3QVCoNz8j+YzVzUn9MHWTl6Hntaa6vNM7SpwDU9g9HkMWKkKa3mQwK2solj9FBypc/i27HYMeNMO8j73Nr6InDpcL12xs0PFd+1i8BX7QO3L9DT backup@example"
)

// SSHPublicKey: an IAM user's SSH public key for CodeCommit, Terraform's
// aws_iam_user_ssh_key. ACK's iam-controller (v1.11.3) has none.
func TestSSHPublicKey(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = iamv1.AddToScheme(scheme)
	env := harness.Start(t, "iam", scheme)
	cloud := iam.NewFromConfig(env.AWS)
	ctx := context.Background()

	if _, err := cloud.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String("erin")}); err != nil {
		t.Fatal(err)
	}

	waitSynced := func(t *testing.T, obj *iamv1.SSHPublicKey) *iamv1.SSHPublicKey {
		t.Helper()
		got := &iamv1.SSHPublicKey{}
		harness.Eventually(t, 60*time.Second, obj.Name+" synced", func() error {
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
				return err
			}
			if ok, why := synced(got.Status.Conditions); !ok {
				return fmt.Errorf("not synced: %s", why)
			}
			return nil
		})
		return got
	}
	gone := func(t *testing.T, obj client.Object) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.GetName()+" gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &iamv1.SSHPublicKey{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}
	cloudKey := func(id string) (*iamtypes.SSHPublicKey, error) {
		out, err := cloud.GetSSHPublicKey(ctx, &iam.GetSSHPublicKeyInput{UserName: aws.String("erin"), SSHPublicKeyId: aws.String(id), Encoding: iamtypes.EncodingTypeSsh})
		if err != nil {
			return nil, err
		}
		return out.SSHPublicKey, nil
	}
	cloudStatus := func(t *testing.T, id, want string) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, "key "+want, func() error {
			k, err := cloudKey(id)
			if err != nil {
				return err
			}
			if string(k.Status) != want {
				return fmt.Errorf("status %s", k.Status)
			}
			return nil
		})
	}

	deploy := &iamv1.SSHPublicKey{
		ObjectMeta: metav1.ObjectMeta{Name: "erin-deploy", Namespace: "default"},
		Spec:       iamv1.SSHPublicKeySpec{UserName: aws.String("erin"), SSHPublicKeyBody: aws.String(sshKeyA)},
	}
	var id string

	t.Run("a key is uploaded, active, and settles", func(t *testing.T) {
		if err := env.Client.Create(ctx, deploy); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t, deploy)
		id = aws.ToString(got.Status.SSHPublicKeyID)
		if id == "" || aws.ToString(got.Status.Fingerprint) == "" {
			t.Fatalf("status id %q fingerprint %q", id, aws.ToString(got.Status.Fingerprint))
		}
		k, err := cloudKey(id)
		if err != nil || k.Status != iamtypes.StatusTypeActive || aws.ToString(k.SSHPublicKeyBody) != sshKeyA {
			t.Fatalf("cloud key %+v: %v", k, err)
		}
		updates := func() int {
			n := 0
			for _, line := range strings.Split(env.Logs(), "\n") {
				if strings.Contains(line, ">>> rm.sdkUpdate") && strings.Contains(line, `"name": "erin-deploy"`) {
					n++
				}
			}
			return n
		}
		before := updates()
		time.Sleep(15 * time.Second)
		if n := updates(); n != before {
			t.Fatalf("updated %d times across resyncs", n-before)
		}
	})

	t.Run("it is deactivated", func(t *testing.T) {
		harness.Eventually(t, 30*time.Second, "spec updated", func() error {
			got := &iamv1.SSHPublicKey{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(deploy), got); err != nil {
				return err
			}
			got.Spec.Status = aws.String("Inactive")
			return env.Client.Update(ctx, got)
		})
		cloudStatus(t, id, "Inactive")
	})

	t.Run("a reactivation outside Kubernetes is put back", func(t *testing.T) {
		if _, err := cloud.UpdateSSHPublicKey(ctx, &iam.UpdateSSHPublicKeyInput{UserName: aws.String("erin"), SSHPublicKeyId: aws.String(id), Status: iamtypes.StatusTypeActive}); err != nil {
			t.Fatal(err)
		}
		cloudStatus(t, id, "Inactive")
	})

	t.Run("the body cannot change", func(t *testing.T) {
		got := &iamv1.SSHPublicKey{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(deploy), got); err != nil {
			t.Fatal(err)
		}
		got.Spec.SSHPublicKeyBody = aws.String(sshKeyB)
		err := env.Client.Update(ctx, got)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("changing sshPublicKeyBody: got %v, want the CRD's immutability refusal", err)
		}
	})

	t.Run("deletion-policy retain leaves the key", func(t *testing.T) {
		keep := &iamv1.SSHPublicKey{
			ObjectMeta: metav1.ObjectMeta{
				Name: "erin-backup", Namespace: "default",
				Annotations: map[string]string{ackv1alpha1.AnnotationDeletionPolicy: string(ackv1alpha1.DeletionPolicyRetain)},
			},
			Spec: iamv1.SSHPublicKeySpec{UserName: aws.String("erin"), SSHPublicKeyBody: aws.String(sshKeyB), Status: aws.String("Inactive")},
		}
		if err := env.Client.Create(ctx, keep); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t, keep)
		keepID := aws.ToString(got.Status.SSHPublicKeyID)
		cloudStatus(t, keepID, "Inactive")
		if err := env.Client.Delete(ctx, keep); err != nil {
			t.Fatal(err)
		}
		gone(t, keep)
		if _, err := cloudKey(keepID); err != nil {
			t.Fatalf("retained key: %v", err)
		}
	})

	t.Run("delete deletes the key", func(t *testing.T) {
		if err := env.Client.Delete(ctx, deploy); err != nil {
			t.Fatal(err)
		}
		gone(t, deploy)
		_, err := cloudKey(id)
		var nse *iamtypes.NoSuchEntityException
		if !errors.As(err, &nse) {
			t.Fatalf("key still there, or another error: %v", err)
		}
	})
}
