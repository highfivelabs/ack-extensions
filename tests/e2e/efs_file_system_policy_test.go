// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	efsv1 "github.com/highfivelabs/ack-extensions/services/efs/apis/v1alpha1"
)

// TestEFSFileSystemPolicy tests efs.aws.hfl.dev FileSystemPolicy: a policy on
// a file system named by fileSystemRef, whose document names the file
// system's own ARN as ${fileSystemARN}. The document EFS holds has the ARN
// filled in; the object keeps the template (no drift); a changed template is
// put again. Delete is not run here: moto has no DeleteFileSystemPolicy
// (500), so TestSdkDeleteFileSystemPolicy in the package covers it
// (docs/plan.md section 6.48).
func TestEFSFileSystemPolicy(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = efsv1.AddToScheme(scheme)
	env := harness.Start(t, "efs", scheme)
	efsCloud := efs.NewFromConfig(env.AWS)
	ctx := context.Background()

	// The file system is made directly: moto answers 500 to the
	// DeleteFileSystemPolicy that the FileSystem kind sends when it has no
	// policy (docs/plan.md section 6.48), so a FileSystem object cannot be
	// the target here. fileSystemRef's resolution is ACK's generated code;
	// the live run covers it.
	fsOut, err := efsCloud.CreateFileSystem(ctx, &efs.CreateFileSystemInput{CreationToken: aws.String("ackx-efs-policy-001")})
	if err != nil {
		t.Fatal(err)
	}
	fsID := aws.ToString(fsOut.FileSystemId)
	cleanupLog(t, "efs "+fsID, func() error {
		_, err := efsCloud.DeleteFileSystem(ctx, &efs.DeleteFileSystemInput{FileSystemId: aws.String(fsID)})
		return err
	})

	template := func(action string) string {
		return `{"Version":"2012-10-17","Statement":[{"Sid":"Mount","Effect":"Allow","Principal":{"AWS":"*"},"Action":"` + action + `","Resource":"${fileSystemARN}"}]}`
	}
	pol := &efsv1.FileSystemPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "pol", Namespace: "default"},
		Spec: efsv1.FileSystemPolicySpec{
			FileSystemID:                   aws.String(fsID),
			BypassPolicyLockoutSafetyCheck: aws.Bool(true),
			Policy:                         aws.String(template("elasticfilesystem:ClientMount")),
		},
	}
	if err := env.Client.Create(ctx, pol); err != nil {
		t.Fatal(err)
	}

	held := func() (string, error) {
		out, err := efsCloud.DescribeFileSystemPolicy(ctx, &efs.DescribeFileSystemPolicyInput{FileSystemId: aws.String(fsID)})
		if err != nil {
			return "", err
		}
		return aws.ToString(out.Policy), nil
	}
	wantARN := "file-system/" + fsID

	harness.Eventually(t, 60*time.Second, "policy put with the ARN filled in", func() error {
		got, err := held()
		if err != nil {
			return err
		}
		if strings.Contains(got, "${") || !strings.Contains(got, wantARN) || !strings.Contains(got, "ClientMount") {
			return fmt.Errorf("EFS holds %s", got)
		}
		return nil
	})
	harness.Eventually(t, 60*time.Second, "policy synced and the template kept", func() error {
		var cur efsv1.FileSystemPolicy
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(pol), &cur); err != nil {
			return err
		}
		if ok, why := synced(cur.Status.Conditions); !ok {
			return fmt.Errorf("not synced: %s", why)
		}
		if aws.ToString(cur.Spec.Policy) != template("elasticfilesystem:ClientMount") {
			return fmt.Errorf("spec.policy = %s: the template was not kept", aws.ToString(cur.Spec.Policy))
		}
		return nil
	})

	// A changed template is put again.
	var cur efsv1.FileSystemPolicy
	if err := env.Client.Get(ctx, client.ObjectKeyFromObject(pol), &cur); err != nil {
		t.Fatal(err)
	}
	cur.Spec.Policy = aws.String(template("elasticfilesystem:ClientWrite"))
	if err := env.Client.Update(ctx, &cur); err != nil {
		t.Fatal(err)
	}
	harness.Eventually(t, 60*time.Second, "changed template put", func() error {
		got, err := held()
		if err != nil {
			return err
		}
		if !strings.Contains(got, "ClientWrite") || !strings.Contains(got, wantARN) {
			return fmt.Errorf("EFS holds %s", got)
		}
		return nil
	})
}
