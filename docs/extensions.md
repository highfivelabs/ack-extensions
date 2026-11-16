# Extending ACK's own kinds

The builder's answer to `docs/plan.md` open question 1 (2026-11-13: "extend
in ack-extensions with a note to upstream"): a field one of ACK's kinds
lacks is filled here, without waiting for ACK's release, and each one is
paired with a note to upstream ([`upstream-notes.md`](upstream-notes.md),
drafts; this repository files nothing upstream).

## The shape: an annotation on ACK's object, a controller beside ACK's

ACK's kind, its CRD, its controller and its status stay ACK's. The missing
field is an **annotation in our group** on ACK's own object, and **our
manager beside ACK's controller** (the operator runs it with ACK's
controller's arguments and identity, `docs/plan.md` section 7) acts on it:

```yaml
apiVersion: kms.services.k8s.aws/v1alpha1     # ACK's Key, unchanged
kind: Key
metadata:
  name: app
  annotations:
    kms.aws.hfl.dev/rotation-period-in-days: "180"   # ours
    kms.aws.hfl.dev/enabled: "true"                  # ours
    kms.services.k8s.aws/pending-window-in-days: "30" # ACK's own
spec:
  enableKeyRotation: true
```

Why this shape, and not the other two `docs/plan.md` section 5 named:

- **A copy of ACK's kind in our group** (`kms.aws.hfl.dev` `Key`) would be
  a second kind for one AWS resource: every chart and user would choose
  between ours and ACK's, two controllers could manage one key, and
  retiring ours when ACK ships the field means migrating every object
  (section 3, "Retiring a kind"). It would also copy all of ACK's hooks
  for that kind (s3-controller's `Bucket` has about twenty).
- **A patch to ACK's controller** would mean building and shipping our own
  image of ACK's controller: a fork, which this repository does not do
  (section 2, "What is not forked").
- **An annotation** is how ACK itself carries what its spec lacks (its
  `kms.services.k8s.aws/pending-window-in-days`, `services.k8s.aws/deletion-policy`,
  `services.k8s.aws/region`). A chart sets it on the object it already
  renders; when ACK ships the field, the chart moves the value from the
  annotation to the spec and our controller is retired, with no object
  migrated.

What an annotation cannot do: it is not validated by ACK's CRD (our
controller checks it and records a Warning Event, `InvalidAnnotation`),
and our controller cannot write ACK's status, so what it did is recorded
as Events on the object. It also cannot change what ACK's own create call
sends: a gap that is a **create-time parameter** of ACK's create (an
instance's network interface groups, a database restored to a point in
time, a certificate validated by email) is not filled this way. Those are
listed under "Not filled here" below, and `docs/plan.md` open question 23
asks how the builder wants them filled.

**Answered, 2026-11-15** (item ACKQ1-1; `docs/plan.md` section 3, "Fixed
copies of ACK's kinds"): the annotation stays the first choice where it
can fill the gap. Where it cannot, and ACK's kind is broken or misses
attributes, we ship the identical kind with the fix in our group, as an
intentional overlap with ACK's, never a copy without a known missing or
broken bit. The `Instance` and ACM validation-method fields wait for
upstream; the first fixed copy is s3-controller's `Bucket` (its read
panics on a bucket without default encryption).

## How it is built

- `pkg/extend`: a registry. A service's hand-written code registers an
  extension in an `init` function; the generated `main.go` calls
  `extend.Setup(mgr, sc, ackCfg)` once ACK's service controller is bound
  (`patches/code-generator/0004-extend-ack-kinds.patch`, ours alone). An
  extension gets the manager, ACK's service controller (so its AWS
  configuration is made by ACK's runtime, with ACK's endpoint, credentials
  and HTTP settings) and ACK's flags (region, deletion policy, resync).
- An extension is a plain controller-runtime controller on ACK's kind in
  `services/<service>/pkg/extend/<kind>/`, with its RBAC markers there
  (`hack/generate.sh` reads `services/<service>/pkg/...`) and its AWS calls
  read into the environment's IAM role by `pkg/ackiam` like any kind's
  (`task ack:iam LIST=1` lists them as `Key.kms`, `Bucket.s3`).
- It waits until ACK has made the resource (ACK's `status.ackResourceMetadata`),
  acts only in the manager's own account (an object ACK reached through a
  cross-account role map, CARM, gets a Warning Event and is left alone),
  changes nothing for a read-only object (`services.k8s.aws/read-only`),
  and rereads AWS on ACK's resync period, putting back a change made
  outside Kubernetes.

## What is filled

| ACK kind | Annotation (ours) | What it does | AWS calls | Terraform | Proven |
| --- | --- | --- | --- | --- | --- |
| kms `Key` | `kms.aws.hfl.dev/rotation-period-in-days` (90 to 2560) | the automatic rotation period, while ACK's `spec.enableKeyRotation` is true (ACK turns rotation on and off; this sets its period) | `GetKeyRotationStatus`, `EnableKeyRotation` with `RotationPeriodInDays` | `rotation_period_in_days` | on moto with ACK's own kms-controller (`TestKeyExtension`), except the period itself: moto takes it and never returns it (`docs/plan.md` section 4.4), so the request's shape is shown by the unit tests (`services/kms/pkg/extend/key`) |
| kms `Key` | `kms.aws.hfl.dev/enabled` (`true`/`false`) | the key enabled or disabled; without it the key's state is left as it is | `DescribeKey`, `EnableKey`, `DisableKey` | `is_enabled` | on moto with ACK's kms-controller: disable, enable, a key disabled outside Kubernetes enabled again |
| kms `Key` | none: ACK's own `kms.services.k8s.aws/pending-window-in-days` (7 to 30, default 7) | the deletion window | ACK's `ScheduleKeyDeletion` | `deletion_window_in_days` | on moto with ACK's kms-controller: a 30-day window reaches `ScheduleKeyDeletion` |
| s3 `Bucket` | `s3.aws.hfl.dev/force-destroy` (`true`/`false`) | empties the bucket when the Bucket is deleted, so ACK's `DeleteBucket` succeeds | `GetObjectLockConfiguration`, `ListObjectVersions`, `DeleteObjects` (with `BypassGovernanceRetention` only when the bucket has Object Lock enabled: real S3 refuses it otherwise), `ListMultipartUploads`, `AbortMultipartUpload` | `force_destroy` | on moto (`TestBucketExtension`), the test playing ACK's side of the delete (s3-controller is not a Go module here); paging and batches by unit test; on our fixed copy of ACK's `Bucket` (`s3.aws.hfl.dev`, `docs/plan.md` section 6.38) by `TestBucket`, with our runtime's own delete |

### The `Bucket`'s delete, step by step

1. While the annotation is `"true"`, our controller holds its own
   finalizer, `s3.aws.hfl.dev/force-destroy`, on ACK's `Bucket` (and drops
   it when the annotation goes).
2. The `Bucket` is deleted. ACK's controller calls `DeleteBucket`, which
   fails with `BucketNotEmpty` while objects remain, and retries.
3. Our controller checks that ACK will delete the bucket: the deletion
   policy (the object's `services.k8s.aws/deletion-policy`, then the
   namespace's `s3.services.k8s.aws/deletion-policy`, then the
   controller's `--deletion-policy`, as ACK's reconciler reads it) is not
   `retain`; the object is not read-only; ACK made or adopted the bucket;
   it is in the manager's own account. If any is not so, it drops its
   finalizer and empties nothing (with an Event saying why).
4. It deletes every object version and delete marker, 1,000 a call, and
   aborts every multipart upload, and repeats every five seconds until
   ACK's finalizer (`finalizers.s3.services.k8s.aws/Bucket`) is gone,
   that is until ACK's `DeleteBucket` has succeeded; an object written
   meanwhile is deleted on the next pass. Then it drops its own finalizer.
5. An object version that cannot be deleted (compliance-mode Object Lock,
   a legal hold) is a Warning Event, `BucketEmptyFailed`, and is retried;
   the `Bucket` stays until it can be deleted, as with Terraform.

The S3 manager is a new service here, `services/s3`, with no kind of its
own: the operator runs it beside ACK's s3-controller (`MANAGER_IMAGE_S3`),
and an `ACKContext` lists it as `s3`.

## Not filled here

The rest of `docs/plan.md` section 5's "Gaps on ACK's own kinds", and why
each is not an annotation here (each has its upstream note in
[`upstream-notes.md`](upstream-notes.md)):

| ACK kind | Gap | Why not here |
| --- | --- | --- |
| ec2 `Instance` | `securityGroupRefs` | merged upstream (ec2-controller #406); comes with `v1.25.0`, when the catalog moves its pin |
| ec2 `Instance` | `networkInterfaces[].groups`, `networkInterfaces[].primaryIPv6`, `enablePrimaryIPv6`, `placement.groupID`, `cpuOptions.amdSevSnp` | create-time parameters of `RunInstances` (open question 23) |
| rds `DBInstance` | self-managed Active Directory (`domainFqdn`, `domainOu`, `domainAuthSecretARN`, `domainDNSIPs`) | `ModifyDBInstance` takes them too, so an extension could set them after ACK's create, but ACK's `spec.domain` is read back from the instance's domain membership and would fight it; needs care and a moto that has domains (it has none) |
| rds `DBInstance` | point-in-time restore; restore from S3 | create-time: a different create call (open question 23) |
| rds | blue/green deployments | a kind of its own, not a field (an operation with a switchover); moto has none of it |
| acm `Certificate` | synced only once `ISSUED` | ACK's condition is ACK's; the fallback is a small kind here (`CertificateValidation`, ready when `ISSUED`), to build if the note upstream is declined |
| acm `Certificate` | `validationMethod` `EMAIL` | create-time: ACK's create hook sets `DNS` (open question 23) |
| route53 `RecordSet` | overwrite on create (`allowOverwrite`) | ACK's create sends `CREATE`; an extension cannot turn it into `UPSERT` without racing ACK's create. ACK's `adopt-or-create` annotation is the way once route53-controller #121 lands |
| ours: ec2 `VolumeAttachment`, `Volume` | `instanceRef` | ours; waits on open question 16 |

### The catalog's eks chart (catalog PR #237): the field gaps, 2026-11-16

What the chart refused as "fields ACK's kinds lack", read against
eks-controller `v1.26.0`, kms-controller `v1.7.0` and
cloudwatchlogs-controller `v1.10.0`, and where each is filled. Two are
filled already; the rest need a fixed copy (`docs/plan.md` section 3) of a
kind whose controller is not built here yet, and wait for a chart to need
them first (the order below).

| ACK kind | Gap (Terraform variable) | Where it is filled |
| --- | --- | --- |
| kms `Key` | the deletion window (`kms_key_deletion_window_in_days`) | **filled**: ACK's own annotation `kms.services.k8s.aws/pending-window-in-days` (7 to 30); the chart can carry it now |
| kms `Key` | the rotation period (`kms_key_rotation_period_in_days`) | **filled**: our annotation `kms.aws.hfl.dev/rotation-period-in-days` (above); the chart can carry it now |
| eks `Cluster` | `service_ipv6_cidr` | **nothing to fill**: EKS assigns an IPv6 cluster's service range (`CreateCluster` takes no such field); ACK's `status` reads it (`kubernetesNetworkConfig.serviceIPv6CIDR`), so the chart can give it as an output, not take it as an input |
| eks `Nodegroup` | `node_repair_config` | an annotation here would do (`UpdateNodegroupConfig` takes it after ACK's create; ACK ignores the field on its reads, so nothing fights it), but eks is not a service here yet, and every eks-controller release needs aws-sdk-go-v2 `v1.45.1` or later (`docs/plan.md` section 3) |
| eks `Nodegroup` | `warm_pool_config` | the same, on a newer model still (ACK ignores it since its `v1.91.0` model bump) |
| eks `Addon` | `namespace_config` | create-time (`CreateAddon` only): a fixed copy of ACK's `Addon` |
| eks `Cluster` | `force_update_version` | an update-time flag of ACK's own `UpdateClusterVersion` call: a fixed copy of ACK's `Cluster` |
| cloudwatchlogs `LogGroup` | `log_group_class` | create-time (`CreateLogGroup` only): a fixed copy of ACK's `LogGroup` |

The eks rows wait on the SDK question: an eks service here built from
eks-controller (an annotation beside it, or a fixed copy) needs the core
aws-sdk-go-v2 moved past `v1.41.5`, which
`docs/plan.md` section 3 keeps at ACK's controllers' version. That move is
a pin change for the whole repository, done with an ACK release move, not
for one field.

