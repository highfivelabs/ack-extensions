// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	ackiam "github.com/aws-controllers-k8s/iam-controller/apis/v1alpha1"
	ackrds "github.com/aws-controllers-k8s/rds-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	rdsv1 "github.com/highfivelabs/ack-extensions/services/rds/apis/v1alpha1"
)

func rdsEnv(t *testing.T) (*harness.Env, *rds.Client) {
	scheme := runtime.NewScheme()
	_ = rdsv1.AddToScheme(scheme)
	_ = ackrds.AddToScheme(scheme)
	_ = ackiam.AddToScheme(scheme)
	env := harness.Start(t, "rds", scheme)
	return env, rds.NewFromConfig(env.AWS)
}

func cloudOptions(ctx context.Context, c *rds.Client, group string) ([]string, error) {
	out, err := c.DescribeOptionGroups(ctx, &rds.DescribeOptionGroupsInput{OptionGroupName: aws.String(group)})
	if err != nil {
		return nil, err
	}
	if len(out.OptionGroupsList) != 1 {
		return nil, fmt.Errorf("%d option groups named %s", len(out.OptionGroupsList), group)
	}
	var names []string
	for _, o := range out.OptionGroupsList[0].Options {
		names = append(names, aws.ToString(o.OptionName))
	}
	sort.Strings(names)
	return names, nil
}

// OptionGroup, as upstream pull request #257 builds it: created without
// options, which ModifyOptionGroup then adds; an option taken off the spec
// is removed; delete removes the group.
func TestOptionGroup(t *testing.T) {
	env, cloud := rdsEnv(t)
	ctx := context.Background()

	og := &rdsv1.OptionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "app-mysql", Namespace: "default"},
		Spec: rdsv1.OptionGroupSpec{
			Name:               aws.String("app-mysql"),
			Description:        aws.String("audit and memcached for the app database"),
			EngineName:         aws.String("mysql"),
			MajorEngineVersion: aws.String("8.0"),
			Options: []*rdsv1.OptionConfiguration{
				{OptionName: aws.String("MARIADB_AUDIT_PLUGIN")},
				{OptionName: aws.String("MEMCACHED")},
			},
			Tags: []*rdsv1.Tag{{Key: aws.String("team"), Value: aws.String("payments")}},
		},
	}

	waitOptions := func(t *testing.T, want []string) {
		t.Helper()
		harness.Eventually(t, 90*time.Second, fmt.Sprintf("options %v in the cloud and synced", want), func() error {
			got, err := cloudOptions(ctx, cloud, "app-mysql")
			if err != nil {
				return err
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				return fmt.Errorf("cloud options %v, want %v", got, want)
			}
			obj := &rdsv1.OptionGroup{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(og), obj); err != nil {
				return err
			}
			if ok, why := synced(obj.Status.Conditions); !ok {
				return fmt.Errorf("not synced: %s", why)
			}
			if len(obj.Status.ObservedOptions) != len(want) {
				return fmt.Errorf("status.observedOptions has %d, want %d", len(obj.Status.ObservedOptions), len(want))
			}
			return nil
		})
	}

	t.Run("create makes the group, then adds its options", func(t *testing.T) {
		if err := env.Client.Create(ctx, og); err != nil {
			t.Fatal(err)
		}
		waitOptions(t, []string{"MARIADB_AUDIT_PLUGIN", "MEMCACHED"})
		out, err := cloud.DescribeOptionGroups(ctx, &rds.DescribeOptionGroupsInput{OptionGroupName: aws.String("app-mysql")})
		if err != nil {
			t.Fatal(err)
		}
		tags, err := cloud.ListTagsForResource(ctx, &rds.ListTagsForResourceInput{ResourceName: out.OptionGroupsList[0].OptionGroupArn})
		if err != nil {
			t.Fatal(err)
		}
		if !hasTag(tags.TagList, "team", "payments") {
			t.Fatalf("cloud tags %v lack team=payments", tags.TagList)
		}
	})

	t.Run("an option taken off the spec is removed", func(t *testing.T) {
		obj := &rdsv1.OptionGroup{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(og), obj); err != nil {
			t.Fatal(err)
		}
		obj.Spec.Options = []*rdsv1.OptionConfiguration{{OptionName: aws.String("MARIADB_AUDIT_PLUGIN")}}
		if err := env.Client.Update(ctx, obj); err != nil {
			t.Fatal(err)
		}
		waitOptions(t, []string{"MARIADB_AUDIT_PLUGIN"})
	})

	t.Run("the engine cannot change", func(t *testing.T) {
		obj := &rdsv1.OptionGroup{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(og), obj); err != nil {
			t.Fatal(err)
		}
		obj.Spec.EngineName = aws.String("mariadb")
		err := env.Client.Update(ctx, obj)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("changing engineName: got %v, want the CRD's immutability refusal", err)
		}
	})

	t.Run("delete removes the group", func(t *testing.T) {
		if err := env.Client.Delete(ctx, og); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "group gone from the cloud", func() error {
			_, err := cloudOptions(ctx, cloud, "app-mysql")
			if err == nil {
				return fmt.Errorf("still there")
			}
			if !strings.Contains(err.Error(), "OptionGroupNotFoundFault") {
				return err
			}
			return nil
		})
	})
}

func hasTag(tags []rdstypes.Tag, k, v string) bool {
	for _, tg := range tags {
		if aws.ToString(tg.Key) == k && aws.ToString(tg.Value) == v {
			return true
		}
	}
	return false
}

// The fixtures RDS Proxy fronts: a proxy, an instance and an Aurora
// cluster, made directly in the fake cloud (ACK's own DBProxy, DBInstance
// and DBCluster would make them on a real cluster).
func proxyFixtures(t *testing.T, ctx context.Context, env *harness.Env, cloud *rds.Client) {
	t.Helper()
	net := ec2.NewFromConfig(env.AWS)
	vpc, err := net.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.20.0.0/16")})
	if err != nil {
		t.Fatal(err)
	}
	var subnets []string
	for i, az := range []string{"us-west-2a", "us-west-2b"} {
		sn, err := net.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String(fmt.Sprintf("10.20.%d.0/24", i)), AvailabilityZone: aws.String(az)})
		if err != nil {
			t.Fatal(err)
		}
		subnets = append(subnets, aws.ToString(sn.Subnet.SubnetId))
	}
	if _, err := cloud.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String("app-db"), Engine: aws.String("postgres"),
		DBInstanceClass: aws.String("db.t4g.micro"), AllocatedStorage: aws.Int32(20),
		MasterUsername: aws.String("app"), MasterUserPassword: aws.String("not-a-real-password"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cloud.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
		DBClusterIdentifier: aws.String("app-aurora"), Engine: aws.String("aurora-postgresql"),
		MasterUsername: aws.String("app"), MasterUserPassword: aws.String("not-a-real-password"),
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app-proxy", "aurora-proxy"} {
		if _, err := cloud.CreateDBProxy(ctx, &rds.CreateDBProxyInput{
			DBProxyName: aws.String(name), EngineFamily: rdstypes.EngineFamilyPostgresql,
			RoleArn:      aws.String("arn:aws:iam::123456789012:role/proxy-secrets"),
			VpcSubnetIds: subnets,
			Auth: []rdstypes.UserAuthConfig{{
				AuthScheme: rdstypes.AuthSchemeSecrets,
				SecretArn:  aws.String("arn:aws:secretsmanager:us-west-2:123456789012:secret:app-db"),
				IAMAuth:    rdstypes.IAMAuthModeDisabled,
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func cloudTargets(ctx context.Context, c *rds.Client, proxy string) (map[string]rdstypes.TargetType, error) {
	out, err := c.DescribeDBProxyTargets(ctx, &rds.DescribeDBProxyTargetsInput{DBProxyName: aws.String(proxy)})
	if err != nil {
		return nil, err
	}
	m := map[string]rdstypes.TargetType{}
	for _, tg := range out.Targets {
		m[aws.ToString(tg.RdsResourceId)] = tg.Type
	}
	return m, nil
}

func TestDBProxyTarget(t *testing.T) {
	env, cloud := rdsEnv(t)
	ctx := context.Background()
	proxyFixtures(t, ctx, env, cloud)

	waitSynced := func(t *testing.T, obj *rdsv1.DBProxyTarget) *rdsv1.DBProxyTarget {
		t.Helper()
		got := &rdsv1.DBProxyTarget{}
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

	instance := &rdsv1.DBProxyTarget{
		ObjectMeta: metav1.ObjectMeta{Name: "app-db", Namespace: "default"},
		Spec:       rdsv1.DBProxyTargetSpec{DBProxyName: aws.String("app-proxy"), DBInstanceIdentifier: aws.String("app-db")},
	}

	t.Run("an instance is registered and synced", func(t *testing.T) {
		// The fake reports a new target REGISTERING once, in the
		// RegisterDBProxyTargets response, and AVAILABLE on every read
		// after; ACK's runtime reads right after a create and keeps that
		// status, so REGISTERING is never visible here. The rule (synced
		// only when every target is AVAILABLE) is covered by
		// services/rds/pkg/resource/db_proxy_target/sdk_test.go.
		if err := env.Client.Create(ctx, instance); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t, instance)
		if len(got.Status.Targets) != 1 {
			t.Fatalf("status.targets = %d, want 1", len(got.Status.Targets))
		}
		tg := got.Status.Targets[0]
		if aws.ToString(tg.Type) != "RDS_INSTANCE" || aws.ToString(tg.RDSResourceID) != "app-db" || aws.ToString(tg.TargetHealth.State) != "AVAILABLE" {
			t.Fatalf("status.targets[0] = type %s id %s health %s", aws.ToString(tg.Type), aws.ToString(tg.RDSResourceID), aws.ToString(tg.TargetHealth.State))
		}
		m, err := cloudTargets(ctx, cloud, "app-proxy")
		if err != nil {
			t.Fatal(err)
		}
		if m["app-db"] != rdstypes.TargetTypeRdsInstance {
			t.Fatalf("cloud targets %v", m)
		}
	})

	t.Run("a cluster is registered as a tracked cluster", func(t *testing.T) {
		cluster := &rdsv1.DBProxyTarget{
			ObjectMeta: metav1.ObjectMeta{Name: "app-aurora", Namespace: "default"},
			Spec:       rdsv1.DBProxyTargetSpec{DBProxyName: aws.String("aurora-proxy"), DBClusterIdentifier: aws.String("app-aurora")},
		}
		if err := env.Client.Create(ctx, cluster); err != nil {
			t.Fatal(err)
		}
		got := waitSynced(t, cluster)
		if aws.ToString(got.Status.Targets[0].Type) != "TRACKED_CLUSTER" {
			t.Fatalf("status.targets[0].type = %s", aws.ToString(got.Status.Targets[0].Type))
		}
		m, err := cloudTargets(ctx, cloud, "aurora-proxy")
		if err != nil {
			t.Fatal(err)
		}
		if m["app-aurora"] != rdstypes.TargetTypeTrackedCluster {
			t.Fatalf("cloud targets %v", m)
		}
	})

	t.Run("both an instance and a cluster is refused by the CRD", func(t *testing.T) {
		both := &rdsv1.DBProxyTarget{
			ObjectMeta: metav1.ObjectMeta{Name: "both", Namespace: "default"},
			Spec: rdsv1.DBProxyTargetSpec{DBProxyName: aws.String("app-proxy"),
				DBInstanceIdentifier: aws.String("app-db"), DBClusterIdentifier: aws.String("app-aurora")},
		}
		err := env.Client.Create(ctx, both)
		if err == nil || !strings.Contains(err.Error(), "exactly one of dbInstanceIdentifier") {
			t.Fatalf("got %v, want the CRD's exactly-one refusal", err)
		}
	})

	t.Run("references resolve to ACK's DBProxy and DBInstance", func(t *testing.T) {
		if _, err := cloud.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
			DBInstanceIdentifier: aws.String("reports-db"), Engine: aws.String("postgres"),
			DBInstanceClass: aws.String("db.t4g.micro"), AllocatedStorage: aws.Int32(20),
			MasterUsername: aws.String("app"), MasterUserPassword: aws.String("not-a-real-password"),
		}); err != nil {
			t.Fatal(err)
		}
		// ACK objects as ACK's rds-controller leaves them once synced.
		proxy := &ackrds.DBProxy{
			ObjectMeta: metav1.ObjectMeta{Name: "app-proxy", Namespace: "default"},
			Spec: ackrds.DBProxySpec{Name: aws.String("app-proxy"), EngineFamily: aws.String("POSTGRESQL"),
				RoleARN: aws.String("arn:aws:iam::123456789012:role/proxy-secrets"), VPCSubnetIDs: []*string{aws.String("subnet-aaaa1111")},
				Auth: []*ackrds.UserAuthConfig{{AuthScheme: aws.String("SECRETS")}}},
		}
		db := &ackrds.DBInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "reports-db", Namespace: "default"},
			Spec: ackrds.DBInstanceSpec{DBInstanceIdentifier: aws.String("reports-db"), Engine: aws.String("postgres"),
				DBInstanceClass: aws.String("db.t4g.micro")},
		}
		for _, o := range []client.Object{proxy, db} {
			if err := env.Client.Create(ctx, o); err != nil {
				t.Fatal(err)
			}
		}
		syncedCond := []*ackv1alpha1.Condition{{Type: ackv1alpha1.ConditionTypeResourceSynced, Status: corev1.ConditionTrue}}
		proxy.Status.Conditions, db.Status.Conditions = syncedCond, syncedCond
		for _, o := range []client.Object{proxy, db} {
			if err := env.Client.Status().Update(ctx, o); err != nil {
				t.Fatal(err)
			}
		}
		byRef := &rdsv1.DBProxyTarget{
			ObjectMeta: metav1.ObjectMeta{Name: "reports-db", Namespace: "default"},
			Spec: rdsv1.DBProxyTargetSpec{
				DBProxyRef:    &ackv1alpha1.AWSResourceReferenceWrapper{From: &ackv1alpha1.AWSResourceReference{Name: aws.String("app-proxy")}},
				DBInstanceRef: &ackv1alpha1.AWSResourceReferenceWrapper{From: &ackv1alpha1.AWSResourceReference{Name: aws.String("reports-db")}},
			},
		}
		if err := env.Client.Create(ctx, byRef); err != nil {
			t.Fatal(err)
		}
		waitSynced(t, byRef)
		m, err := cloudTargets(ctx, cloud, "app-proxy")
		if err != nil {
			t.Fatal(err)
		}
		if m["reports-db"] != rdstypes.TargetTypeRdsInstance {
			t.Fatalf("cloud targets %v", m)
		}
	})

	t.Run("delete deregisters the instance", func(t *testing.T) {
		if err := env.Client.Delete(ctx, instance); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, 60*time.Second, "deregistered and object gone", func() error {
			m, err := cloudTargets(ctx, cloud, "app-proxy")
			if err != nil {
				return err
			}
			if _, ok := m["app-db"]; ok {
				return fmt.Errorf("app-db still a target: %v", m)
			}
			err = env.Client.Get(ctx, client.ObjectKeyFromObject(instance), &rdsv1.DBProxyTarget{})
			if err == nil {
				return fmt.Errorf("object still present")
			}
			return client.IgnoreNotFound(err)
		})
	})
}
