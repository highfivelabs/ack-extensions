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
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	efsv1 "github.com/highfivelabs/ack-extensions/services/efs/apis/v1alpha1"
)

// TestEFSFileSystemAndMountTarget tests efs.aws.hfl.dev FileSystem (creationToken,
// bypassPolicyLockoutSafetyCheck, policy, tags, update, delete) and MountTarget
// (ipAddressType, securityGroups update, delete) against moto.
func TestEFSFileSystemAndMountTarget(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = efsv1.AddToScheme(scheme)
	env := harness.Start(t, "efs", scheme)
	efsCloud := efs.NewFromConfig(env.AWS)
	ec2Cloud := ec2.NewFromConfig(env.AWS)
	ctx := context.Background()

	vpcOut, err := ec2Cloud.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.42.0.0/16")})
	if err != nil {
		t.Fatal(err)
	}
	vpcID := aws.ToString(vpcOut.Vpc.VpcId)
	cleanupLog(t, "vpc "+vpcID, func() error {
		_, err := ec2Cloud.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: aws.String(vpcID)})
		return err
	})

	subOut, err := ec2Cloud.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            aws.String(vpcID),
		CidrBlock:        aws.String("10.42.1.0/24"),
		AvailabilityZone: aws.String(harness.Region + "a"),
	})
	if err != nil {
		t.Fatal(err)
	}
	subnetID := aws.ToString(subOut.Subnet.SubnetId)
	cleanupLog(t, "subnet "+subnetID, func() error {
		_, err := ec2Cloud.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: aws.String(subnetID)})
		return err
	})

	sg1Out, err := ec2Cloud.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String("ackx-efs-sg1"),
		Description: aws.String("efs sg 1"),
		VpcId:       aws.String(vpcID),
	})
	if err != nil {
		t.Fatal(err)
	}
	sg1 := aws.ToString(sg1Out.GroupId)
	cleanupLog(t, "sg "+sg1, func() error {
		_, err := ec2Cloud.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: aws.String(sg1)})
		return err
	})

	sg2Out, err := ec2Cloud.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String("ackx-efs-sg2"),
		Description: aws.String("efs sg 2"),
		VpcId:       aws.String(vpcID),
	})
	if err != nil {
		t.Fatal(err)
	}
	sg2 := aws.ToString(sg2Out.GroupId)
	cleanupLog(t, "sg "+sg2, func() error {
		_, err := ec2Cloud.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: aws.String(sg2)})
		return err
	})

	token := "ackx-efs-token-001"
	fs := &efsv1.FileSystem{
		ObjectMeta: metav1.ObjectMeta{Name: "app-efs", Namespace: "default"},
		Spec: efsv1.FileSystemSpec{
			CreationToken:                  aws.String(token),
			Encrypted:                      aws.Bool(true),
			PerformanceMode:                aws.String("generalPurpose"),
			ThroughputMode:                 aws.String("bursting"),
			BypassPolicyLockoutSafetyCheck: aws.Bool(true),
			Policy:                         aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"*"},"Action":"elasticfilesystem:ClientMount","Resource":"*"}]}`),
			Tags: []*efsv1.Tag{
				{Key: aws.String("Name"), Value: aws.String("app-efs")},
			},
		},
	}
	if err := env.Client.Create(ctx, fs); err != nil {
		t.Fatal(err)
	}

	var fsID string
	harness.Eventually(t, 60*time.Second, "filesystem synced", func() error {
		var cur efsv1.FileSystem
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(fs), &cur); err != nil {
			return err
		}
		if cur.Status.FileSystemID != nil {
			fsID = *cur.Status.FileSystemID
		}
		if ok, why := synced(cur.Status.Conditions); !ok || fsID == "" {
			return fmt.Errorf("not synced (id=%q): %s", fsID, why)
		}
		return nil
	})
	cleanupLog(t, "efs "+fsID, func() error {
		_, err := efsCloud.DeleteFileSystem(ctx, &efs.DeleteFileSystemInput{FileSystemId: aws.String(fsID)})
		return err
	})

	fsOut, err := efsCloud.DescribeFileSystems(ctx, &efs.DescribeFileSystemsInput{FileSystemId: aws.String(fsID)})
	if err != nil || len(fsOut.FileSystems) == 0 {
		t.Fatalf("DescribeFileSystems(%s): len=%d err=%v", fsID, len(fsOut.FileSystems), err)
	}
	if aws.ToString(fsOut.FileSystems[0].CreationToken) != token {
		t.Fatalf("CreationToken = %q, want %q", aws.ToString(fsOut.FileSystems[0].CreationToken), token)
	}

	// Create MountTarget referencing FileSystem via fileSystemRef with ipAddressType.
	mt := &efsv1.MountTarget{
		ObjectMeta: metav1.ObjectMeta{Name: "app-mt", Namespace: "default"},
		Spec: efsv1.MountTargetSpec{
			FileSystemRef: &ackv1alpha1.AWSResourceReferenceWrapper{
				From: &ackv1alpha1.AWSResourceReference{Name: aws.String("app-efs")},
			},
			SubnetID:       aws.String(subnetID),
			IPAddressType:  aws.String("IPV4_ONLY"),
			SecurityGroups: []*string{aws.String(sg1)},
		},
	}
	if err := env.Client.Create(ctx, mt); err != nil {
		t.Fatal(err)
	}

	var mtID string
	harness.Eventually(t, 60*time.Second, "mount target synced", func() error {
		var cur efsv1.MountTarget
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(mt), &cur); err != nil {
			return err
		}
		if cur.Status.MountTargetID != nil {
			mtID = *cur.Status.MountTargetID
		}
		if ok, why := synced(cur.Status.Conditions); !ok || mtID == "" {
			return fmt.Errorf("not synced (id=%q): %s", mtID, why)
		}
		return nil
	})
	cleanupLog(t, "mount target "+mtID, func() error {
		_, err := efsCloud.DeleteMountTarget(ctx, &efs.DeleteMountTargetInput{MountTargetId: aws.String(mtID)})
		return err
	})

	// Update MountTarget security groups.
	var curMT efsv1.MountTarget
	if err := env.Client.Get(ctx, client.ObjectKeyFromObject(mt), &curMT); err != nil {
		t.Fatal(err)
	}
	curMT.Spec.SecurityGroups = []*string{aws.String(sg2)}
	if err := env.Client.Update(ctx, &curMT); err != nil {
		t.Fatal(err)
	}
	harness.Eventually(t, 60*time.Second, "mount target security group updated", func() error {
		out, err := efsCloud.DescribeMountTargetSecurityGroups(ctx, &efs.DescribeMountTargetSecurityGroupsInput{
			MountTargetId: aws.String(mtID),
		})
		if err != nil {
			return err
		}
		if len(out.SecurityGroups) != 1 || out.SecurityGroups[0] != sg2 {
			return fmt.Errorf("security groups = %v, want [%s]", out.SecurityGroups, sg2)
		}
		return nil
	})

	// Delete MountTarget then FileSystem.
	if err := env.Client.Delete(ctx, &curMT); err != nil {
		t.Fatal(err)
	}
	harness.Eventually(t, 60*time.Second, "mount target deleted", func() error {
		out, err := efsCloud.DescribeMountTargets(ctx, &efs.DescribeMountTargetsInput{FileSystemId: aws.String(fsID)})
		if err != nil {
			return err
		}
		if len(out.MountTargets) != 0 {
			return fmt.Errorf("%d mount targets still present", len(out.MountTargets))
		}
		return nil
	})

	var curFS efsv1.FileSystem
	if err := env.Client.Get(ctx, client.ObjectKeyFromObject(fs), &curFS); err != nil {
		t.Fatal(err)
	}
	if err := env.Client.Delete(ctx, &curFS); err != nil {
		t.Fatal(err)
	}
	harness.Eventually(t, 60*time.Second, "filesystem deleted", func() error {
		out, err := efsCloud.DescribeFileSystems(ctx, &efs.DescribeFileSystemsInput{FileSystemId: aws.String(fsID)})
		if err != nil {
			return nil
		}
		for _, f := range out.FileSystems {
			if f.LifeCycleState != efstypes.LifeCycleStateDeleted {
				return fmt.Errorf("filesystem %s still %s", fsID, f.LifeCycleState)
			}
		}
		return nil
	})
}
