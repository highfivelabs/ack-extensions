# Notes to upstream (drafts, not filed)

The builder's answer to `docs/plan.md` open question 1 (2026-11-13): each
field filled here on ACK's own kinds ([`extensions.md`](extensions.md)) is
paired with a note to upstream. This repository opens, comments on and
reacts to nothing upstream (CLAUDE.md, "Never"), so each note is written
here as a ready-to-file draft: a title, where it goes, and the body. A
person files it, from their own account, and records the link in the
"Filed" column.

ACK takes feature requests as issues in `aws-controllers-k8s/community`
(labels `kind/enhancement` and `service/<name>`); a pull request goes to
the controller's own repository. Versions are those read on 2026-11-12
(`docs/plan.md` section 5): ec2-controller `v1.24.0`, rds-controller
`v1.15.0`, s3-controller `v1.16.0`, kms-controller `v1.8.0`,
acm-controller `v1.12.0`, route53-controller `v1.9.0`.

| # | Note | Where | Filled here? | Filed |
| --- | --- | --- | --- | --- |
| 1 | KMS `Key`: `rotationPeriodInDays` | community issue (then kms-controller PR) | yes, `kms.aws.hfl.dev/rotation-period-in-days` | not yet |
| 2 | KMS `Key`: `enabled` in the spec | community issue | yes, `kms.aws.hfl.dev/enabled` | not yet |
| 3 | KMS `Key`: the deletion window as a spec field | community issue | ACK's own annotation already does it | not yet |
| 4 | S3 `Bucket`: empty the bucket on delete | community issue (community#1393 was closed as stale) | yes, `s3.aws.hfl.dev/force-destroy` | not yet |
| 5 | EC2 `Instance`: five `RunInstances` fields off `ignore` | community issue (beside community#2765) | no: create-time | not yet |
| 6 | RDS `DBInstance`: self-managed Active Directory fields | community issue | no | not yet |
| 7 | RDS `DBInstance`: point-in-time restore | community issue | no: create-time | not yet |
| 8 | RDS `DBInstance`: restore from S3 | community issue | no: create-time | not yet |
| 9 | RDS blue/green deployments | a comment on community#1992, or nothing (it is open) | no | not yet |
| 10 | ACM `Certificate`: synced only once `ISSUED` | community issue | no | not yet |
| 11 | ACM `Certificate`: `validationMethod` | community issue | no: create-time | not yet |
| 12 | Route 53 `RecordSet`: overwrite on create | community issue | no | not yet |
| 13 | moto: `RemoveRoleFromDBCluster` and `RemoveRoleFromDBInstance` | getmoto/moto pull request | no: `DBRoleAssociation`'s delete is proven on a stand-in | not yet |
| 14 | S3 `Bucket`: `customFindBucket` panics without default encryption | community issue, or reopen community#2990 (text in `docs/plan.md` section 8 item 15) | yes, `s3.aws.hfl.dev` `Bucket`, a fixed copy (intentional overlap) | not yet: the builder files it (UPBUG, 2026-11-15); `TODO(builder): link once filed` |
| 15 | AWS Emulator: the 14 operations behind the ACK-kind failures | aws/aws-emulator pull requests, one per service | no: moto stays the test of record for those tests | not yet |

EC2 `Instance.securityGroupRefs` needs no note: ec2-controller PR #406
merged it (closing community#3132) and `v1.25.0` ships it.

---

## 1. KMS `Key`: `rotationPeriodInDays`

**Title:** kms-controller: support `RotationPeriodInDays` on `Key` (custom automatic rotation period)

**Labels:** `kind/enhancement`, `service/kms`

**Body:**

> **Is your feature request related to a problem?**
> `Key` has `spec.enableKeyRotation`, which the controller turns on with
> `EnableKeyRotation`, but no way to set the rotation period. KMS has
> supported a custom period since 2024 (`EnableKeyRotation`
> `RotationPeriodInDays`, 90 to 2560 days; `GetKeyRotationStatus` returns
> it). Every key made through ACK rotates every 365 days, and a policy
> that asks for a shorter period cannot be met. Terraform has
> `aws_kms_key.rotation_period_in_days`, CloudFormation
> `AWS::KMS::Key.RotationPeriodInDays`.
>
> **Describe the solution you'd like**
> A field `spec.rotationPeriodInDays` (integer, 90 to 2560), passed as
> `RotationPeriodInDays` in the `EnableKeyRotation` call the existing
> `updateKeyRotation` hook already makes, read back from
> `GetKeyRotationStatus` in `sdkFind` (where `enableKeyRotation` is read
> today), and compared in the delta, so a change calls `EnableKeyRotation`
> again with the new period. Today the hook calls `EnableKeyRotation` only
> when `KeyRotationEnabled` differs; it would also call it when the period
> differs.
>
> **Describe alternatives you've considered**
> We set it with a controller beside kms-controller that reads an
> annotation and calls `EnableKeyRotation` with the period once the key
> exists. It works, but a field is what users expect.
>
> **Additional context**
> kms-controller `v1.8.0` (`generator.yaml`: `EnableKeyRotation` is a
> custom field; `pkg/resource/key/hooks.go`, `updateKeyRotation`).

## 2. KMS `Key`: `enabled` in the spec

**Title:** kms-controller: enable and disable a `Key` from its spec

**Labels:** `kind/enhancement`, `service/kms`

**Body:**

> **Is your feature request related to a problem?**
> `Key` reports `status.enabled` but has no way to disable a key or
> enable it again: the controller never calls `DisableKey` or `EnableKey`.
> Disabling a key is the usual first step before scheduling its deletion,
> and the way to stop its use during an incident. Terraform has
> `aws_kms_key.is_enabled`, CloudFormation `AWS::KMS::Key.Enabled`.
>
> **Describe the solution you'd like**
> A field `spec.enabled` (boolean, default true), late-initialized from
> `DescribeKey`'s `KeyMetadata.Enabled`, and a `customUpdate` branch that
> calls `DisableKey` or `EnableKey` when it differs from the key's state
> (`KeyState` `Enabled` or `Disabled`; a key in another state is left
> until it settles). `status.enabled` can stay as it is.
>
> **Describe alternatives you've considered**
> A controller beside kms-controller reading an annotation, which is what
> we run today.
>
> **Additional context**
> kms-controller `v1.8.0`; `KMSInvalidStateException` from
> `EnableKeyRotation` on a disabled key means the rotation hook should run
> after the enable in the same update.

## 3. KMS `Key`: the deletion window as a spec field

**Title:** kms-controller: `pendingWindowInDays` as a `Key` spec field, beside the annotation

**Labels:** `kind/enhancement`, `service/kms`

**Body:**

> **Is your feature request related to a problem?**
> The deletion window is set by the annotation
> `kms.services.k8s.aws/pending-window-in-days` (default 7), which works
> but is not part of the schema: a typo or an out-of-range value (KMS
> takes 7 to 30) falls back to 7 silently (`GetDeletePendingWindowInDays`
> ignores a value it cannot parse), and tools that generate manifests from
> the CRD's schema cannot see it. Terraform has
> `aws_kms_key.deletion_window_in_days`, CloudFormation
> `AWS::KMS::Key.PendingWindowInDays`.
>
> **Describe the solution you'd like**
> A field `spec.pendingWindowInDays` (integer, 7 to 30, validated by the
> CRD), used by the delete hook ahead of the annotation, the annotation
> kept for compatibility. At least: report an annotation that cannot be
> parsed (a condition or an event) instead of using 7.
>
> **Additional context**
> community#1787 asks for an immediate delete, which KMS does not allow;
> this is the window only.

## 4. S3 `Bucket`: empty the bucket on delete

**Title:** s3-controller: option to empty a `Bucket` before deleting it (like Terraform's `force_destroy`)

**Labels:** `kind/enhancement`, `service/s3`

**Body:**

> **Is your feature request related to a problem?**
> Deleting a `Bucket` that has objects calls `DeleteBucket`, which fails
> with `BucketNotEmpty` and is retried for ever; the object stays in
> Kubernetes with its finalizer until someone empties the bucket by hand.
> For buckets that hold logs, build artifacts or test data, the lifecycle
> users want is the one Terraform's `aws_s3_bucket.force_destroy` gives.
> community#1393 asked for this and was closed as stale (2025-09-16) with
> no pull request.
>
> **Describe the solution you'd like**
> An opt-in, as a spec field (`spec.forceDestroy`) or an annotation
> (`s3.services.k8s.aws/force-destroy: "true"`), honoured only when the
> deletion policy is `delete`. An `sdk_delete_pre_build_request` hook
> that, when it is set, lists every object version and delete marker
> (`ListObjectVersions`, paged), deletes them with `DeleteObjects` (1,000
> a call, with `BypassGovernanceRetention`, as Terraform does), aborts
> every multipart upload (`ListMultipartUploads`, `AbortMultipartUpload`),
> then lets `DeleteBucket` run; an object it cannot delete (compliance
> mode Object Lock, a legal hold) is a terminal-looking error that names
> it. The controller's recommended policy would need
> `s3:ListBucketVersions`, `s3:DeleteObjectVersion`,
> `s3:ListBucketMultipartUploads`, `s3:AbortMultipartUpload` and
> `s3:BypassGovernanceRetention`.
>
> **Describe alternatives you've considered**
> A controller beside s3-controller that holds its own finalizer on
> annotated `Bucket`s and empties the bucket until s3-controller's
> `DeleteBucket` succeeds. We run that today; it works, but the delete
> belongs in the controller that owns the bucket.
>
> **Additional context**
> s3-controller `v1.16.0`: `Bucket` has no delete hook.

## 5. EC2 `Instance`: five `RunInstances` fields off `ignore`

**Title:** ec2-controller: expose `NetworkInterfaces[].Groups`, `NetworkInterfaces[].PrimaryIpv6`, `EnablePrimaryIpv6`, `Placement.GroupId` and `CpuOptions.AmdSevSnp` on `Instance`

**Labels:** `kind/enhancement`, `service/ec2`

**Body:**

> **Is your feature request related to a problem?**
> These `RunInstancesInput` fields are in `generator.yaml`'s
> `ignore.field_paths` (ec2-controller `v1.24.0`), so an `Instance` cannot:
> put security groups on a network interface it defines (community#2765),
> give that interface a primary IPv6 address, enable a primary IPv6
> address, place itself in a placement group by ID (only `groupName` is
> there), or enable AMD SEV-SNP (`cpuOptions` has `coreCount`,
> `threadsPerCore` and `nestedVirtualization`). Terraform's
> `aws_instance` has all five (`network_interface`'s security groups,
> `enable_primary_ipv6`, `placement_group_id`, `cpu_options.amd_sev_snp`).
>
> **Describe the solution you'd like**
> Take the five paths off `ignore`, with a `references` entry from
> `NetworkInterfaces[].Groups` to `SecurityGroup` (`Status.ID`), as
> PR #406 did for `SecurityGroupIds`. All five are create-time (none can
> change on a running instance), so they would be immutable.

## 6. RDS `DBInstance`: self-managed Active Directory fields

**Title:** rds-controller: self-managed Active Directory on `DBInstance` (`DomainFqdn`, `DomainOu`, `DomainAuthSecretArn`, `DomainDnsIps`)

**Labels:** `kind/enhancement`, `service/rds`

**Body:**

> **Is your feature request related to a problem?**
> `DBInstance` joins an AWS Managed Microsoft AD (`domain`,
> `domainIAMRoleName`), but the fields for a self-managed directory are in
> `ignore.field_paths` on create and on both restores (rds-controller
> `v1.15.0`). SQL Server and Oracle instances joined to a customer's own
> directory cannot be made through ACK.
>
> **Describe the solution you'd like**
> Take `DomainFqdn`, `DomainOu`, `DomainAuthSecretArn` and `DomainDnsIps`
> off `ignore` on `CreateDBInstance`, the restores and `ModifyDBInstance`
> (which takes them too), with a `references` entry from
> `domainAuthSecretARN` to secretsmanager-controller's `Secret`, and read
> them back from `DomainMemberships`.

## 7. RDS `DBInstance`: point-in-time restore

**Title:** rds-controller: restore a `DBInstance` to a point in time (`RestoreDBInstanceToPointInTime`)

**Labels:** `kind/enhancement`, `service/rds`

**Body:**

> **Is your feature request related to a problem?**
> `DBInstance` can be restored from a snapshot (`dbSnapshotIdentifier`)
> but not to a point in time; `DBCluster` can (`restoreToTime`,
> `useLatestRestorableTime`, since community#1665). Recovering an instance
> to just before a bad change is the most common restore.
>
> **Describe the solution you'd like**
> A branch of the custom create, as `DBCluster`'s, chosen by
> `restoreTime` or `useLatestRestorableTime` (not by
> `sourceDBInstanceIdentifier`, which already means a read replica), with
> the source as `sourceDBInstanceAutomatedBackupsARN` or a new
> `restoreSourceDBInstanceIdentifier`, calling
> `RestoreDBInstanceToPointInTime`.

## 8. RDS `DBInstance`: restore from S3

**Title:** rds-controller: create a `DBInstance` from a backup in S3 (`RestoreDBInstanceFromS3`)

**Labels:** `kind/enhancement`, `service/rds`

**Body:**

> **Is your feature request related to a problem?**
> Migrating a MySQL database into RDS from a Percona XtraBackup in S3
> (`RestoreDBInstanceFromS3`) cannot be done through `DBInstance`.
>
> **Describe the solution you'd like**
> The same shape as point-in-time restore: a branch of the custom create,
> chosen by `s3BucketName`, with `s3Prefix`, `s3IngestionRoleARN`
> (a reference to iam-controller's `Role`), `sourceEngine` and
> `sourceEngineVersion`, all immutable.

## 9. RDS blue/green deployments

community#1992 (open, 2026-05-14) asks for this already. Nothing new to
file; if the person filing wants to add weight, a short comment:

> We need this too, as a kind of its own (`BlueGreenDeployment`: source,
> target engine version and parameter group, and a switchover field that
> calls `SwitchoverBlueGreenDeployment`), not as a field of `DBInstance`.

## 10. ACM `Certificate`: synced only once `ISSUED`

**Title:** acm-controller: `Certificate` should not be `ResourceSynced` while `PENDING_VALIDATION` (opt-in)

**Labels:** `kind/enhancement`, `service/acm`

**Body:**

> **Is your feature request related to a problem?**
> A `Certificate` is `ACK.ResourceSynced=True` as soon as it is requested,
> while `status.status` is `PENDING_VALIDATION`. Anything that waits for
> synced (a Helm or Argo CD health check, a `kubectl wait`, a load
> balancer listener that references it) goes ahead with a certificate
> that cannot be used yet. Terraform separates the two with
> `aws_acm_certificate_validation`.
>
> **Describe the solution you'd like**
> A `synced` block for `Certificate` in `generator.yaml`:
> `synced: when: [{path: Status.Status, in: [ISSUED]}]`, opt-in (an
> annotation or a field such as `spec.waitForIssued`) so current users are
> not held.
>
> **Describe alternatives you've considered**
> A separate small kind that references the certificate and is ready when
> it is `ISSUED`.

## 11. ACM `Certificate`: `validationMethod`

**Title:** acm-controller: allow `ValidationMethod: EMAIL` on `Certificate`

**Labels:** `kind/enhancement`, `service/acm`

**Body:**

> **Is your feature request related to a problem?**
> `RequestCertificateInput.ValidationMethod` is in `ignore.field_paths`
> and the create hook sets `DNS` ("We only support DNS-based
> validation"). A domain whose DNS is not in Route 53 or not writable by
> the team cannot get a certificate through ACK.
>
> **Describe the solution you'd like**
> Take `ValidationMethod` and `DomainValidationOptions[].ValidationDomain`
> off `ignore`; the hook defaults `ValidationMethod` to `DNS` when it is
> not set, so nothing changes for current users.

## 12. Route 53 `RecordSet`: overwrite on create

**Title:** route53-controller: `allowOverwrite` on `RecordSet` (create with `UPSERT`)

**Labels:** `kind/enhancement`, `service/route53`

**Body:**

> **Is your feature request related to a problem?**
> A `RecordSet` is created with `ChangeResourceRecordSets` action
> `CREATE`, which fails when a record of that name and type exists. Taking
> over an existing record (a migration, or a record a previous tool made)
> needs the adoption path, which does not work for `RecordSet` until
> route53-controller PR #121 lands (community#2981). Terraform has
> `aws_route53_record.allow_overwrite`.
>
> **Describe the solution you'd like**
> A field `spec.allowOverwrite` (boolean, default false); when true,
> `sdk_create_post_build_request` sends `UPSERT` instead of `CREATE`.

---

## 13. moto: `RemoveRoleFromDBCluster` and `RemoveRoleFromDBInstance`

The builder's answer to `docs/plan.md` open question 6 (2026-11-15, item
ACKQ): contribute the two operations to moto and leave the stand-in until
the pin moves to a release that has them. A person opens the pull request;
this is the draft.

**Where:** a pull request to `getmoto/moto` (Apache-2.0), `moto/rds/`.

**Title:** RDS: implement RemoveRoleFromDBCluster and RemoveRoleFromDBInstance

**Body:**

> moto implements `AddRoleToDBCluster` and `AddRoleToDBInstance`, and
> reports the roles in `DescribeDBClusters`/`DescribeDBInstances`
> (`AssociatedRoles`), but not their inverses, so a client that removes a
> role gets `NotImplementedError`. This adds both:
>
> - `RemoveRoleFromDBCluster(DBClusterIdentifier, RoleArn, FeatureName)`:
>   removes the role from the cluster's associated roles;
>   `DBClusterNotFoundFault` for an unknown cluster,
>   `DBClusterRoleNotFound` when the role is not associated.
> - `RemoveRoleFromDBInstance(DBInstanceIdentifier, RoleArn, FeatureName)`:
>   the same for an instance; `DBInstanceNotFound`,
>   `DBInstanceRoleNotFound`.
>
> Both return an empty result, as AWS does. Tests in
> `tests/test_rds/` add a role, remove it, check `AssociatedRoles` is empty,
> and check both faults.

Before filing, check moto's latest release and `master` for the two
operations (they were absent from the release `hack/moto.sh` pins when
question 6 was written, `docs/plan.md` section 4.4).

## 15. AWS Emulator: the 14 operations behind the ACK-kind failures

The builder's answer to item ACKF18 (2026-11-15: "as recommended (Defer
until the other foundation items are built; then Macie and Inspector
first on the fake AWS.)"; the item also recommended that agents propose
upstream the 14 operations the AWS Emulator lacks, as drafts the builder
files, with moto the test of record until they land). The gaps are those
of [`aws-emulator.md`](aws-emulator.md), "The 14 failures" (release
1.0.338, read 2026-11-12). A person files each one, from their own
account; nothing here has been sent.

**Where:** pull requests to `aws/aws-emulator` (CC0), one per service so
each is small to review; the code lives in the service's crate
(`services/service-com-amazonaws-<service>-*/src/`). Where the
repository already has a file for the gap in
`issues/things-that-do-not-work/`, the pull request names it and removes
it (its convention for a fixed gap); otherwise it adds the implementation
and a test, with no issue file. Read its `CONTRIBUTING` and `main` first:
a later release may already have the operation (recheck with
`AWS_EMULATOR_VERSION=<new> hack/aws-emulator.sh` and the expected-failure
list).

| # | Service | Operation | Our test | Its issue file (1.0.338) |
| --- | --- | --- | --- | --- |
| 1 | Auto Scaling | `DeletePolicy` (a scaffold) | `TestScalingPolicy` | `20261111T072954Z-autoscaling-deletepolicy-stub` |
| 2 | Auto Scaling | `DescribeScheduledActions` (501) | `TestScheduledAction` | none |
| 3 | EC2 | `DescribeTags` (501) | `TestResourceTag` | `20261022T235108Z-ec2-describetags-stub` |
| 4 | EC2 | `CreateVpnConnection` (a stub), with `DescribeVpnConnections`' options and tunnels | `TestVPNConnection` | `20261023T102725Z-ec2-createvpnconnection-stub` |
| 5 | EC2 | `CreateTags` on a network ACL (`InvalidNetworkAclID.NotFound`) | `TestDefaultNetworkACL` | not looked for |
| 6 | EventBridge | `CreateConnection`'s managed Secrets Manager secret | `TestConnection` | none (a related one: `20261029T235600Z-events-connection-lifecycle-never-authorizes-connectionstate-stays-creating`) |
| 7 | IAM | `DeleteAccountPasswordPolicy` (the policy still reads back) | `TestAccountPasswordPolicy` | `20261111T010549Z-iam-getaccountpasswordpolicy-answers-200-with-a-policy-after` |
| 8 | IAM | `UpdateSSHPublicKey` (a scaffold) | `TestSSHPublicKey` | `20261112T141524Z-iam-updatesshpublickey-stub` |
| 9 | IAM | `DeleteVirtualMFADevice` (a scaffold) | `TestIAMVirtualMFADevice` | `20261105T054223Z-iam-deletevirtualmfadevice-stub` |
| 10 | KMS | `PutKeyPolicy`/`GetKeyPolicy` on a replica key in its own Region | `TestReplicaKey` | none; isolate before filing |
| 11 | Organizations | `ListAWSServiceAccessForOrganization` (a scaffold) | `TestOrganizationsOrganization` | none |
| 12 | Organizations | `EnablePolicyType`/`DisablePolicyType` refuse valid input | `TestOrganizationsOrganization` | none |
| 13 | Organizations | `CreatePolicy` (with `UpdatePolicy`, `AttachPolicy`, `DetachPolicy`) | `TestOrganizationsPolicy`, `TestOrganizationsPolicyAttachment` | none |
| 14 | Organizations | `CreateAccount` (with `DescribeCreateAccountStatus`) | `TestOrganizationsDelegatedAdministrator`, `TestOrganizationsAccountPlacement` | none |

### Auto Scaling: `DeletePolicy` and `DescribeScheduledActions`

**Title:** autoscaling: implement DeletePolicy and DescribeScheduledActions

**Body:**

> `DeletePolicy` is scaffolded, so a policy made with `PutScalingPolicy`
> is never removed and `DescribePolicies` keeps returning it.
> `DescribeScheduledActions` answers 501, so a scheduled action made with
> `PutScheduledUpdateGroupAction` cannot be read back. This implements
> both, as the Auto Scaling API reference describes them:
>
> - `DeletePolicy(AutoScalingGroupName, PolicyName)`: removes the policy
>   (by name or ARN); `ValidationError` for an unknown group; deleting an
>   unknown policy succeeds, as AWS does.
> - `DescribeScheduledActions(AutoScalingGroupName, ScheduledActionNames,
>   StartTime, EndTime, MaxRecords, NextToken)`: the group's actions,
>   filtered by name and time, paginated.
>
> Removes `issues/things-that-do-not-work/20261111T072954Z-autoscaling-deletepolicy-stub`
> (and `20261111T093414Z-autoscaling-cloudwatch-ec2-deletepolicy-answers-http-501-notimplemented`,
> for Auto Scaling's part). Tests: put, describe and delete a policy;
> put, describe (with each filter) and delete a scheduled action.

### EC2: `DescribeTags`, `CreateVpnConnection`, tags on network ACLs

**Title:** ec2: implement DescribeTags and CreateVpnConnection; tag network ACLs

**Body:**

> Three gaps a Kubernetes controller test suite met on 1.0.338:
>
> - `DescribeTags` answers 501. It should list the tags of every
>   taggable resource, with the `resource-id`, `resource-type`, `key` and
>   `value` filters and pagination.
> - `CreateVpnConnection` is a stub. It should make a connection between
>   a customer gateway and a virtual private gateway or transit gateway,
>   with its `Options` (static routes only, tunnel options, the inside
>   CIDRs and pre-shared keys given or generated), two tunnels in
>   `VgwTelemetry`, the customer gateway configuration, and state
>   `pending` then `available`; `DescribeVpnConnections` returns all of
>   it, and `DeleteVpnConnection` passes through `deleting`.
> - `CreateTags` on the default network ACL that `DescribeNetworkAcls`
>   just returned answers `InvalidNetworkAclID.NotFound`; network ACLs are
>   taggable, so the tag should be stored and returned on the ACL.
>
> Removes `20261022T235108Z-ec2-describetags-stub` and
> `20261023T102725Z-ec2-createvpnconnection-stub`. Tests for each.

### EventBridge: the connection's managed secret

**Title:** events: CreateConnection stores its credentials in a managed Secrets Manager secret

**Body:**

> `CreateConnection` returns a `SecretArn`, but no secret exists there:
> `GetSecretValue` on it answers `ResourceNotFoundException`. AWS stores
> the connection's authorization parameters in a secret it manages
> (named `events!connection/<name>/<id>`), updates it on
> `UpdateConnection` and deletes it with the connection. This makes the
> secret through the Secrets Manager state, so a client can read it, and
> removes it on `DeleteConnection`. Related, not the same:
> `20261029T235600Z-events-connection-lifecycle-never-authorizes-connectionstate-stays-creating`.

### IAM: the password policy's delete, `UpdateSSHPublicKey`, `DeleteVirtualMFADevice`

**Title:** iam: implement DeleteAccountPasswordPolicy, UpdateSSHPublicKey and DeleteVirtualMFADevice

**Body:**

> - After `DeleteAccountPasswordPolicy`, `GetAccountPasswordPolicy` still
>   answers 200 with the policy. It should answer `NoSuchEntity`.
> - `UpdateSSHPublicKey(UserName, SSHPublicKeyId, Status)` is
>   scaffolded; it should set the key's status (`Active`/`Inactive`),
>   which `GetSSHPublicKey` and `ListSSHPublicKeys` then report.
> - `DeleteVirtualMFADevice(SerialNumber)` is scaffolded; it should remove
>   the device, `DeleteConflict` while it is still enabled for a user.
>
> Removes `20261111T010549Z-iam-getaccountpasswordpolicy-answers-200-with-a-policy-after`,
> `20261112T141524Z-iam-updatesshpublickey-stub` and
> `20261105T054223Z-iam-deletevirtualmfadevice-stub`. Tests for each.

### KMS: a replica key's policy (isolate first)

Not ready to file. `ReplicateKey` works on 1.0.338, but the replica's key
policy, put in its own Region, never reads back as the one put
(`tests/e2e/kms_test.go:165`). Before filing: reproduce with two calls
(`PutKeyPolicy` then `GetKeyPolicy`, both against the replica's Region)
outside our harness. If it reproduces:

**Title:** kms: PutKeyPolicy on a replica key is not returned by GetKeyPolicy

**Body:**

> After `ReplicateKey` into another Region, `PutKeyPolicy` on the replica
> (in that Region) succeeds, but `GetKeyPolicy` there returns the policy
> the replica was made with. A replica's key policy is its own (AWS:
> "each replica key has its own key policy"), so the put should be
> returned.

### Organizations: four operations

**Title:** organizations: implement CreatePolicy, CreateAccount and ListAWSServiceAccessForOrganization; accept valid policy types

**Body:**

> Organizations is the largest gap a Kubernetes controller test suite met
> on 1.0.338 (five of its tests):
>
> - `CreatePolicy` gives no policy (the response has no `PolicyId`; 501
>   on 1.0.322). It should store the policy (name, description, type,
>   content, tags) and support `DescribePolicy`, `UpdatePolicy`,
>   `DeletePolicy`, `ListPolicies`, `AttachPolicy`, `DetachPolicy` and
>   `ListPoliciesForTarget`, with `PolicyTypeNotEnabledException` when
>   the policy's type is not enabled on the root.
> - `CreateAccount` is scaffolded. It should return a
>   `CreateAccountStatus` that `DescribeCreateAccountStatus` moves to
>   `SUCCEEDED` with the new account's ID, the account then in
>   `ListAccounts` under the root.
> - `ListAWSServiceAccessForOrganization` is scaffolded; it should list
>   the principals `EnableAWSServiceAccess` enabled.
> - `EnablePolicyType` and `DisablePolicyType` refuse valid input ("1
>   validation error") for `RootId` and `PolicyType` values that AWS
>   accepts (`r-` roots, `SERVICE_CONTROL_POLICY`, `TAG_POLICY`).
>
> Tests for each. Related: the account starts in an organization
> (`20261104T063936Z-account-organizations-describeorganization-reports-an-organization-on-a-freshly`),
> which is a separate question and not changed here.

Until these land in a release `hack/aws-emulator.sh` pins, moto stays the
test of record for these tests (`task check`); the emulator run is read
for the tests it passes.
