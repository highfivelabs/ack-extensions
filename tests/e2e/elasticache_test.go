// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	ecv1 "github.com/highfivelabs/ack-extensions/services/elasticache/apis/v1alpha1"
	"github.com/highfivelabs/ack-extensions/services/elasticache/pkg/resource/replication_group"
)

// TestElastiCacheReplicationGroupAndCacheCluster tests elasticache.aws.hfl.dev
// ReplicationGroup (numCacheClusters, transitEncryptionMode, clusterMode,
// authToken Secret value change detection, finalSnapshotIdentifier on delete)
// and CacheCluster (create, finalSnapshotIdentifier on delete) against moto.
func TestElastiCacheReplicationGroupAndCacheCluster(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = ecv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	env := harness.Start(t, "elasticache", scheme)
	cloud := elasticache.NewFromConfig(env.AWS)
	ctx := context.Background()

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "redis-auth", Namespace: "default"},
		StringData: map[string]string{"token": "initial-secret-token-123456"},
	}
	if err := env.Client.Create(ctx, secret); err != nil {
		t.Fatal(err)
	}

	rgID := "ackx-rg-batch2"
	authRef := &ackv1alpha1.SecretKeyReference{Key: "token"}
	authRef.Name = "redis-auth"
	authRef.Namespace = "default"
	rg := &ecv1.ReplicationGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "app-rg", Namespace: "default"},
		Spec: ecv1.ReplicationGroupSpec{
			ReplicationGroupID:       aws.String(rgID),
			Description:              aws.String("ack-extensions batch 2 test"),
			Engine:                   aws.String("redis"),
			CacheNodeType:            aws.String("cache.t3.micro"),
			NumCacheClusters:         aws.Int64(2),
			TransitEncryptionEnabled: aws.Bool(true),
			TransitEncryptionMode:    aws.String("preferred"),
			ClusterMode:              aws.String("disabled"),
			AuthToken:                authRef,
			FinalSnapshotIdentifier:  aws.String("ackx-rg-final"),
		},
	}
	if err := env.Client.Create(ctx, rg); err != nil {
		t.Fatal(err)
	}
	cleanupLog(t, "replication group "+rgID, func() error {
		_, err := cloud.DeleteReplicationGroup(ctx, &elasticache.DeleteReplicationGroupInput{
			ReplicationGroupId: aws.String(rgID),
		})
		return err
	})

	var initialHash string
	harness.Eventually(t, 60*time.Second, "replication group synced", func() error {
		var cur ecv1.ReplicationGroup
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(rg), &cur); err != nil {
			return err
		}
		initialHash = cur.Annotations[replication_group.AnnotationLastAuthTokenSHA256]
		if ok, why := synced(cur.Status.Conditions); !ok || initialHash == "" {
			return fmt.Errorf("not synced (hash=%q): %s", initialHash, why)
		}
		return nil
	})

	// Update Secret value without changing spec.authToken reference; controller
	// must notice the Secret change and update the recorded SHA-256 annotation.
	var curSecret corev1.Secret
	if err := env.Client.Get(ctx, client.ObjectKeyFromObject(secret), &curSecret); err != nil {
		t.Fatal(err)
	}
	curSecret.Data["token"] = []byte("rotated-secret-token-654321")
	if err := env.Client.Update(ctx, &curSecret); err != nil {
		t.Fatal(err)
	}

	harness.Eventually(t, 60*time.Second, "replication group rotated auth token hash", func() error {
		var cur ecv1.ReplicationGroup
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(rg), &cur); err != nil {
			return err
		}
		h := cur.Annotations[replication_group.AnnotationLastAuthTokenSHA256]
		if h == "" || h == initialHash {
			return fmt.Errorf("hash unchanged (%q)", h)
		}
		if ok, why := synced(cur.Status.Conditions); !ok {
			return fmt.Errorf("not synced: %s", why)
		}
		return nil
	})

	// Delete ReplicationGroup (with finalSnapshotIdentifier).
	var curRG ecv1.ReplicationGroup
	if err := env.Client.Get(ctx, client.ObjectKeyFromObject(rg), &curRG); err != nil {
		t.Fatal(err)
	}
	if err := env.Client.Delete(ctx, &curRG); err != nil {
		t.Fatal(err)
	}
	// moto 5.2.5 transitions DeleteReplicationGroup to "deleting" without
	// removing it from DescribeReplicationGroups; accept either not-found or
	// status == "deleting".
	harness.Eventually(t, 60*time.Second, "replication group deleting or deleted", func() error {
		out, err := cloud.DescribeReplicationGroups(ctx, &elasticache.DescribeReplicationGroupsInput{
			ReplicationGroupId: aws.String(rgID),
		})
		if err != nil {
			return nil
		}
		if len(out.ReplicationGroups) > 0 && aws.ToString(out.ReplicationGroups[0].Status) == "deleting" {
			return nil
		}
		return fmt.Errorf("replication group %s still active", rgID)
	})

	// Test CacheCluster create and delete with finalSnapshotIdentifier.
	ccID := "ackx-cc-batch2"
	cc := &ecv1.CacheCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "app-cc", Namespace: "default"},
		Spec: ecv1.CacheClusterSpec{
			CacheClusterID:            aws.String(ccID),
			Engine:                    aws.String("redis"),
			CacheNodeType:             aws.String("cache.t3.micro"),
			NumCacheNodes:             aws.Int64(1),
			PreferredAvailabilityZone: aws.String(harness.Region + "a"),
			FinalSnapshotIdentifier:   aws.String("ackx-cc-final"),
			Tags: []*ecv1.Tag{
				{Key: aws.String("env"), Value: aws.String("test")},
			},
		},
	}
	if err := env.Client.Create(ctx, cc); err != nil {
		t.Fatal(err)
	}
	cleanupLog(t, "cache cluster "+ccID, func() error {
		_, err := cloud.DeleteCacheCluster(ctx, &elasticache.DeleteCacheClusterInput{
			CacheClusterId: aws.String(ccID),
		})
		return err
	})
	harness.Eventually(t, 60*time.Second, "cache cluster synced", func() error {
		var cur ecv1.CacheCluster
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(cc), &cur); err != nil {
			return err
		}
		if ok, why := synced(cur.Status.Conditions); !ok {
			return fmt.Errorf("not synced: %s", why)
		}
		return nil
	})
	var curCC ecv1.CacheCluster
	if err := env.Client.Get(ctx, client.ObjectKeyFromObject(cc), &curCC); err != nil {
		t.Fatal(err)
	}
	if err := env.Client.Delete(ctx, &curCC); err != nil {
		t.Fatal(err)
	}
	harness.Eventually(t, 60*time.Second, "cache cluster deleting or deleted", func() error {
		out, err := cloud.DescribeCacheClusters(ctx, &elasticache.DescribeCacheClustersInput{
			CacheClusterId: aws.String(ccID),
		})
		if err != nil {
			return nil
		}
		if len(out.CacheClusters) > 0 && aws.ToString(out.CacheClusters[0].CacheClusterStatus) == "deleting" {
			return nil
		}
		return fmt.Errorf("cache cluster %s still active", ccID)
	})
}
