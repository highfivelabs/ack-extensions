// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	acklambda "github.com/aws-controllers-k8s/lambda-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	lambdav1 "github.com/highfivelabs/ack-extensions/services/lambda/apis/v1alpha1"
)

// cloudStatements is the function's policy in the fake cloud, statement
// ID to statement; none when Lambda reports no policy.
func cloudStatements(ctx context.Context, c *lambda.Client, fn, qualifier string) (map[string]map[string]any, error) {
	in := &lambda.GetPolicyInput{FunctionName: aws.String(fn)}
	if qualifier != "" {
		in.Qualifier = aws.String(qualifier)
	}
	out, err := c.GetPolicy(ctx, in)
	var nf *lambdatypes.ResourceNotFoundException
	if errors.As(err, &nf) {
		return map[string]map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Statement []map[string]any `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(aws.ToString(out.Policy)), &doc); err != nil {
		return nil, err
	}
	m := map[string]map[string]any{}
	for _, st := range doc.Statement {
		sid, _ := st["Sid"].(string)
		m[sid] = st
	}
	return m, nil
}

func lambdaFixture(t *testing.T, ctx context.Context, env *harness.Env, cloud *lambda.Client) {
	t.Helper()
	role, err := iam.NewFromConfig(env.AWS).CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String("app-fn"),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var code bytes.Buffer
	zw := zip.NewWriter(&code)
	w, _ := zw.Create("index.py")
	_, _ = w.Write([]byte("def handler(event, context):\n    return 1\n"))
	_ = zw.Close()
	if _, err := cloud.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String("app"), Runtime: lambdatypes.RuntimePython312, Role: role.Role.Arn,
		Handler: aws.String("index.handler"), Code: &lambdatypes.FunctionCode{ZipFile: code.Bytes()},
	}); err != nil {
		t.Fatal(err)
	}
	v, err := cloud.PublishVersion(ctx, &lambda.PublishVersionInput{FunctionName: aws.String("app")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cloud.CreateAlias(ctx, &lambda.CreateAliasInput{FunctionName: aws.String("app"), Name: aws.String("live"), FunctionVersion: v.Version}); err != nil {
		t.Fatal(err)
	}
}

// Permission: one statement of a function's resource-based policy, so
// that S3, an ALB, EventBridge or SNS may invoke the function.
//
// moto keeps one policy for a function and all its versions and aliases
// (AWS keeps one each), so the qualified step shows that the qualifier is
// sent and the statement read back with it, not that the policies are
// separate.
func TestPermission(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = lambdav1.AddToScheme(scheme)
	_ = acklambda.AddToScheme(scheme)
	env := harness.Start(t, "lambda", scheme)
	cloud := lambda.NewFromConfig(env.AWS)
	ctx := context.Background()
	lambdaFixture(t, ctx, env, cloud)

	waitSynced := func(t *testing.T, obj *lambdav1.Permission) *lambdav1.Permission {
		t.Helper()
		got := &lambdav1.Permission{}
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
	waitStatement := func(t *testing.T, sid string, present bool) map[string]any {
		t.Helper()
		var st map[string]any
		harness.Eventually(t, 60*time.Second, fmt.Sprintf("statement %s present=%v", sid, present), func() error {
			m, err := cloudStatements(ctx, cloud, "app", "")
			if err != nil {
				return err
			}
			var ok bool
			st, ok = m[sid]
			if ok != present {
				return fmt.Errorf("statements %v", keys(m))
			}
			return nil
		})
		return st
	}
	gone := func(t *testing.T, obj client.Object) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, obj.GetName()+" gone", func() error {
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &lambdav1.Permission{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}

	s3 := &lambdav1.Permission{
		ObjectMeta: metav1.ObjectMeta{Name: "s3-invoke", Namespace: "default"},
		Spec: lambdav1.PermissionSpec{
			FunctionName: aws.String("app"), StatementID: aws.String("s3-invoke"),
			Action: aws.String("lambda:InvokeFunction"), Principal: aws.String("s3.amazonaws.com"),
			SourceARN: aws.String("arn:aws:s3:::uploads"), SourceAccount: aws.String("123456789012"),
		},
	}

	t.Run("a service may invoke the function", func(t *testing.T) {
		if err := env.Client.Create(ctx, s3); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t, s3)
		if !strings.Contains(aws.ToString(got.Status.Statement), `"s3-invoke"`) {
			t.Fatalf("status.statement = %s", aws.ToString(got.Status.Statement))
		}
		st := waitStatement(t, "s3-invoke", true)
		b, _ := json.Marshal(st)
		for _, want := range []string{`"Service":"s3.amazonaws.com"`, `"AWS:SourceArn":"arn:aws:s3:::uploads"`, `"AWS:SourceAccount":"123456789012"`, `"lambda:InvokeFunction"`} {
			if !strings.Contains(string(b), want) {
				t.Errorf("cloud statement %s lacks %s", b, want)
			}
		}
	})

	t.Run("an account may invoke an alias", func(t *testing.T) {
		alias := &lambdav1.Permission{
			ObjectMeta: metav1.ObjectMeta{Name: "partner", Namespace: "default"},
			Spec: lambdav1.PermissionSpec{
				FunctionName: aws.String("app"), Qualifier: aws.String("live"), StatementID: aws.String("partner"),
				Action: aws.String("lambda:InvokeFunction"), Principal: aws.String("210987654321"),
			},
		}
		if err := env.Client.Create(ctx, alias); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t, alias)
		// The spec keeps the account ID as written (AWS stores its root ARN).
		if aws.ToString(got.Spec.Principal) != "210987654321" {
			t.Fatalf("spec.principal rewritten to %q", aws.ToString(got.Spec.Principal))
		}
		m, err := cloudStatements(ctx, cloud, "app", "live")
		if err != nil {
			t.Fatal(err)
		}
		res, _ := m["partner"]["Resource"].(string)
		if !strings.HasSuffix(res, ":function:app:live") {
			t.Fatalf("alias statement resource %q, statements %v", res, keys(m))
		}
	})

	t.Run("the CRD refuses what Lambda would", func(t *testing.T) {
		for _, c := range []struct {
			name string
			spec lambdav1.PermissionSpec
			want string
		}{
			{"no function", lambdav1.PermissionSpec{StatementID: aws.String("x"), Action: aws.String("lambda:InvokeFunction"), Principal: aws.String("sns.amazonaws.com")},
				"exactly one of functionName and functionRef"},
			{"a statement ID Lambda refuses", lambdav1.PermissionSpec{FunctionName: aws.String("app"), StatementID: aws.String("has space"), Action: aws.String("lambda:InvokeFunction"), Principal: aws.String("sns.amazonaws.com")},
				"statementID"},
			{"an auth type Lambda does not have", lambdav1.PermissionSpec{FunctionName: aws.String("app"), StatementID: aws.String("x"), Action: aws.String("lambda:InvokeFunctionUrl"), Principal: aws.String("*"), FunctionURLAuthType: aws.String("OPEN")},
				"functionURLAuthType"},
		} {
			obj := &lambdav1.Permission{ObjectMeta: metav1.ObjectMeta{Name: "refused", Namespace: "default"}, Spec: c.spec}
			err := env.Client.Create(ctx, obj)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s: got %v, want the CRD's refusal naming %q", c.name, err, c.want)
			}
		}
	})

	t.Run("a statement cannot change", func(t *testing.T) {
		obj := &lambdav1.Permission{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(s3), obj); err != nil {
			t.Fatal(err)
		}
		obj.Spec.Principal = aws.String("sns.amazonaws.com")
		err := env.Client.Update(ctx, obj)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("changing principal: got %v, want the CRD's immutability refusal", err)
		}
	})

	t.Run("a statement removed outside Kubernetes is added back", func(t *testing.T) {
		if _, err := cloud.RemovePermission(ctx, &lambda.RemovePermissionInput{FunctionName: aws.String("app"), StatementId: aws.String("s3-invoke")}); err != nil {
			t.Fatal(err)
		}
		waitStatement(t, "s3-invoke", true)
	})

	t.Run("references resolve to ACK's Function", func(t *testing.T) {
		// A Function as ACK's lambda-controller leaves it once synced; the
		// controller does not run here.
		fn := &acklambda.Function{
			ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
			Spec:       acklambda.FunctionSpec{Name: aws.String("app"), Code: &acklambda.FunctionCode{S3Bucket: aws.String("code"), S3Key: aws.String("app.zip")}},
		}
		if err := env.Client.Create(ctx, fn); err != nil {
			t.Fatal(err)
		}
		fn.Status.Conditions = []*ackv1alpha1.Condition{{Type: ackv1alpha1.ConditionTypeResourceSynced, Status: corev1.ConditionTrue}}
		if err := env.Client.Status().Update(ctx, fn); err != nil {
			t.Fatal(err)
		}
		byRef := &lambdav1.Permission{
			ObjectMeta: metav1.ObjectMeta{Name: "events", Namespace: "default"},
			Spec: lambdav1.PermissionSpec{
				FunctionRef: &ackv1alpha1.AWSResourceReferenceWrapper{From: &ackv1alpha1.AWSResourceReference{Name: aws.String("app")}},
				StatementID: aws.String("events"), Action: aws.String("lambda:InvokeFunction"), Principal: aws.String("events.amazonaws.com"),
				SourceARN: aws.String("arn:aws:events:us-west-2:123456789012:rule/nightly"),
			},
		}
		if err := env.Client.Create(ctx, byRef); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, byRef)
		waitStatement(t, "events", true)
	})

	t.Run("deletion-policy retain leaves the statement", func(t *testing.T) {
		keep := &lambdav1.Permission{
			ObjectMeta: metav1.ObjectMeta{
				Name: "kept", Namespace: "default",
				Annotations: map[string]string{ackv1alpha1.AnnotationDeletionPolicy: string(ackv1alpha1.DeletionPolicyRetain)},
			},
			Spec: lambdav1.PermissionSpec{FunctionName: aws.String("app"), StatementID: aws.String("kept"),
				Action: aws.String("lambda:InvokeFunction"), Principal: aws.String("sns.amazonaws.com")},
		}
		if err := env.Client.Create(ctx, keep); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, keep)
		if err := env.Client.Delete(ctx, keep); err != nil {
			t.Fatal(err)
		}
		gone(t, keep)
		waitStatement(t, "kept", true)
	})

	t.Run("delete removes the statement", func(t *testing.T) {
		if err := env.Client.Delete(ctx, s3); err != nil {
			t.Fatal(err)
		}
		gone(t, s3)
		waitStatement(t, "s3-invoke", false)
	})
}

func keys(m map[string]map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
