// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	ebv1 "github.com/highfivelabs/ack-extensions/services/eventbridge/apis/v1alpha1"
)

// Connection: an EventBridge connection, the credentials an API
// destination calls an HTTP endpoint with. ACK's eventbridge-controller
// (v1.9.2) ignores it. The password comes from a Kubernetes Secret.
//
// EventBridge stores a connection's credentials in a Secrets Manager
// secret it manages; moto does the same, so the test reads that secret to
// see that the password in the Kubernetes Secret reached AWS.
func TestConnection(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = ebv1.AddToScheme(scheme)
	env := harness.Start(t, "eventbridge", scheme)
	cloud := eventbridge.NewFromConfig(env.AWS)
	sm := secretsmanager.NewFromConfig(env.AWS)
	ctx := context.Background()

	if err := env.Client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "partner-auth", Namespace: "default"},
		StringData: map[string]string{"password": "not-a-real-password"},
	}); err != nil {
		t.Fatal(err)
	}

	waitSynced := func(t *testing.T, obj *ebv1.Connection) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.Name+" synced", func() error {
			got := &ebv1.Connection{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
				return err
			}
			if ok, why := synced(got.Status.Conditions); !ok {
				return fmt.Errorf("not synced: %s", why)
			}
			return nil
		})
	}
	gone := func(t *testing.T, obj client.Object) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.GetName()+" gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &ebv1.Connection{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}
	connection := func(name, team string) *ebv1.Connection {
		return &ebv1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: ebv1.ConnectionSpec{
				Name: aws.String(name), AuthorizationType: aws.String("BASIC"),
				AuthParameters: &ebv1.CreateConnectionAuthRequestParameters{
					BasicAuthParameters: &ebv1.CreateConnectionBasicAuthRequestParameters{
						Username: aws.String("app"),
						Password: &ackv1alpha1.SecretKeyReference{SecretReference: corev1.SecretReference{Name: "partner-auth"}, Key: "password"},
					},
					InvocationHTTPParameters: &ebv1.ConnectionHTTPParameters{HeaderParameters: []*ebv1.ConnectionHeaderParameter{
						{Key: aws.String("x-team"), Value: aws.String(team), IsValueSecret: aws.Bool(false)},
					}},
				},
			},
		}
	}
	partner := connection("partner", "payments")

	t.Run("a connection is made with the Secret's password", func(t *testing.T) {
		if err := env.Client.Create(ctx, partner); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, partner)
		out, err := cloud.DescribeConnection(ctx, &eventbridge.DescribeConnectionInput{Name: aws.String("partner")})
		if err != nil {
			t.Fatal(err)
		}
		if out.ConnectionState != ebtypes.ConnectionStateAuthorized || out.AuthorizationType != ebtypes.ConnectionAuthorizationTypeBasic {
			t.Fatalf("cloud connection state %s type %s", out.ConnectionState, out.AuthorizationType)
		}
		secret, err := sm.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: out.SecretArn})
		if err != nil {
			t.Fatal(err)
		}
		var stored map[string]any
		if err := json.Unmarshal([]byte(aws.ToString(secret.SecretString)), &stored); err != nil {
			t.Fatal(err)
		}
		if b, _ := json.Marshal(stored); !strings.Contains(string(b), "not-a-real-password") {
			t.Fatalf("the Kubernetes Secret's password is not in the connection's secret: %s", b)
		}
	})

	t.Run("it settles: the secrets read back as no drift", func(t *testing.T) {
		updates := func() int {
			n := 0
			for _, line := range strings.Split(env.Logs(), "\n") {
				if strings.Contains(line, ">>> rm.sdkUpdate") && strings.Contains(line, `"name": "partner"`) {
					n++
				}
			}
			return n
		}
		before := updates()
		time.Sleep(15 * time.Second)
		if n := updates(); n != before {
			t.Fatalf("updated %d times across resyncs: the secrets read as drift", n-before)
		}
	})

	t.Run("a changed parameter is updated", func(t *testing.T) {
		// The controller writes the object too: read and update again until
		// the update is not a conflict.
		harness.Eventually(t, 30*time.Second, "spec updated", func() error {
			obj := &ebv1.Connection{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(partner), obj); err != nil {
				return err
			}
			obj.Spec.AuthParameters.InvocationHTTPParameters.HeaderParameters[0].Value = aws.String("platform")
			return env.Client.Update(ctx, obj)
		})
		harness.Eventually(t, 60*time.Second, "new header in the cloud", func() error {
			out, err := cloud.DescribeConnection(ctx, &eventbridge.DescribeConnectionInput{Name: aws.String("partner")})
			if err != nil {
				return err
			}
			p := out.AuthParameters
			if p == nil || p.InvocationHttpParameters == nil || len(p.InvocationHttpParameters.HeaderParameters) != 1 ||
				aws.ToString(p.InvocationHttpParameters.HeaderParameters[0].Value) != "platform" {
				return fmt.Errorf("auth parameters %+v", p)
			}
			return nil
		})
		waitSynced(t, partner)
	})

	t.Run("the name cannot change", func(t *testing.T) {
		obj := &ebv1.Connection{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(partner), obj); err != nil {
			t.Fatal(err)
		}
		obj.Spec.Name = aws.String("renamed")
		err := env.Client.Update(ctx, obj)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("changing name: got %v, want the CRD's immutability refusal", err)
		}
	})

	t.Run("deletion-policy retain leaves the connection", func(t *testing.T) {
		keep := connection("kept", "ops")
		keep.Annotations = map[string]string{ackv1alpha1.AnnotationDeletionPolicy: string(ackv1alpha1.DeletionPolicyRetain)}
		if err := env.Client.Create(ctx, keep); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, keep)
		if err := env.Client.Delete(ctx, keep); err != nil {
			t.Fatal(err)
		}
		gone(t, keep)
		if _, err := cloud.DescribeConnection(ctx, &eventbridge.DescribeConnectionInput{Name: aws.String("kept")}); err != nil {
			t.Fatalf("retained connection: %v", err)
		}
	})

	t.Run("delete deletes the connection", func(t *testing.T) {
		if err := env.Client.Delete(ctx, partner); err != nil {
			t.Fatal(err)
		}
		gone(t, partner)
		_, err := cloud.DescribeConnection(ctx, &eventbridge.DescribeConnectionInput{Name: aws.String("partner")})
		var nf *ebtypes.ResourceNotFoundException
		if !errors.As(err, &nf) {
			t.Fatalf("connection still there, or another error: %v", err)
		}
	})
}

// ApiDestination: an EventBridge API destination, the HTTP endpoint a rule
// sends events to through a Connection's credentials. ACK's
// eventbridge-controller (v1.9.2) ignores it. Its connectionRef names our
// own Connection.
func TestApiDestination(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = ebv1.AddToScheme(scheme)
	env := harness.Start(t, "eventbridge", scheme)
	cloud := eventbridge.NewFromConfig(env.AWS)
	ctx := context.Background()

	if err := env.Client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "partner-key", Namespace: "default"},
		StringData: map[string]string{"key": "not-a-real-key"},
	}); err != nil {
		t.Fatal(err)
	}
	conn := &ebv1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "partner", Namespace: "default"},
		Spec: ebv1.ConnectionSpec{
			Name: aws.String("partner"), AuthorizationType: aws.String("API_KEY"),
			AuthParameters: &ebv1.CreateConnectionAuthRequestParameters{
				APIKeyAuthParameters: &ebv1.CreateConnectionAPIKeyAuthRequestParameters{
					APIKeyName:  aws.String("x-api-key"),
					APIKeyValue: &ackv1alpha1.SecretKeyReference{SecretReference: corev1.SecretReference{Name: "partner-key"}, Key: "key"},
				},
			},
		},
	}
	if err := env.Client.Create(ctx, conn); err != nil {
		t.Fatal(err)
	}

	synced := func(name string, obj client.Object, conds func() []*ackv1alpha1.Condition) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, name+" synced", func() error {
			if err := env.Client.Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, obj); err != nil {
				return err
			}
			for _, c := range conds() {
				if c.Type == ackv1alpha1.ConditionTypeResourceSynced && c.Status == corev1.ConditionTrue {
					return nil
				}
			}
			return fmt.Errorf("not synced: %v", conds())
		})
	}
	gotConn := &ebv1.Connection{}
	synced("partner", gotConn, func() []*ackv1alpha1.Condition { return gotConn.Status.Conditions })
	connARN := string(*gotConn.Status.ACKResourceMetadata.ARN)

	dest := &ebv1.APIDestination{
		ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: "default"},
		Spec: ebv1.APIDestinationSpec{
			Name:               aws.String("orders"),
			ConnectionRef:      &ackv1alpha1.AWSResourceReferenceWrapper{From: &ackv1alpha1.AWSResourceReference{Name: aws.String("partner")}},
			InvocationEndpoint: aws.String("https://partner.example.com/orders"), HTTPMethod: aws.String("POST"),
			InvocationRateLimitPerSecond: aws.Int64(10),
		},
	}
	gotDest := &ebv1.APIDestination{}

	t.Run("an API destination is made through the referenced connection", func(t *testing.T) {
		if err := env.Client.Create(ctx, dest); err != nil {
			t.Fatal(err)
		}
		synced("orders", gotDest, func() []*ackv1alpha1.Condition { return gotDest.Status.Conditions })
		out, err := cloud.DescribeApiDestination(ctx, &eventbridge.DescribeApiDestinationInput{Name: aws.String("orders")})
		if err != nil {
			t.Fatal(err)
		}
		if aws.ToString(out.ConnectionArn) != connARN || aws.ToString(out.InvocationEndpoint) != "https://partner.example.com/orders" ||
			out.HttpMethod != ebtypes.ApiDestinationHttpMethodPost || out.ApiDestinationState != ebtypes.ApiDestinationStateActive {
			t.Fatalf("cloud destination connection %q endpoint %q method %s state %s", aws.ToString(out.ConnectionArn),
				aws.ToString(out.InvocationEndpoint), out.HttpMethod, out.ApiDestinationState)
		}
	})

	t.Run("a changed rate limit is updated", func(t *testing.T) {
		obj := &ebv1.APIDestination{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(dest), obj); err != nil {
			t.Fatal(err)
		}
		obj.Spec.InvocationRateLimitPerSecond = aws.Int64(25)
		if err := env.Client.Update(ctx, obj); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "rate limit 25 in the cloud", func() error {
			out, err := cloud.DescribeApiDestination(ctx, &eventbridge.DescribeApiDestinationInput{Name: aws.String("orders")})
			if err != nil {
				return err
			}
			if aws.ToInt32(out.InvocationRateLimitPerSecond) != 25 {
				return fmt.Errorf("rate limit %d", aws.ToInt32(out.InvocationRateLimitPerSecond))
			}
			return nil
		})
	})

	t.Run("the name cannot change", func(t *testing.T) {
		obj := &ebv1.APIDestination{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(dest), obj); err != nil {
			t.Fatal(err)
		}
		obj.Spec.Name = aws.String("renamed")
		err := env.Client.Update(ctx, obj)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("changing name: got %v, want the CRD's immutability refusal", err)
		}
	})

	t.Run("deletion-policy retain leaves the destination", func(t *testing.T) {
		keep := &ebv1.APIDestination{
			ObjectMeta: metav1.ObjectMeta{
				Name: "audit", Namespace: "default",
				Annotations: map[string]string{ackv1alpha1.AnnotationDeletionPolicy: string(ackv1alpha1.DeletionPolicyRetain)},
			},
			Spec: ebv1.APIDestinationSpec{
				Name: aws.String("audit"), ConnectionARN: aws.String(connARN),
				InvocationEndpoint: aws.String("https://partner.example.com/audit"), HTTPMethod: aws.String("PUT"),
			},
		}
		if err := env.Client.Create(ctx, keep); err != nil {
			t.Fatal(err)
		}
		got := &ebv1.APIDestination{}
		synced("audit", got, func() []*ackv1alpha1.Condition { return got.Status.Conditions })
		if err := env.Client.Delete(ctx, keep); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "audit gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(keep), &ebv1.APIDestination{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
		if _, err := cloud.DescribeApiDestination(ctx, &eventbridge.DescribeApiDestinationInput{Name: aws.String("audit")}); err != nil {
			t.Fatalf("retained destination: %v", err)
		}
	})

	t.Run("delete deletes the destination", func(t *testing.T) {
		if err := env.Client.Delete(ctx, dest); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "orders gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(dest), &ebv1.APIDestination{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
		_, err := cloud.DescribeApiDestination(ctx, &eventbridge.DescribeApiDestinationInput{Name: aws.String("orders")})
		var nf *ebtypes.ResourceNotFoundException
		if !errors.As(err, &nf) {
			t.Fatalf("destination still there, or another error: %v", err)
		}
	})
}
