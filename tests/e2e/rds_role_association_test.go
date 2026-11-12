// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	ackiam "github.com/aws-controllers-k8s/iam-controller/apis/v1alpha1"
	ackrds "github.com/aws-controllers-k8s/rds-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/awsemulator"
	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	rdsv1 "github.com/highfivelabs/ack-extensions/services/rds/apis/v1alpha1"
)

const account = "arn:aws:iam::123456789012:role/"

// cloudRoles is the cluster's or instance's AssociatedRoles in the fake
// cloud, as "role-arn feature status".
func cloudRoles(ctx context.Context, c *rds.Client, cluster, instance string) ([]string, error) {
	var out []string
	if cluster != "" {
		resp, err := c.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(cluster)})
		if err != nil {
			return nil, err
		}
		for _, r := range resp.DBClusters[0].AssociatedRoles {
			out = append(out, fmt.Sprintf("%s %s %s", aws.ToString(r.RoleArn), aws.ToString(r.FeatureName), aws.ToString(r.Status)))
		}
		return out, nil
	}
	resp, err := c.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(instance)})
	if err != nil {
		return nil, err
	}
	for _, r := range resp.DBInstances[0].AssociatedRoles {
		out = append(out, fmt.Sprintf("%s %s %s", aws.ToString(r.RoleArn), aws.ToString(r.FeatureName), aws.ToString(r.Status)))
	}
	return out, nil
}

func hasRole(roles []string, want string) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}

// DBRoleAssociation: an IAM role associated with an Aurora cluster or an
// RDS instance for one feature (S3 import and export, Lambda), which
// ACK's DBCluster and DBInstance only report.
//
// moto implements AddRoleToDBCluster and AddRoleToDBInstance but not
// RemoveRoleFromDBCluster or RemoveRoleFromDBInstance: it answers them
// with a bare HTTP 500 (moto 5.2.5; still unimplemented on moto's master,
// IMPLEMENTATION_COVERAGE.md, 2026-11-08). So the delete step here shows
// that deleting the object calls RemoveRoleFromDBCluster and keeps the
// object until it succeeds; that a successful removal, or an association
// already gone, releases the object is shown by
// services/rds/pkg/resource/db_role_association/sdk_test.go against a
// stand-in for RDS's documented responses.
func TestDBRoleAssociation(t *testing.T) {
	env, cloud := rdsEnv(t)
	ctx := context.Background()

	if _, err := cloud.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
		DBClusterIdentifier: aws.String("app-aurora"), Engine: aws.String("aurora-postgresql"),
		MasterUsername: aws.String("app"), MasterUserPassword: aws.String("not-a-real-password"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cloud.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String("app-db"), Engine: aws.String("postgres"),
		DBInstanceClass: aws.String("db.t4g.micro"), AllocatedStorage: aws.Int32(20),
		MasterUsername: aws.String("app"), MasterUserPassword: aws.String("not-a-real-password"),
	}); err != nil {
		t.Fatal(err)
	}

	waitSynced := func(t *testing.T, obj *rdsv1.DBRoleAssociation) *rdsv1.DBRoleAssociation {
		t.Helper()
		got := &rdsv1.DBRoleAssociation{}
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
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(obj), &rdsv1.DBRoleAssociation{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	}

	s3Import := &rdsv1.DBRoleAssociation{
		ObjectMeta: metav1.ObjectMeta{Name: "aurora-s3-import", Namespace: "default"},
		Spec: rdsv1.DBRoleAssociationSpec{DBClusterIdentifier: aws.String("app-aurora"),
			RoleARN: aws.String(account + "aurora-s3"), FeatureName: aws.String("s3Import")},
	}

	t.Run("a role is associated with a cluster and synced", func(t *testing.T) {
		if err := env.Client.Create(ctx, s3Import); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t, s3Import)
		if aws.ToString(got.Status.Status) != "ACTIVE" {
			t.Fatalf("status.status = %q", aws.ToString(got.Status.Status))
		}
		roles, err := cloudRoles(ctx, cloud, "app-aurora", "")
		if err != nil {
			t.Fatal(err)
		}
		if !hasRole(roles, account+"aurora-s3 s3Import ACTIVE") {
			t.Fatalf("cloud roles %v", roles)
		}
	})

	s3Export := &rdsv1.DBRoleAssociation{
		ObjectMeta: metav1.ObjectMeta{
			Name: "db-s3-export", Namespace: "default",
			Annotations: map[string]string{ackv1alpha1.AnnotationDeletionPolicy: string(ackv1alpha1.DeletionPolicyRetain)},
		},
		Spec: rdsv1.DBRoleAssociationSpec{DBInstanceIdentifier: aws.String("app-db"),
			RoleARN: aws.String(account + "db-s3"), FeatureName: aws.String("s3Export")},
	}

	t.Run("a role is associated with an instance", func(t *testing.T) {
		if err := env.Client.Create(ctx, s3Export); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, s3Export)
		roles, err := cloudRoles(ctx, cloud, "", "app-db")
		if err != nil {
			t.Fatal(err)
		}
		if !hasRole(roles, account+"db-s3 s3Export ACTIVE") {
			t.Fatalf("cloud roles %v", roles)
		}
	})

	t.Run("the CRD refuses what RDS would", func(t *testing.T) {
		for _, c := range []struct {
			name string
			spec rdsv1.DBRoleAssociationSpec
			want string
		}{
			{"an instance without a feature", rdsv1.DBRoleAssociationSpec{DBInstanceIdentifier: aws.String("app-db"), RoleARN: aws.String(account + "x")},
				"featureName is required for a DB instance"},
			{"both a cluster and an instance", rdsv1.DBRoleAssociationSpec{DBClusterIdentifier: aws.String("app-aurora"), DBInstanceIdentifier: aws.String("app-db"),
				RoleARN: aws.String(account + "x"), FeatureName: aws.String("s3Import")}, "exactly one of dbClusterIdentifier"},
			{"no role", rdsv1.DBRoleAssociationSpec{DBClusterIdentifier: aws.String("app-aurora")}, "exactly one of roleARN and roleRef"},
		} {
			obj := &rdsv1.DBRoleAssociation{ObjectMeta: metav1.ObjectMeta{Name: "refused", Namespace: "default"}, Spec: c.spec}
			err := env.Client.Create(ctx, obj)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s: got %v, want the CRD's refusal %q", c.name, err, c.want)
			}
		}
	})

	t.Run("the role cannot change", func(t *testing.T) {
		obj := &rdsv1.DBRoleAssociation{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(s3Import), obj); err != nil {
			t.Fatal(err)
		}
		obj.Spec.RoleARN = aws.String(account + "another")
		err := env.Client.Update(ctx, obj)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("changing roleARN: got %v, want the CRD's immutability refusal", err)
		}
	})

	t.Run("references resolve to ACK's DBCluster and Role", func(t *testing.T) {
		// ACK objects as ACK's rds- and iam-controller leave them once
		// synced; neither controller runs here.
		cluster := &ackrds.DBCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "app-aurora", Namespace: "default"},
			Spec:       ackrds.DBClusterSpec{DBClusterIdentifier: aws.String("app-aurora"), Engine: aws.String("aurora-postgresql")},
		}
		role := &ackiam.Role{
			ObjectMeta: metav1.ObjectMeta{Name: "aurora-lambda", Namespace: "default"},
			Spec: ackiam.RoleSpec{Name: aws.String("aurora-lambda"),
				AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"rds.amazonaws.com"},"Action":"sts:AssumeRole"}]}`)},
		}
		for _, o := range []client.Object{cluster, role} {
			if err := env.Client.Create(ctx, o); err != nil {
				t.Fatal(err)
			}
		}
		syncedCond := []*ackv1alpha1.Condition{{Type: ackv1alpha1.ConditionTypeResourceSynced, Status: corev1.ConditionTrue}}
		cluster.Status.Conditions = syncedCond
		arn := ackv1alpha1.AWSResourceName(account + "aurora-lambda")
		role.Status.Conditions = syncedCond
		role.Status.ACKResourceMetadata = &ackv1alpha1.ResourceMetadata{ARN: &arn, OwnerAccountID: ptr(ackv1alpha1.AWSAccountID("123456789012")), Region: ptr(ackv1alpha1.AWSRegion("us-west-2"))}
		for _, o := range []client.Object{cluster, role} {
			if err := env.Client.Status().Update(ctx, o); err != nil {
				t.Fatal(err)
			}
		}
		byRef := &rdsv1.DBRoleAssociation{
			ObjectMeta: metav1.ObjectMeta{Name: "aurora-lambda", Namespace: "default"},
			Spec: rdsv1.DBRoleAssociationSpec{
				DBClusterRef: &ackv1alpha1.AWSResourceReferenceWrapper{From: &ackv1alpha1.AWSResourceReference{Name: aws.String("app-aurora")}},
				RoleRef:      &ackv1alpha1.AWSResourceReferenceWrapper{From: &ackv1alpha1.AWSResourceReference{Name: aws.String("aurora-lambda")}},
				FeatureName:  aws.String("Lambda"),
			},
		}
		if err := env.Client.Create(ctx, byRef); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, byRef)
		roles, err := cloudRoles(ctx, cloud, "app-aurora", "")
		if err != nil {
			t.Fatal(err)
		}
		if !hasRole(roles, account+"aurora-lambda Lambda ACTIVE") {
			t.Fatalf("cloud roles %v", roles)
		}
	})

	t.Run("deletion-policy retain leaves the role associated", func(t *testing.T) {
		// A retained delete makes no AWS call, so the fake's missing
		// RemoveRoleFromDBInstance does not matter here: had our manager
		// called it, the call would fail and the object would stay.
		if err := env.Client.Delete(ctx, s3Export); err != nil {
			t.Fatal(err)
		}
		gone(t, s3Export)
		roles, err := cloudRoles(ctx, cloud, "", "app-db")
		if err != nil {
			t.Fatal(err)
		}
		if !hasRole(roles, account+"db-s3 s3Export ACTIVE") {
			t.Fatalf("deletion-policy retain removed the role: %v", roles)
		}
	})

	t.Run("delete calls RemoveRoleFromDBCluster and waits for it", func(t *testing.T) {
		if err := env.Client.Delete(ctx, s3Import); err != nil {
			t.Fatal(err)
		}
		if awsemulator.Selected(t) {
			// The AWS Emulator implements the removal (moto does not): the
			// role leaves the cluster and the object goes.
			gone(t, s3Import)
			harness.Eventually(t, 60*time.Second, "the role removed from the cluster", func() error {
				roles, err := cloudRoles(ctx, cloud, "app-aurora", "")
				if err != nil {
					return err
				}
				for _, r := range roles {
					if strings.HasPrefix(r, account+"aurora-s3 ") {
						return fmt.Errorf("still associated: %v", roles)
					}
				}
				return nil
			})
			return
		}
		harness.Eventually(t, 90*time.Second, "the removal attempted and the object kept", func() error {
			obj := &rdsv1.DBRoleAssociation{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(s3Import), obj); err != nil {
				return fmt.Errorf("object not kept: %v", err)
			}
			for _, c := range obj.Status.Conditions {
				if c.Type == ackv1alpha1.ConditionTypeRecoverable && c.Status == corev1.ConditionTrue &&
					strings.Contains(aws.ToString(c.Message), "RemoveRoleFromDBCluster") {
					return nil
				}
			}
			return fmt.Errorf("no recoverable condition naming RemoveRoleFromDBCluster yet")
		})
		roles, err := cloudRoles(ctx, cloud, "app-aurora", "")
		if err != nil {
			t.Fatal(err)
		}
		if !hasRole(roles, account+"aurora-s3 s3Import ACTIVE") {
			t.Fatalf("the fake removed a role it cannot remove: %v", roles)
		}
		// Let the object go, as a person would after removing the role by
		// hand, so the environment shuts down cleanly.
		obj := &rdsv1.DBRoleAssociation{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(s3Import), obj); err != nil {
			t.Fatal(err)
		}
		obj.Finalizers = nil
		if err := env.Client.Update(ctx, obj); err != nil {
			t.Fatal(err)
		}
		gone(t, s3Import)
	})
}

func ptr[T any](v T) *T { return &v }
