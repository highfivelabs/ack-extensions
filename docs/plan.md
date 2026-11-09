# ack-extensions plan

Status, 2026-11-09: the repository has seventeen kinds:

- `applicationautoscaling.aws.hfl.dev` `ScheduledAction`;
- `autoscaling.aws.hfl.dev` `ScalingPolicy` and `ScheduledAction`;
- `ec2.aws.hfl.dev` `CustomerGateway`, `VPNGateway`, `Volume` and
  `VolumeAttachment`;
- `ecr.aws.hfl.dev` `ReplicationConfiguration`;
- `eventbridge.aws.hfl.dev` `Connection` and `APIDestination`;
- `iam.aws.hfl.dev` `RolePolicyAttachment` and `AccountAlias`;
- `kms.aws.hfl.dev` `ReplicaKey`;
- `lambda.aws.hfl.dev` `Permission`;
- `rds.aws.hfl.dev` `OptionGroup`;
- `rds.aws.hfl.dev` `DBProxyTarget`;
- `rds.aws.hfl.dev` `DBRoleAssociation`.

There is an operator that runs our managers beside ACK's controllers as
the same AWS identity, and tests against a fake AWS (moto), in Go and on
kind (section 8). There is no image registry, release or remote yet.
This document says how it is built, why, how it stays in step with ACK,
how it is tested, which gaps come next, and what is left.

## 1. How ACK is built

- **One controller per AWS service, one repository each.** Examples are
  `aws-controllers-k8s/rds-controller` and `iam-controller`; there are 80
  or so. Each is its own Go module, released on its own, with its own
  Helm chart (`<service>-chart`), image, ServiceAccount
  (`ack-<service>-controller`) and IAM role.
- **Generated from the AWS SDK's model.** `aws-controllers-k8s/code-generator`
  (`ack-generate`) reads the service's Smithy model from `aws-sdk-go-v2`
  (`codegen/sdk-codegen/aws-models/<service>.json`, at a pinned SDK tag)
  and a `generator.yaml`. From them it writes:
  - the API types (`apis/v1alpha1`);
  - the resource managers (`pkg/resource/<resource>/{sdk,delta,manager,...}.go`);
  - the controller's `main.go`;
  - RBAC and the Helm chart.

  A resource is found from `Create<X>`/`Describe<X>`/`Delete<X>`
  operations; `generator.yaml` renames fields, marks them immutable or
  read-only, ignores resources and shapes, and points at **hook
  templates** (`templates/hooks/...`) that splice hand-written code into
  the generated paths. Hand-written code lives beside it (`hooks.go`).
- **One shared runtime.** `aws-controllers-k8s/runtime` is a Go module.
  Every controller links it, and it holds:
  - the reconciler and the resource-manager interfaces;
  - references between kinds, adoption and field exports;
  - region and account selection: the annotations, the cross-account role
    maps (CARM) and `IAMRoleSelector`;
  - the AWS credential chain and the flags every controller shares.
- **Tests.** Generated code has unit tests with mocked SDK clients. The
  end-to-end tests are Python (`test/e2e`) and run against real AWS
  accounts in ACK's Prow infrastructure (`test-infra`). There is no fake
  AWS upstream.

## 2. How a gap kind is built here

**Our kinds live in our groups**, `<service>.aws.hfl.dev`, reconciled by
our process, never by ACK's. ACK's controllers only reconcile the kinds
compiled into them, so a gap kind in `rds.services.k8s.aws` would never
be picked up by ACK's rds-controller. It would also clash with ACK's CRD
the day ACK ships the same kind.

**One module, one directory per service**, `services/<service>/`, laid
out as an ACK controller repository is:

| Path | What | Generated? |
| --- | --- | --- |
| `generator.yaml`, `metadata.yaml`, `templates/hooks/` | the generator's input: only the kinds ACK lacks; every resource ACK already has is ignored | no |
| `apis/v1alpha1/` | API types | yes, plus hand-written kinds |
| `pkg/resource/<kind>/` | resource managers | yes, plus hand-written `hooks.go` and hand-written kinds |
| `cmd/controller/main.go` | the service's manager | yes |
| `cmd/controller/handwritten.go` | registers the hand-written kinds and the ACK kinds they reference | no |

Two ways to build a kind:

1. **Generated**, when the AWS API has a Create/Describe/Delete shape the
   generator recognises (`OptionGroup`), or a Put/Describe/Delete shape
   mapped to one in `generator.yaml`'s `operations`, as ACK does for its
   own Put-shaped kinds (`ScalingPolicy`, and the two `ScheduledAction`s). The `generator.yaml` entry and
   hook templates are ACK's, copied from upstream where they exist, and
   the generated code is regenerated here, never copied.
2. **Hand-written on ACK's runtime interfaces**, when the API's shape is
   not one the generator reads:
   - `AttachRolePolicy`/`DetachRolePolicy` (`RolePolicyAttachment`, as
     upstream PR #184 does);
   - `RegisterDBProxyTargets`/`DeregisterDBProxyTargets` (`DBProxyTarget`);
   - `AddRoleToDBCluster`/`AddRoleToDBInstance` and their
     `RemoveRoleFrom...` (`DBRoleAssociation`);
   - `AddPermission`/`GetPolicy`/`RemovePermission` (`Permission`);
   - `ReplicateKey` in one Region and `DescribeKey`/`ScheduleKeyDeletion`
     in another (`ReplicaKey`).

   The files follow the generated layout (descriptor, resource,
   identifiers, manager, manager factory, references, delta, sdk), so the
   runtime treats them like any generated kind.

**`hack/generate.sh`** does what ACK's `scripts/build-controller.sh` does
for one controller, for each of our services:

1. `ack-generate apis` and `controller`;
2. rewrite the generated imports of ACK's own module path to ours
   (ACK's templates hard-code `github.com/aws-controllers-k8s/<service>-controller`);
3. replace every file that carries the generator's header, and leave
   hand-written files alone;
4. controller-gen v0.19.0, the version ACK pins, for deepcopy, CRDs
   (`config/crds/bases`) and the managers' RBAC (`config/rbac`);
5. gofmt and goimports.

`make check` fails if it changes anything committed. Generation is
deterministic: two runs give identical trees.

**One patch to the generator**, `patches/code-generator/0001`, adds
`--api-group-suffix`. The generator already supports another group
suffix internally (its Crossplane mode passes one), but its ACK commands
always use `services.k8s.aws`. The patch should go upstream.

**What is not forked.** ACK's runtime and the controllers whose kinds we
reference come in as Go modules at released versions, unchanged. Only the
generator is fetched as source (it is a tool, not linked), at the pin,
with the patch.

## 3. Keeping in step with ACK

- `UPSTREAM.md` holds the pins (code generator, runtime, the referenced
  controllers, the SDK model tag) and a table of every copied file with
  its source commit and change. Each copied file says so in its header.
- **Moving the pin.** ACK releases the runtime and generator together
  (`v0.67.0` both), and controllers follow within days. A move takes all
  of them to one ACK release:
  1. Update `UPSTREAM.md`'s generator commit and `go.mod`'s runtime and
     controller versions.
  2. Run `make upstream generate`, then `make check`.

  Keep the AWS SDK modules at the versions ACK's controllers use, so
  shared dependencies are the ones ACK tests with. Adding the EC2 client
  for the tests at `@latest` moved `aws-sdk-go-v2` from `v1.41.5` to
  `v1.47.8`; it is pinned to ACK ec2-controller's `v1.290.1` instead.
- **Copied from open pull requests.** PR #184 (iam-controller, head
  `78eb350e`) and PR #257 (rds-controller, head `32c3cb23`). Both are open
  and conflict with their base (`mergeable_state: dirty`). When either
  changes, re-copy from the new head. When either merges and ships in a
  release, retire ours.
- **Retiring a kind.** When ACK ships the kind or field:
  1. Mark ours deprecated.
  2. Migrate each object: re-create it in ACK's group, adopting the AWS
     resource (ACK's `services.k8s.aws/adoption-policy` with
     `adoption-fields`).
  3. Delete ours with `services.k8s.aws/deletion-policy: retain`.
  4. After a support window, remove our kind.

## 4. Testing with a fake AWS

### 4.1 The choice: moto in server mode

| Option | Verdict |
| --- | --- |
| **moto** (`getmoto/moto`, Apache-2.0), server mode | **Chosen.** Real HTTP endpoints for IAM, RDS (option groups, proxies, proxy targets, role associations except their removal), EC2 and STS, in-memory, started per test on a free port. Pinned at `5.2.5`: the pip package in `hack/moto-requirements.txt` for the Go tests, and the `motoserver/moto:5.2.5` image by digest for kind. |
| LocalStack | Rejected. The community repository is archived (2026-03), and the unified image needs a LocalStack account under its EULA, free only for non-commercial use. That is not something an open-source project's tests can depend on. |
| ACK's own approach | Not hermetic: end-to-end tests run against real AWS accounts in ACK's Prow. |
| Recorded HTTP (go-vcr) | Kept for later: recordings need a real AWS account to record once, and they do not exercise the controller's reads after its writes the way a stateful fake does. |

ACK's runtime already takes `--aws-endpoint-url` and
`--aws-identity-endpoint-url` (with `--allow-unsafe-aws-endpoint-urls`
for plain HTTP). That is how the Helm chart points a controller anywhere.
So the managers need no fake-cloud switch of our own: the tests start
the real binaries with the flags the chart passes, pointed at moto, with
the credentials `testing`/`testing`.

### 4.2 The harness

`pkg/test/harness` runs, per test:

- moto, from `.cache/moto-venv` (`hack/moto.sh`), or `MOTO_URL` to use
  one already running;
- envtest (a real kube-apiserver and etcd, Kubernetes 1.35) with our
  CRDs, ACK's core CRDs (`services.k8s.aws`) and ACK's iam and rds CRDs,
  read from the Go modules at the pinned versions;
- the service's real controller binary (`services/<service>/cmd/controller`),
  built once, started with ACK's chart arguments against moto.

The tests assert on two sides: the Kubernetes object (conditions,
status) and the fake cloud through the real AWS SDK clients.

### 4.3 Results (`make test`; latest 2026-11-09 on a Spot VM, linux amd64, Go 1.27.3)

```text
--- PASS: TestAccountAlias
    the alias is set and settles (no update across three resyncs); an
    alias set outside Kubernetes is put back; the CRD refuses what IAM
    would; the alias cannot change; deletion-policy retain leaves it;
    delete deletes it
--- PASS: TestSameAliasIsNoDrift (account alias unit test)
--- PASS: TestRolePolicyAttachment
    create attaches the policy; references resolve to ACK's Role; delete
    detaches the policy; deletion-policy retain keeps the attachment
--- PASS: TestOptionGroup
    create makes the group, then adds its options; an option taken off
    the spec is removed; the engine cannot change; delete removes the group
--- PASS: TestDBProxyTarget
    an instance is registered and synced; a cluster is registered as a
    tracked cluster; both an instance and a cluster is refused by the CRD;
    references resolve to ACK's DBProxy and DBInstance; delete deregisters
--- PASS: TestAllTargetsAvailable, TestRegisteredExactlyOne,
          TestSyncedFollowsTargetHealth (db_proxy_target unit tests)
--- PASS: TestDBRoleAssociation
    a role is associated with a cluster and synced; a role is associated
    with an instance; the CRD refuses an instance without a feature, both
    a cluster and an instance, and no role; the role cannot change;
    references resolve to ACK's DBCluster and Role; deletion-policy
    retain leaves the role associated; delete calls
    RemoveRoleFromDBCluster and keeps the object while it fails (moto
    does not implement it, section 4.4)
--- PASS: TestScalingPolicy
    a target-tracking policy is put; a changed target is put again;
    server defaults are no drift (no update across three resyncs); the
    policy name cannot change; deletion-policy retain leaves the
    policy; delete removes the policies
--- PASS: TestScheduledAction
    a recurring action is put and settles; a changed schedule is put
    again; a one-time action is put with its start time; a one-time
    action that ran (deleted from moto as AWS would) is completed, not
    put again; the action name cannot change; deletion-policy retain
    leaves the action; delete removes the actions
--- PASS: TestCompletedOneTimeAction, TestDeleteCompletedCallsNothing
          (scheduled_action unit tests, with a fake clock)
--- PASS: TestApplicationScheduledAction
    a recurring action is put and settles (no update across three
    resyncs); a changed capacity and schedule is put again; the target
    cannot change; deletion-policy retain leaves the action; delete
    removes the action
--- PASS: TestCustomerGateway
    a customer gateway is made, tagged, and available; its tags change;
    its address cannot change; deletion-policy retain leaves the gateway;
    delete deletes the gateway (listed as deleted, the object gone)
--- PASS: TestVpnGateway
    a VPN gateway is made, tagged, and attached to its VPC; it moves to
    another VPC; its ASN cannot change; deletion-policy retain leaves the
    gateway attached; delete detaches and deletes the gateway
--- PASS: TestVolumeAttachment
    the referenced volume is attached and settles (no update across
    three resyncs); the device cannot change; a detach outside
    Kubernetes is put back; deletion-policy retain leaves the volume
    attached; delete detaches the volume
--- PASS: TestAttachmentTo, TestDeviceIsCompared, TestAttachedIsSynced
          (volume attachment unit tests)
--- PASS: TestVolume
    a volume is made, tagged, and available; it grows; its zone cannot
    change; EC2's defaults are no drift (no update across three
    resyncs); deletion-policy retain leaves the volume; delete deletes
    the volume
--- PASS: TestConnection
    a connection is made with the Secret's password (read from the
    connection's managed secret in moto); it settles, the secrets read
    back as no drift (no update across three resyncs); a changed
    parameter is updated; the name cannot change; deletion-policy retain
    leaves the connection; delete deletes it
--- PASS: TestApiDestination
    an API destination is made through the referenced connection (its
    ARN from our Connection's status); a changed rate limit is updated;
    the name cannot change; deletion-policy retain leaves the
    destination; delete deletes it
--- PASS: TestDefaultRateLimitIsNoDrift, TestSetRateLimitIsCompared
          (API destination unit tests)
--- PASS: TestReplicationConfiguration
    create puts the rules; a changed rule is put again; a rule changed
    outside Kubernetes is put back; the CRD refuses what ECR would (a
    filter type ECR does not have, a registry that is not an account
    ID); another account's registry is refused, and the registry is
    unchanged; delete puts no rules; deletion-policy retain leaves them
--- PASS: TestRoundTrip, TestDeltaNoFiltersIsNoFilters,
          TestNilConfigurationIsNoRules, TestRegistryMismatch
          (replication configuration unit tests)
--- PASS: TestKeepSecretsIsNoDrift, TestKeepSecretsKeepsOtherDrift
          (connection unit tests)
--- PASS: TestReplicaKey
    a replica is made in its Region (tags put there); a policy is put on
    the replica only; the replica Region cannot change; references
    resolve to ACK's Key; deletion-policy retain leaves the replica;
    delete schedules the replica's deletion, never the primary key's
    (the primary key checked Enabled and untagged after every step)
--- PASS: TestDeleteNeverReachesThePrimaryRegion,
          TestFindReadsTheReplicaInItsRegion, TestSyncedWhenEnabled,
          TestTagsAreASet, TestThePrimaryKeyIsFoundByItsRegion,
          TestDeleteRefusesAnARNFromAnotherRegion, TestKeyIDInRegion
          (replica_key unit tests, against a stand-in
          that records each call's signed Region)
--- PASS: TestPermission
    a service may invoke the function; an account may invoke an alias
    (the spec keeps the account ID as written); the CRD refuses no
    function, a statement ID Lambda refuses and an unknown auth type; a
    statement cannot change; a statement removed outside Kubernetes is
    added back; references resolve to ACK's Function; deletion-policy
    retain leaves the statement; delete removes the statement
--- PASS: TestObservedSpec, TestFindStatementMissing, TestFillUnset,
          TestPermissionARN (permission unit tests)
--- PASS: TestAssociatedExactlyOne, TestMatchingRoleAndFeature,
          TestSyncedFollowsAssociationStatus, TestDeleteRemovesTheRole
          (db_role_association unit tests; the last against a stand-in
          for RDS's RemoveRoleFrom... responses)
--- PASS: the OptionGroup hook tests copied from PR #257
--- PASS: TestManagerArgs, TestReconcile (the operator against envtest)
```

Each end-to-end test was also run against a deliberately broken
controller to show it can fail:

- With `DetachRolePolicy` removed, the delete step failed: "policy
  detached and object gone: not within 1m0s: still attached".
- With `DBProxyTarget`'s readiness rule replaced by `true`,
  `TestSyncedFollowsTargetHealth` failed: "IsSynced with a REGISTERING
  target".
- With `DBRoleAssociation`'s delete returning before
  `RemoveRoleFromDBCluster`, the delete step failed: "the removal
  attempted and the object kept: not within 1m30s: object not kept".
- With `ReplicaKey`'s replica-Region client replaced by the manager's
  own (pull request #112's behaviour), `TestDeleteNeverReachesThePrimaryRegion`
  failed: "ScheduleKeyDeletion reached the primary key's Region
  us-west-2".
- With the completion rule's finalizer check removed,
  `TestCompletedOneTimeAction` failed: "never created by this object (no
  finalizer): completed true, want false".
- With `ScalingPolicy`'s server-defaults hook removed from its delta,
  the defaults step failed: "the policy was put again 3 times across
  resyncs: the server's defaults read as drift".
- With `Permission`'s read overwriting fields the spec sets,
  `TestFillUnset` failed: "principal rewritten to
  arn:aws:iam::210987654321:root".
- With its readiness rule replaced by `true`,
  `TestSyncedFollowsAssociationStatus` failed: "IsSynced with status
  "PENDING" = true, want false".

### 4.4 What moto does differently from AWS

- **The REGISTERING state.** moto reports a new proxy target REGISTERING
  only in the `RegisterDBProxyTargets` response, and AVAILABLE on every
  read after. ACK's runtime reads right after a create and keeps that
  status, so the not-yet-available path cannot be seen end to end. On
  AWS it lasts minutes. The rule is unit-tested instead.
- **A proxy that does not exist.** moto answers `DescribeDBProxyTargets`
  on such a proxy with a server error (a Python `KeyError`), where AWS
  returns `DBProxyNotFoundFault`. Our code handles the AWS fault; the
  tests never delete the proxy first.
- **Instance identifiers.** moto does not check them on registration
  (AWS does).
- **The target group name.** moto's `DescribeDBProxyTargets` ignores it
  (there is only `default` on AWS too).
- **Option group names.** moto lower-cases them, so the tests use lower
  case, as AWS stores them.
- **IAM.** moto does not enforce it by default, so the tests prove which
  identity a pod presents, not that AWS would authorise it.
- **Removing a role from a cluster or instance.** moto 5.2.5 implements
  `AddRoleToDBCluster` and `AddRoleToDBInstance`, but not
  `RemoveRoleFromDBCluster` or `RemoveRoleFromDBInstance`: it answers
  both with a bare HTTP 500. They are unchecked in moto's
  `IMPLEMENTATION_COVERAGE.md` on its master (2026-11-08), so a newer
  moto would not help. `TestDBRoleAssociation` shows that deleting the
  object calls `RemoveRoleFromDBCluster` and keeps the object while the
  call fails. That a successful removal, or an association already gone
  (`DBClusterRoleNotFound`, `DBInstanceRoleNotFound`, or the database
  gone), releases the object is unit-tested
  (`db_role_association/sdk_test.go`) against a stand-in serving the
  responses RDS documents, not against a fake with state.
- **A function's versions and aliases share one policy in moto.** AWS
  keeps one policy per function, version and alias. moto keeps one for the
  function and all its versions and aliases, and its `RemovePermission`
  ignores the qualifier. `TestPermission` shows that a qualified
  statement is sent with its qualifier and read back with it, not that
  the policies are separate.
- **An account principal.** moto stores `Principal: "210987654321"` as
  given; AWS stores `{"AWS": "arn:aws:iam::210987654321:root"}`. Our read
  keeps a field the spec sets as written, so neither form is drift. Both
  forms are unit-tested.
- **The event source token.** moto drops `EventSourceToken` from the
  statement, where AWS writes a `lambda:EventSourceToken` condition. Only
  the unit tests read it back.
- **EC2 Auto Scaling policies.**
  - moto keys policies by name alone, across groups (AWS keys them by
    group and name), and `DeletePolicy` ignores the group.
  - It checks neither that the group exists nor the policy's parameters.
  - It returns a default cooldown of 300 for a simple scaling policy
    (AWS returns one only when it was set), never returns `Enabled`, and
    returns an empty `PredictiveScalingConfiguration` for every policy.
  - The read treats an empty predictive configuration as none. Server
    defaults count as no drift (section 6.6). `TestScalingPolicy` shows
    the object then stays synced across resyncs.
- **EC2 Auto Scaling scheduled actions.** moto stores an action as
  given. It does not compute a recurring action's next start time, does
  not delete a one-time action after it runs, and returns no
  `ScheduledActionARN`. `TestScheduledAction` deletes a one-time action
  from moto itself once its start time passes, as AWS would.
- **Application Auto Scaling scheduled actions.** moto stores an action
  as given. It does not require a registered scalable target, does not
  check the schedule expression, and returns the time zone only when one
  was set.
- **EC2 customer gateways.** moto keeps a deleted gateway listed in
  state `deleted`, as AWS does, and creates one `available` at once (AWS
  goes through `pending`). It does not check that the address is
  public.
- **EC2 VPN gateways.** moto attaches and detaches at once (`attached`,
  `detached`; AWS goes through `attaching` and `detaching`, and refuses
  to delete a gateway until it is detached). It keeps a deleted gateway
  listed in state `deleted`, as AWS does. It does not implement
  `EnableVgwRoutePropagation`, so route propagation is not built.
- **EBS volumes.** moto creates a volume `available` at once (AWS goes
  through `creating`), applies `ModifyVolume` at once (AWS goes through
  `modifying` and `optimizing`), and removes a deleted volume at once.
  It fills in a volume type when the spec leaves it out, which
  `TestVolume` shows is no drift. It attaches and detaches at once (the
  attach answers `attaching`, the next read `attached`), takes any AMI for
  the test's instance, and does not refuse a volume already attached
  elsewhere (AWS answers `VolumeInUse`).
- **ECR replication.** moto accepts one replication rule at most ("This
  feature is disabled" for more; AWS allows ten), and does not check that
  a destination account lets this one replicate into it (on AWS, its
  registry policy must). It never replicates images.
- **EventBridge connections.** moto marks a connection `AUTHORIZED` at
  once (an OAuth connection is `AUTHORIZING` first on AWS). It stores
  the credentials in a Secrets Manager secret, as EventBridge does, which
  the test reads. It returns `AuthParameters` as given, where AWS omits
  the secrets. The read hook keeps the spec's secrets either way.
- **EventBridge API destinations.** moto marks a destination `ACTIVE` at
  once and, when the spec leaves the rate limit out, reports none (AWS
  reports 300 per second). A unit test shows that default is no drift.
- **IAM account alias.** moto replaces the alias on create, as AWS
  does, and does not check that another account already has it (AWS
  answers `EntityAlreadyExists`).
- **KMS replica keys.**
  - moto keeps a replica in its own Region's backend, under the primary
    key's key ID, as AWS does.
  - It applies no `Tags` given to `ReplicateKey`. The read then finds
    none, and the update that follows puts them; `TestReplicaKey` shows
    that path.
  - A new replica is `Enabled` at once. On AWS it is `Creating` first;
    the readiness rule is unit-tested.
  - moto's `ReplicateKey` copies the key shallowly, so the replica shares
    its primary key's `MultiRegionConfiguration`. Describing the replica
    then writes `REPLICA` into it, and from then on the primary key in its
    own Region also reports `MultiRegionKeyType: REPLICA`. The first
    end-to-end run failed on this. The read now checks that the primary
    key's `PrimaryKey.Region` is the manager's Region, which means the
    same on AWS.
  - moto's `ScheduleKeyDeletion` does not resolve a key ARN (a server
    error, `KeyError`); AWS accepts a key ID or an ARN, and refuses an ARN
    from another Region. The manager sends the replica's key ID to the
    replica's Region, after checking that the replica's ARN is in that
    Region, which stands in for AWS's check.
- **An association's status.** moto marks a new association `ACTIVE` at
  once. On AWS it is `PENDING` first, and `INVALID` when RDS cannot assume
  the role. The rule (synced only when `ACTIVE`) is unit-tested.
- **Role ARNs and features.** moto checks neither that the role exists
  nor that the engine supports the feature (AWS does both). It refuses a
  second association with the same role or the same feature, as AWS
  does.

## 5. Candidates, ranked

**The ranking (2026-11-09)** follows the usage-ranked gap list in
`highfivelabs/catalog` `docs/aws.md`, section 5. That list counts which of
the most-downloaded terraform-aws-modules (Terraform Registry downloads,
2026-11-09) need something ACK lacks, and ranks the gaps by how many of the
top 25 modules need them. Candidates from this plan's earlier tables that
the catalog does not rank follow it, from rank 19.

The rules are unchanged:

- **Upstream first.** Build only what upstream has neither released nor
  has in a pull request close to merging; otherwise wait for the release.
- **Open questions.** Skip anything blocked on an open question
  (section 9), and say so here.
- **The fake cloud.** Prove what moto can show, and list the rest as not
  proven. Where moto implements none of a kind's API, nothing could be
  proven, so it waits.

Upstream was read on 2026-11-09 with read-only `gh`: each ACK controller's
latest release and its `generator.yaml`, open pull requests, and the
`community` issues. moto support is moto's `IMPLEMENTATION_COVERAGE.md` on
its master (5.2.5 is the pin).

| Rank | Candidate | Upstream (2026-11-09) | moto | Status |
| --- | --- | --- | --- | --- |
| 1 | Lambda `Permission` (`AddPermission`/`RemovePermission`) | No PR; `community` issues #3051 and #2659 are open. lambda-controller `v1.19.0`'s `Alias.permissions` adds permissions to an alias only. | yes | built: `Permission` |
| 2 | Secrets Manager rotation on `Secret` | secretsmanager-controller PR #69 | yes | skipped: a field on an existing kind (open question 1) |
| 3 | RDS role associations | none | adds yes, removes no (section 4.4) | built: `DBRoleAssociation` |
| 4 | EC2 Auto Scaling `ScalingPolicy` and `ScheduledAction`; Application Auto Scaling `ScheduledAction` | none: autoscaling-controller `v1.5.0` has only `AutoScalingGroup`; applicationautoscaling-controller `v1.9.0` has `ScalableTarget` and `ScalingPolicy` | yes | built: `ScalingPolicy`, EC2 Auto Scaling `ScheduledAction`, Application Auto Scaling `ScheduledAction` |
| 5 | KMS `ReplicaKey` | kms-controller PR #112: open, conflicts with base, not approved; an ACK maintainer's review finds its read, update and delete act on the primary key | `ReplicateKey` yes, `UpdatePrimaryRegion` no | built: `ReplicaKey` |
| 6 | Standalone security group rules | none; ec2-controller has rule-sync fixes open (#394, #385) | yes | skipped: open question 7 |
| 7 | EC2 `VPNGateway` (attachment, route propagation), `CustomerGateway` | ignored in ec2-controller `v1.23.1`'s `generator.yaml` | yes, except `EnableVgwRoutePropagation` | built: `CustomerGateway`, `VPNGateway` (attachment); route propagation not built (moto has none of it) |
| 8 | EBS `Volume`, `VolumeAttachment` | ignored in ec2-controller's `generator.yaml`; `aws-controllers-k8s/ebs-controller` is an empty scaffold for the EBS direct API (no kinds, no release) | yes | built: `Volume`, `VolumeAttachment` |
| 9 | CloudWatch Logs `MetricFilter`; CloudWatch `CompositeAlarm` | metric filters: cloudwatchlogs-controller PR #61, as a field on ACK's `LogGroup`. `CompositeAlarm`: ignored in cloudwatch-controller `v1.11.0`. | metric filters yes; `PutCompositeAlarm` no | skipped. Metric filters wait on open question 1. `CompositeAlarm`: moto has none of it. |
| 10 | EventBridge Scheduler `Schedule` and `ScheduleGroup`; EventBridge `Connection` and `ApiDestination` | Scheduler: no controller (`community` issue #2784). `Connection` and `ApiDestination`: ignored in eventbridge-controller `v1.9.2`. | yes | `Connection` and `ApiDestination` built; Scheduler skipped: ACK has no Scheduler controller for our manager to run beside (open question 8) |
| 11 | ECR `ReplicationConfiguration`; `RegistryPolicy`, `RegistryScanningConfiguration` | `ReplicationConfiguration`: ecr-controller PR #121, open, conflicts with base, no review, last updated 2026-10-18. The others: none. | yes (one replication rule at most) | built: `ReplicationConfiguration` (PR #121's API; rechecked 2026-11-09: still open, conflicting, no review, head `f6c7d57e`). `RegistryPolicy` and `RegistryScanningConfiguration` next |
| 12 | WAFv2 `WebACLAssociation` | none (wafv2-controller `v1.8.1`) | yes | to build |
| 13 | ElastiCache `GlobalReplicationGroup` | elasticache-controller PR #237: mergeable, active 2026-11-06 | none | skipped: may be close to merging, and moto has none of it |
| 14 | Route 53 DNSSEC and `KeySigningKey`, VPC association authorization | ignored in route53-controller's `generator.yaml` | none | skipped: moto has none of it |
| 15 | IAM `GroupMembership`, `AccessKey`, `LoginProfile`, `SSHPublicKey`, account alias, password policy | none (iam-controller `v1.11.3`; PR #147 for `LoginProfile` closed; rechecked 2026-11-09) | yes | `AccountAlias` built; the rest under way |
| 16 | EC2 `PlacementGroup` | ignored in ec2-controller's `generator.yaml` | none | skipped: moto has none of it |
| 17 | S3 `Object`, the account's public access block | none | yes | to build |
| 18 | EC2 `KeyPair` | ec2-controller PR #396: both reviews' changes made, main merged in 2026-11-04, mergeable | yes | skipped: close to merging; wait for the release |
| 19 | Secrets Manager resource policy (`PutResourcePolicy`) | no issue or PR | yes | to build |
| 20 | RDS event subscriptions | ignored in rds-controller's `generator.yaml` | create and delete yes, modify no | to build |
| 21 | SSM `Association` | ssm-controller PR #85, conflicts with base | none | skipped: moto has none of it |
| 22 | ACM import from a Kubernetes TLS Secret | acm-controller PR #104 | | skipped: a field on an existing kind (open question 1) |
| 23 | `Role` additive policy mode | iam-controller PR #184, second commit | | skipped: changes ACK's own `Role` (open question 3) |

Built before this ranking:

- `RolePolicyAttachment`: iam-controller PR #184.
- `OptionGroup`: rds-controller PR #257.
- `DBProxyTarget`: new.

**`DBRoleAssociation` came next on 2026-11-08** for these reasons:

- the old ranks 3 to 5 waited on open question 1 or would have changed
  ACK's own `Role`;
- rank 6 was marked "not an application-stack need";
- rank 7 may be close to merging;
- table (b) marked RDS role associations "next".

The upstream recheck that day found:

- rds-controller `v1.14.2` with nothing after it on `main`;
- no code, issue or pull request covering `AddRoleToDB*`.

**Database users and roles have no AWS API.** On Google Cloud the Cloud
SQL Admin API manages them (gaps-gcp's `SQLUser.databaseRoles`). On AWS
they are SQL inside the database, so they are out of scope for a
controller of AWS APIs. Open question 2.

## 6. The kinds built

### 6.1 `iam.aws.hfl.dev` `RolePolicyAttachment`

Copied from PR #184's hand-written resource. The changes:

- our module path and API group;
- `roleRef` and `policyRef` resolve to ACK's own `Role` and `Policy`
  (`iam.services.k8s.aws`, from ACK's module) instead of kinds in the same
  module;
- RBAC markers to read them.

**Do not use it on a role managed by an ACK `Role` that lists
`spec.policies`.** ACK's `Role` treats `spec.policies` as the whole set,
read from `ListAttachedRolePolicies`. On its next update it detaches any
policy not listed, including ours, and ours re-attaches on the next
resync (ACK iam-controller `v1.11.3`, `pkg/resource/role/hooks.go`,
`syncManagedPolicies`). PR #184's second commit adds an additive mode to
ACK's `Role` for exactly this case. Until that ships, the kind is for
roles ACK does not manage. The `roleRef` test shows that references
resolve; ACK's iam-controller does not run in it. Open question 3.

### 6.2 `rds.aws.hfl.dev` `OptionGroup`

Generated from PR #257's `generator.yaml` entry and hook templates, with
its `hooks.go`, `hooks_test.go` and tag helpers (`pkg/util/tags.go`)
copied; the import paths are the only change. The PR's hook tests pass
against our regenerated code. Behaviour, as upstream wrote it:

- **Create.** `CreateOptionGroup` takes no options, so the group is
  created, marked not synced, and the options added on the next pass
  through `ModifyOptionGroup` `OptionsToInclude`.
- **Update.** An option removed from the spec goes through
  `OptionsToRemove`.
- **Immutable fields.** Engine, major version and description are
  immutable (a CEL rule in the CRD).
- **Tags** are synced separately.

### 6.3 `rds.aws.hfl.dev` `DBProxyTarget`

New. The spec names:

- one proxy (`dbProxyName` or `dbProxyRef`);
- the target group (`default`, the only one RDS Proxy has);
- exactly one instance or cluster (or a reference to ACK's `DBInstance`
  or `DBCluster`).

A CEL rule in the CRD enforces "exactly one", and every field is
immutable.

- **Create** is `RegisterDBProxyTargets`.
- **Read** is `DescribeDBProxyTargets`, filtered to this registration:
  - an instance's `RDS_INSTANCE` target;
  - a cluster's `TRACKED_CLUSTER` target and the member instances RDS
    reports with its `TrackedClusterId`.
- **Delete** is `DeregisterDBProxyTargets`. A proxy or target already
  gone counts as deleted.
- **Status** carries each target's endpoint, port, role, type and health.
- **Synced only when every target is AVAILABLE.** `IsSynced` decides,
  because the manager leaves `ACK.ResourceSynced` to the runtime, as
  ACK's generated kinds do. The runtime requeues until then.

RDS has no ARN for a target, so `status.ackResourceMetadata.arn` is the
target group's ARN form with the resource appended. It is an identifier,
never sent to AWS.

### 6.4 `rds.aws.hfl.dev` `DBRoleAssociation`

New: no ACK code, issue or pull request covers it (section 5). ACK's
`DBCluster` and `DBInstance` report `status.associatedRoles` but cannot
add one, so Aurora cannot read from or write to S3 or call Lambda, and
RDS for PostgreSQL, SQL Server and Oracle cannot use their S3
integrations, without a step outside Kubernetes. Hand-written, with the
skeleton copied from PR #184 as `DBProxyTarget`'s is.

One kind covers both databases, as `DBProxyTarget` does, because the two
APIs take the same three things. The spec names:

- exactly one cluster or instance (`dbClusterIdentifier`,
  `dbInstanceIdentifier`, or a reference to ACK's `DBCluster` or
  `DBInstance`);
- exactly one role (`roleARN`, or `roleRef` to ACK's
  `iam.services.k8s.aws` `Role`, read from its
  `status.ackResourceMetadata.arn`);
- the feature (`featureName`: `s3Import`, `s3Export`, `Lambda`,
  `S3_INTEGRATION`, ...), which RDS requires for an instance and Aurora
  MySQL leaves out for a cluster.

CEL rules in the CRD enforce both "exactly one" rules and the instance's
feature, and every field is immutable.

- **Create** is `AddRoleToDBCluster` or `AddRoleToDBInstance`.
- **Read** is `DescribeDBClusters` or `DescribeDBInstances`, keeping the
  entry of `AssociatedRoles` with this role and this feature: RDS
  identifies an association by both, since one role may serve two
  features of one database. A database that does not exist has no
  association.
- **Delete** is `RemoveRoleFromDBCluster` or `RemoveRoleFromDBInstance`.
  An association or database already gone counts as deleted.
- **Status** carries RDS's state, `PENDING`, `ACTIVE` or `INVALID`.
- **Synced only when `ACTIVE`.** `INVALID` (RDS cannot assume the role)
  is not terminal: fixing the role's trust policy can mend it, and the
  runtime requeues.
- **Terminal faults:** an association that already exists for this
  database with another role or feature, and the per-database role
  quota.

RDS has no ARN for an association, so `status.ackResourceMetadata.arn` is
the database's ARN form with the role and feature appended. It is an
identifier, never sent to AWS.

**It does not fight ACK.** ACK's `DBCluster` and `DBInstance` read
`AssociatedRoles` into status and never call `RemoveRoleFrom...`
(rds-controller `v1.14.2`), so unlike `RolePolicyAttachment` beside an
ACK `Role` (section 6.1), there is no tug of war. If ACK later adds
`associatedRoles` to `DBCluster`'s spec as the whole set, that changes,
and retiring this kind would mean moving each association into that
field rather than adopting a kind.

### 6.5 `lambda.aws.hfl.dev` `Permission`

New: no ACK kind or pull request adds it. `community` issues #3051 and
#2659 ask for it. ACK's lambda-controller `v1.19.0` has permissions only
inside an `Alias`, as `spec.permissions`, which ACK treats as the alias's
whole policy. A function's own policy, the one S3 notifications, an ALB
target group, EventBridge rules and SNS subscriptions need, has no kind.

The calls, and the reading of a policy statement back into the fields of
ACK's `AddPermissionInput`, are copied from ACK's `Alias`
(`pkg/resource/alias/hooks.go`). One change: the policy is parsed with
`encoding/json` instead of `github.com/micahhausler/aws-iam-policy`, so
there is no new dependency.

The spec names:
- the function (`functionName`, or `functionRef` to ACK's `Function`);
- optionally a version or alias (`qualifier`);
- the statement's `statementID`, `action` and `principal`;
- optionally `sourceARN`, `sourceAccount`, `eventSourceToken`,
  `principalOrgID` and `functionURLAuthType`.

Lambda cannot change a statement, so every field is immutable.

- **Create** is `AddPermission`.
- **Read** is `GetPolicy`, keeping the statement with this ID:
  - no function, no policy or no such statement reads as not found;
  - a statement removed outside Kubernetes is added again;
  - a field the spec sets is kept as written, because AWS stores some in
    a normal form (an account ID as its root ARN);
  - an unset field is filled from the statement, as adoption needs.
- **Delete** is `RemovePermission`. A statement or function already gone
  counts as removed.
- **Status** carries the statement as Lambda stores it.
- **Terminal faults:** a statement Lambda refuses
  (`InvalidParameterValueException`) and a full policy
  (`PolicyLengthExceededException`). A function that does not exist yet,
  and a concurrent policy change (`ResourceConflictException`), are
  retried.

**Do not point it at an alias that an ACK `Alias` manages.** ACK's
`Alias` reads the alias's whole policy and removes every statement its
`spec.permissions` does not list, even when the list is empty
(`syncPermissions`, `comparePermissions`). Use `Alias.spec.permissions`
there. ACK's `Function` and `Version` manage no policy, so a statement
on a function or version stands alone. This is the same tug of war as
open questions 3 and 7, documented here as section 6.1 documents its own.

### 6.6 `autoscaling.aws.hfl.dev` `ScalingPolicy`

An EC2 Auto Scaling group's scaling policy: target tracking, step, simple
or predictive. ACK's autoscaling-controller `v1.5.0` has only
`AutoScalingGroup`, and a group without policies does not scale. No pull
request adds policies.

**Generated** by ACK's generator. `services/autoscaling/generator.yaml`
maps three operations to the resource, the way ACK's
applicationautoscaling-controller `v1.9.0` generates its own
`ScalingPolicy` from a Put operation:

- `PutScalingPolicy` creates and updates;
- `DescribePolicies` reads;
- `DeletePolicy` deletes.

Two hook templates are written here:

- one asks `DescribePolicies` for the one policy by name;
- one reads an empty predictive scaling configuration as none.

The policy name and group are immutable. Every other field is updated by
putting the policy again.

**Server defaults are no drift.** A field the spec leaves unset takes
the server's value before comparing. These fields are:

- policy type;
- enabled;
- metric aggregation type;
- cooldown;
- instance warmup;
- target tracking's `disableScaleIn`.

This is `custom_delta.go`, copied from ACK's applicationautoscaling-controller
(`scalable_target/custom_delta.go`), which handles its own defaults the
same way. ACK's late initialization cannot do this. The server reports
each of these fields for some policy types only, so the late
initialization would wait forever for the rest. The first end-to-end run
failed exactly so: "Late initialization did not complete", and never
synced. `status.alarms` lists the CloudWatch alarms a
target-tracking policy creates. Tags do not apply.

**Not built: a reference to ACK's `AutoScalingGroup`.** The generator
writes a reference to a kind of the same service with ACK's import path,
`github.com/aws-controllers-k8s/autoscaling-controller/`.
`hack/generate.sh` rewrites that path to ours, because ACK's templates
hard-code it for the generated packages. So a generated kind cannot
reference ACK's kind of its own service. The group is named by
`autoScalingGroupName`, and section 8 tracks the reference.

### 6.7 `autoscaling.aws.hfl.dev` `ScheduledAction`

A scheduled change of an EC2 Auto Scaling group's minimum, maximum or
desired capacity: once at a start time, or on a cron recurrence in a
time zone. ACK's autoscaling-controller `v1.5.0` has none, and no pull
request adds it.

**Generated** as `ScalingPolicy` is:

- `PutScheduledUpdateGroupAction` creates and updates;
- `DescribeScheduledActions` reads, asked for the one action by name;
- `DeleteScheduledAction` deletes.

The action name and group are immutable, and the deprecated `Time` field
is ignored.

**Server defaults are no drift.** For a recurring action, EC2 Auto
Scaling reports the next start time it computed, and it may report the
time zone it applies. `custom_delta.go`, copied from ACK's
applicationautoscaling-controller as for `ScalingPolicy`, keeps them
from reading as drift.

**A one-time action that ran is completed, not put again.** AWS deletes
a one-time action once it has run, and the object would otherwise read
as not found and be put again with a start time in the past, which AWS
refuses. A read hook (`hooks.go`) treats the action as completed when
all of these hold:

- it is one-time (a start time, no recurrence);
- its start time has passed;
- this object created it (it carries ACK's finalizer);
- EC2 Auto Scaling no longer reports it.

Completed reads as found, with the spec as written, so there is no
drift. The object is synced and Ready, with a `Completed` condition
saying when it ran, and deleting it calls nothing. An action this object
never created, a recurring one, or one not yet due is read as before: if
it is missing, it is put. Changing a completed object's start time to a
time to come puts it again.

### 6.8 `kms.aws.hfl.dev` `ReplicaKey`

A multi-Region primary KMS key's replica in another Region
(`ReplicateKey`). kms-controller pull request #112 adds the kind, but it
is not close to merging. An ACK maintainer's review found that its read,
update and delete act on the primary key.

The cause is the Region. `ReplicateKey` must be called in the primary
key's Region, so ACK's runtime gives the resource that Region's client.
The replica, though, lives in its own Region, and shares its key ID with
the primary key. The pull request sends `DescribeKey`, `PutKeyPolicy`,
the tag calls and `ScheduleKeyDeletion` to the primary key's Region by
key ID, so its delete would schedule the primary key for deletion.

**Written by hand here**, with the pull request's API, `KeyMetadata`
conversion and policy and tag calls copied, and every call on the
replica sent to a client for the replica's Region, naming the replica by
its ARN:

- **Create** is `ReplicateKey`, in the manager's Region, which is the
  primary key's.
- **Read** is `DescribeKey` of the primary key, to find the replica in
  its `MultiRegionConfiguration`. That finds a replica created before its
  status was saved. Then `DescribeKey`, `GetKeyPolicy` (only when the spec
  sets a policy) and `ListResourceTags` of the replica, in its Region. A
  replica pending deletion reads as gone.
- **Update** puts the policy and the tags (`PutKeyPolicy`, `TagResource`,
  `UntagResource`) in the replica's Region. The key, the replica Region,
  the description and `bypassPolicyLockoutSafetyCheck` are immutable. The
  policy is compared as JSON and the tags as a set.
- **Delete** is `ScheduleKeyDeletion` of the replica in its Region (its
  key ID, after checking its ARN is in that Region),
  with ACK's own `kms.services.k8s.aws/pending-window-in-days` annotation,
  as ACK's `Key` uses it.
- **Synced** once the replica is `Enabled`; a new replica is `Creating`
  until KMS has copied the key material.

**The primary key is never the target.** Two checks hold this:

- `TestDeleteNeverReachesThePrimaryRegion` runs against a stand-in that
  records the Region each call is signed for. It fails if
  `ScheduleKeyDeletion`, or any call but the primary key's `DescribeKey`,
  reaches the primary key's Region. With the pull request's behaviour
  restored, it fails.
- `TestReplicaKey` checks against moto that the primary key is `Enabled`
  and untagged after every step, delete included.

`keyRef` resolves to ACK's `Key` (`status.keyID`). The status's
`replicaKeyMetadata` and the `tags` use ACK kms-controller's own types, as
the pull request does.

### 6.9 `applicationautoscaling.aws.hfl.dev` `ScheduledAction`

A scheduled change of an Application Auto Scaling scalable target's
minimum and maximum capacity, on an `at()`, `rate()` or `cron()`
schedule in a time zone. The target can be an ECS service, a DynamoDB
table, Aurora replicas, a Lambda alias's provisioned concurrency and so
on. ACK's applicationautoscaling-controller `v1.9.0` has `ScalableTarget`
and `ScalingPolicy`, and its `generator.yaml` ignores `ScheduledAction`.
No pull request adds it.

**Generated** the way that controller generates its own Put-shaped
`ScalingPolicy`:

- `PutScheduledAction` creates and updates;
- `DescribeScheduledActions` reads (the generated request already names
  the action, resource and dimension);
- `DeleteScheduledAction` deletes.

The service namespace, resource, scalable dimension and name are
immutable. A time zone the spec leaves unset is no drift:
`custom_delta.go` is copied from the same controller, as for the EC2 Auto
Scaling kinds.

**Not built: a reference to ACK's `ScalableTarget`.** As with
`ScalingPolicy` and its group (section 6.6), a generated kind cannot
reference ACK's kind of the same service yet (section 8). The target is
named by `serviceNamespace`, `resourceID` and `scalableDimension`.

### 6.10 `ec2.aws.hfl.dev` `CustomerGateway`

The on-premises end of a site-to-site VPN: its public address, BGP ASN
and type (`ipsec.1`), or a certificate. ACK's ec2-controller `v1.23.1`
ignores it in its `generator.yaml`, and no pull request adds it.

**Generated** from `CreateCustomerGateway`, `DescribeCustomerGateways` and
`DeleteCustomerGateway`, with ACK ec2-controller's own patterns for an EC2
resource, copied from its `InternetGateway`:

- tags passed as `TagSpecifications` on create (`hooks.go`);
- tags synced with `CreateTags` and `DeleteTags` afterwards (its
  `pkg/tags`, copied with our types);
- synced when the state is `available`.

A gateway in state `deleted` reads as gone: EC2 keeps it listed for a
while. Every field but the tags is immutable, because EC2 has no call
that changes a customer gateway. `BgpAsn` is a number on create but a
string on output, so the spec keeps the value written
(`set: ignore: "from"`). The generator also names the type field
`type_`, which is kept as it is.

`services/ec2/generator.yaml` ignores the other 80 resources the
generator finds in the EC2 model.

### 6.11 `ec2.aws.hfl.dev` `VPNGateway`

The AWS end of a site-to-site VPN (a virtual private gateway), attached
to one VPC. ACK's ec2-controller `v1.23.1` ignores it, and no pull
request adds it.

**Generated** from `CreateVpnGateway`, `DescribeVpnGateways` and
`DeleteVpnGateway`. Its VPC attachment is modelled as ACK's ec2-controller
models its `InternetGateway`'s, with that controller's hooks copied:

- `spec.vpc` (from `AttachVpnGateway`'s `VpcId`) is attached after the
  create;
- a changed VPC is moved with `DetachVpnGateway` and `AttachVpnGateway`;
- the VPC is read back from the gateway's `VpcAttachments`;
- the gateway is detached before the delete, because EC2 refuses to
  delete an attached one.

Tags and readiness work as for `CustomerGateway`. The type, Amazon-side
ASN and Availability Zone are immutable.

**Not built: a reference to ACK's `VPC`,** the same-service limit
(section 8). **Not built: route propagation**
(`EnableVgwRoutePropagation` on a route table): moto does not implement
it, so nothing about it could be proven.

**Not proven: detaching takes time on AWS.** A delete right after the
detach can be refused while the gateway is `detaching`. The runtime
retries it as a recoverable error, but moto detaches at once, so this
path is not exercised.

### 6.12 `eventbridge.aws.hfl.dev` `Connection`

An EventBridge connection: the credentials an API destination calls an
HTTP endpoint with (basic, API key or OAuth client credentials), and the
parameters added to every call. ACK's eventbridge-controller `v1.9.2`
ignores `Connection` and `ApiDestination` in its `generator.yaml`, and no
pull request adds them.

**Generated** from `CreateConnection`, `DescribeConnection`,
`UpdateConnection` and `DeleteConnection`, and synced when the
connection is `AUTHORIZED`.

**Secrets come from Kubernetes Secrets.** The password, API key and
OAuth client secret are ACK `SecretKeyReference`s (`is_secret`), read
when the connection is created or updated. EventBridge never returns
them, nor the value of an HTTP parameter marked secret. A read hook
(`hooks.go`) keeps the spec's values; without it every resync saw drift
and updated the connection. A non-secret field read back, such as a
username, an endpoint or a parameter, is still compared.

**Not proven: a changed Secret.** Changing the Kubernetes Secret's value
does not change the spec or anything the read compares. So nothing here
pushes the new value until something else in the spec changes. This was
not tested.

### 6.13 `ec2.aws.hfl.dev` `Volume`

An EBS volume. ACK's ec2-controller `v1.23.1` ignores it, and no pull
request adds it. `aws-controllers-k8s/ebs-controller` is an empty
scaffold for the EBS direct API (snapshot blocks), not for volumes.

**Generated** from `CreateVolume`, `DescribeVolumes` and `DeleteVolume`.
It needs patch 0002 to the generator: `CreateVolume` was on the
generator's list of operations whose output keeps the v1 SDK's shape
name, so the generated code called for `*ec2.Volume`, which SDK v2 does
not have (`UPSTREAM.md`, Patches). With the patch, only `services/ec2`
regenerates differently.

- **Update:** `ModifyVolume` changes the size, type, IOPS, throughput and
  multi-attach; the tags are synced as for the gateways. Tags go on
  create as `TagSpecifications`. Both are copied from ec2-controller's
  `InternetGateway`.
- **Immutable:** the Availability Zone, snapshot, encryption, KMS key and
  Outpost.
- **Server defaults are no drift.** EC2 fills in the type, IOPS,
  throughput, multi-attach and encryption, each for some volume types
  only. `custom_delta.go` keeps them from reading as drift, as for the
  Auto Scaling kinds.
- **Read:** `InvalidVolume.NotFound` is not found, and a volume
  `deleting` or `deleted` reads as gone.
- **Synced** when `available` or `in-use`.
- **No attachments in status.** `Volume.Attachments` and
  `CreateVolumeOutput.Attachments` are ignored. The attachment belongs to
  the `VolumeAttachment` kind that follows. The generator also cannot
  rename a shape, so leaving the field in would have emitted a
  `VolumeAttachment` type that clashes with that kind's name, which
  matches Terraform's `aws_volume_attachment`. `status.state` still says
  `in-use`.

**Not proven:** a modification's `modifying` and `optimizing` states,
and EBS's limit of one modification per six hours. moto applies a
change at once, and a second change within six hours is refused on AWS.

### 6.14 `ec2.aws.hfl.dev` `VolumeAttachment`

An EBS volume attached to an instance as a device: Terraform's
`aws_volume_attachment`, whose name the kind keeps. ACK's ec2-controller
`v1.23.1` has no attachment, and no pull request adds one (checked
2026-11-09).

**Hand-written** on ACK's runtime, on PR #184's skeleton, as for
`DBRoleAssociation`: `AttachVolume` and `DetachVolume` are not a create
and delete the generator reads, and there is no read of an attachment
but the volume's own (`DescribeVolumes`).

- **Spec:** `volumeID` or `volumeRef` (our `Volume`, its
  `status.volumeID`), `instanceID` and `deviceName`, all immutable. A
  volume attaches to an instance once, so the volume and instance find
  the attachment; the device is read back, and a different one is drift
  that `sdkUpdate` refuses (terminal).
- **Status:** `state`, `attachTime`, `deleteOnTermination`. Synced when
  `attached`. EC2 has no ARN for an attachment; the volume's ARN form
  with the instance appended stands in, for ACK's metadata only.
- **Read:** a missing volume, or no attachment to the instance (or a
  `detached` one), is not found, so a detach outside Kubernetes is
  attached again.
- **Delete** detaches; a volume or attachment already gone is detached.
- **Errors:** a bad parameter is terminal; a volume or instance not there
  yet, or a volume still attached elsewhere (`VolumeInUse`), is retried.
- **No `instanceRef`.** ACK's ec2-controller has `Instance`, but a
  reference to it needs ec2-controller as a module here (section 8,
  item 10). `instanceID` takes the ID.

**Not proven:** the `attaching` and `detaching` states lasting (moto
does both at once), `VolumeInUse` (moto does not refuse a second
attachment), and detaching from a running instance whose file system is
mounted (AWS may leave it `busy`; Terraform's `force_detach` and
`stop_instance_before_detaching` are not built).

### 6.15 `eventbridge.aws.hfl.dev` `APIDestination`

An EventBridge API destination: the HTTP endpoint, method and rate limit
a rule sends events to, through a `Connection`'s credentials. ACK's
eventbridge-controller `v1.9.2` ignores it, and no pull request adds it
(checked 2026-11-09). The generator names the kind `APIDestination`, as
ACK's own initialisms do; the AWS name is `ApiDestination`.

**Generated** from `CreateApiDestination`, `DescribeApiDestination`,
`UpdateApiDestination` and `DeleteApiDestination`, in
`services/eventbridge` beside `Connection`.

- **Reference:** `connectionRef` names our own `Connection`, and resolves
  to its ARN. The same service, so the import path is not a problem
  (§8 item 10 is about ACK's kinds).
- **Immutable:** the name.
- **Server default is no drift:** a rate limit the spec leaves out reads
  back as AWS's 300 per second (`custom_delta.go`, as for the Auto
  Scaling kinds).
- **Synced** when `ACTIVE`. **Not found:** `ResourceNotFoundException`.
- No tags: API destinations have none.

**Not proven:** AWS's default rate limit coming back from a real read
(moto returns none; the unit test feeds 300 in), and a destination whose
connection is not yet authorized (moto authorizes at once).

### 6.16 `ecr.aws.hfl.dev` `ReplicationConfiguration`

An ECR registry's replication rules: which repositories (by prefix) are
copied to which Regions and accounts. ACK's ecr-controller `v1.11.1` has
no replication. ecr-controller pull request #121 adds this kind; it is
open, conflicts with its base and has no review (rechecked 2026-11-09,
head `f6c7d57e`, last updated 2026-10-18).

**Hand-written** on ACK's runtime: the API has no create, describe or
delete of its own, only `PutReplicationConfiguration` (the whole set) and
`DescribeRegistry`.

- **From PR #121:** the API type, the behaviour and the conversions. Its
  files are marked generated, but were written by hand; here they carry a
  modified-by note instead. Its debug logging is dropped.
- **The skeleton** is PR #184's, as for our other hand-written kinds.
- **Read:** a registry with no rules reads as not found, so the runtime
  creates the configuration, and owns it.
- **Update** puts the whole set again, also when it was changed outside
  Kubernetes.
- **Delete** puts no rules.
- **The registry:** `PutReplicationConfiguration` takes no registry, so it
  acts on the manager's own. `spec.registryID`, if set, must be that
  registry, and anything else is refused (a terminal condition) before
  anything is put.
- **One per registry.** Two objects for the same registry would put their
  rules in turn; nothing stops that yet, as with ACK's other
  account-wide settings.

**Not proven:** more than one rule (moto allows one), a destination
account's registry policy (moto does not check it), and replication
itself.

### 6.21 `iam.aws.hfl.dev` `AccountAlias`

The AWS account's alias, the name in its sign-in URL: Terraform's
`aws_iam_account_alias`. ACK's iam-controller `v1.11.3` has none, and no
pull request adds one (rechecked 2026-11-09).

**Hand-written** on ACK's runtime, on PR #184's skeleton: an
account-wide setting (`CreateAccountAlias`, `ListAccountAliases`,
`DeleteAccountAlias`), not a resource the generator reads.

- **Spec:** `accountAlias`, immutable, with IAM's pattern (except the
  rule against two hyphens in a row, which a CRD pattern cannot say).
- **Read:** the account's alias. Another alias, or none, is not this
  object's: the create that follows sets it back, since a new alias
  replaces the old. A new object sets the alias even when the account
  already has it, instead of ACK's "already exists".
- **Delete** deletes the alias only while it is still this object's.
- **One per account.** Two objects would set their aliases in turn, as
  for `ReplicationConfiguration`.
- **Errors:** an alias another account has (`EntityAlreadyExists`) is
  terminal.

**Not proven:** an alias another account already has (moto does not
check), and real AWS.

## 7. The operator: ACK's identity, beside ACK's controller

### 7.1 How ACK's controllers get their identity (read at the pins)

- **IRSA.** ACK's chart creates the ServiceAccount `ack-<service>-controller`
  with `serviceAccount.annotations`, normally `eks.amazonaws.com/role-arn`.
  EKS's pod identity webhook then injects a projected token and
  `AWS_ROLE_ARN`. The role's trust policy names the ServiceAccount by
  namespace and name (`sub: system:serviceaccount:<ns>:<name>`).
- **EKS Pod Identity.** An association, per cluster, namespace and
  ServiceAccount, to a role. The pod identity agent serves the
  credentials.
- **A credentials Secret.** `aws.credentials.secretName` mounts a shared
  credentials file and sets `AWS_SHARED_CREDENTIALS_FILE` and
  `AWS_PROFILE`.
- **On top of that base identity, per resource**, the runtime
  (`pkg/runtime/reconciler.go`) can assume another role:
  - CARM: a namespace annotated `services.k8s.aws/owner-account-id`
    looks up a role ARN in the `ack-role-account-map` ConfigMap, in the
    controller's own namespace;
  - team CARM: `services.k8s.aws/team-id` and `ack-role-team-map`, behind
    a feature gate;
  - `IAMRoleSelector` objects, behind a feature gate.

  These target roles trust the base role, not a ServiceAccount.

So "the same AWS identity" means three things:

- the same base IAM role;
- the same CARM ConfigMaps, which means the same namespace;
- the same flags and feature gates.

### 7.2 What ours does

`pkg/operator`, deployed by `config/operator`:

1. **Finds ACK controllers** by the chart's label
   `app.kubernetes.io/name: <service>-chart`, or by the annotation
   `aws.hfl.dev/ack-service`, for each service we extend (`iam`, `rds`).
2. **Runs our manager in the same namespace.** The manager is
   `ack-extensions-<service>-manager`, and gets:
   - the controller container's **arguments**, minus the three flags that
     name ACK's kinds, which our runtime would reject;
   - its **environment**, so region, endpoints, watch namespace and
     selectors, CARM, feature gates, deletion policy and the credentials
     file stay the same;
   - the **volumes it mounts**, and its pod placement and security
     context.

   It reads the same CARM ConfigMaps, because `ACK_SYSTEM_NAMESPACE` is
   the same namespace.
3. **Identity `own` (the default).** Our ServiceAccount
   `ack-extensions-<service>-manager` carries ACK's ServiceAccount's
   `eks.amazonaws.com/*` annotations. AWS needs one more grant per base
   role (README, "AWS identity"). After that, CARM and selector roles work
   unchanged, because they trust the base role.
4. **Identity `ack`** (`--identity=ack` or `aws.hfl.dev/identity: ack` on
   ACK's Deployment): our manager runs as ACK's ServiceAccount, and
   ACK's ServiceAccount is bound to our manager's role. No AWS change.
5. **Binds the manager's ClusterRole** (`config/rbac`, generated from the
   markers) and a leader-election Role in the namespace.
6. **Cleans up.** Everything it makes carries
   `app.kubernetes.io/managed-by: ack-extensions-operator` and is deleted
   when ACK's controller goes. Namespaced objects also carry an owner
   reference to ACK's Deployment.
7. **The operator holds no AWS credentials.** It may bind only our two
   manager ClusterRoles (RBAC `bind` by name). It does create
   ServiceAccounts carrying a role ARN, copied only from ACK's
   ServiceAccount in the same namespace. Kubernetes RBAC cannot narrow
   that further. Anyone who may create ServiceAccounts there could do
   the same, but it is a reason to run the operator only where ACK's
   namespace is trusted.

A static bundle was the alternative. It is cheaper, but it would have to
repeat each ACK installation's region, endpoint, watch scope, CARM
setting, feature gates and credentials by hand, and fall out of step
when ACK's chart values change. The operator reads them from ACK's
Deployment. It is about 400 lines.

### 7.3 Kubernetes rights compared (ACK rds-controller `v1.14.2`'s chart)

| | ACK's controller (`ack-rds-controller`) | our manager, identity `own` |
| --- | --- | --- |
| ACK's rds kinds (DB instances, clusters, proxies, snapshots, ...) | full control | read only (`dbproxies`, `dbinstances`, `dbclusters`, for references) |
| our kinds | none | full control (`*.rds.aws.hfl.dev`) |
| ConfigMaps and Secrets, cluster-wide | get, list, watch, patch | get, list, watch, patch (the runtime's own rule: Secret references and field exports) |
| namespaces; `fieldexports`, `iamroleselectors` | yes | yes (the runtime) |
| other services' kinds | read of `ec2`, `iam`, `kms` kinds it references | none |

With identity `ack`, our manager gets the left column plus our kinds:
full control of every ACK rds object, so it could delete a database's
`DBInstance`. ACK's controller also gets our kinds. The difference is
smaller than on Google Cloud: ACK's controller has no cluster-wide
webhook or core rights. But `own` still keeps ACK's databases out of our
manager's reach. Both columns hold the runtime's cluster-wide Secret read
and patch; that is ACK's design, not ours.

### 7.4 What was proven

**`TestReconcile`, against envtest:**

- no manager before ACK's ServiceAccount exists;
- identity `own`:
  - our ServiceAccount carries ACK's role ARN, and no non-identity
    annotation;
  - the manager has ACK's arguments without the three kind flags, ACK's
    environment and its credentials volume (not an unused one);
  - it is owned by ACK's Deployment and bound to our ClusterRole;
- no manager for an ACK controller we do not extend (s3);
- switching to identity `ack`: ACK's ServiceAccount, ours removed, the
  binding moved;
- ACK's controller removed: everything of ours is deleted.

**`tests/operator/run.sh` (`make operator-e2e`) on kind** (kindest/node
v1.37.0; last run 2026-11-09 on a Spot VM, e2-standard-8, by
`../offload`), with moto in the cluster and ACK's **real** rds-controller
`v1.14.2` installed from its own chart, with a credentials Secret and an
IRSA role annotation:

```text
== cluster ack-extensions-operator
== images
ok: moto and public.ecr.aws/aws-controllers-k8s/rds-controller:1.14.2 loaded
== fake AWS
ok: moto ready in fake-aws
== ACK's rds-controller v1.14.2, from its own chart
ok: ACK's controller ack-system/ack-rds-rds-chart running as ack-system/ack-rds-controller
== ours: config/default by ko
ok: CRDs installed: scalingpolicies.autoscaling, scheduledactions.autoscaling, rolepolicyattachments.iam, permissions.lambda, dbproxytargets.rds, dbroleassociations.rds, optiongroups.rds (*.aws.hfl.dev); operator running
== identity own: our manager beside ACK's, as ACK's role
ok: manager ack-system/ack-extensions-rds-manager rolled out
ok: manager pod runs as ack-system/ack-extensions-rds-manager, annotated with ACK's role arn:aws:iam::123456789012:role/ack-rds-controller
ok: same credentials Secret (aws-creds), region (us-west-2) and endpoint as ACK's controller
ok: no autoscaling, iam or lambda manager: none of their ACK controllers is installed
ok: rights: ours manages *.rds.aws.hfl.dev and only reads ACK's rds kinds and Roles; ACK's controller has nothing of ours
== gap kinds reconcile against moto
ok: port-forward to moto
ok: DBProxyTarget default/app-db synced through the manager on kind
ok: OptionGroup default/app-mysql synced through the manager on kind
ok: DBRoleAssociation default/app-db-s3-import synced through the manager on kind
ok: in moto: app-proxy targets ['app-db'] ; app-mysql options ['MARIADB_AUDIT_PLUGIN'] ; app-db roles [('app-db-s3', 's3Import')]
== identity ack: share ACK's ServiceAccount
ok: manager switched to ack-system/ack-rds-controller
ok: our ServiceAccount removed
ok: shared: ACK's ServiceAccount now also manages our kinds
ok: back to identity own
all checks passed (cluster ack-extensions-operator left running; kind delete cluster --name ack-extensions-operator)
```

On the way, `kubectl auth can-i get roles.iam.services.k8s.aws` answered
"no" although our ClusterRole grants it. ACK's iam CRDs are not on this
cluster, so kubectl could not resolve the resource and asked about a
core resource of that literal name. The checks of ACK's `Role`s now use a
SubjectAccessReview, which names the group and resource itself.

Also on the way: moto crash-looped on kind because a Service named `moto`
makes Kubernetes set `MOTO_PORT=tcp://...` in the pod, which moto reads
as its port (`enableServiceLinks: false` in `config/fake-aws`). An image
known only by digest loads into kind under a placeholder name, so the
script checks the tag's digest and loads by tag.

**Not proven, and not possible on kind:**

- IRSA and EKS Pod Identity themselves: the webhook and agent exchanging
  the pod's token for the role's credentials;
- that AWS would authorise the role, since moto does not enforce IAM.

What is proven is everything on our side of that:

- our pod presents a ServiceAccount carrying exactly ACK's role
  annotation, in ACK's namespace, with ACK's credentials file;
- on EKS, the one extra trust-policy entry or association then applies to
  it.

## 8. What is left

Before it can run on a real cluster:

1. Images, registry, signing and a release version scheme. Today ko
   builds them locally (`config/operator` uses `ko://` references).
2. A namespace-scoped ACK installation (`installScope: namespace`): the
   operator copies ACK's `--watch-namespace`, but always binds our role
   cluster-wide.
3. One run against a real AWS account per kind, to check moto against
   AWS:
   - proxy target health over time;
   - option group behaviour with real option settings;
   - `TrackedClusterId` for cluster members;
   - role associations: how long `PENDING` lasts, when `INVALID` appears
     and whether it mends once the role's trust policy is fixed, and the
     real responses and faults of `RemoveRoleFromDBCluster` and
     `RemoveRoleFromDBInstance`, which moto lacks (section 4.4).
4. CRD labels marking a gap kind and naming the upstream kind it
   anticipates, as gaps-gcp does.
5. Offer patch 0001 upstream; comment on PRs #184 and #257 with what we
   found (both conflict with their base).
6. `hack/upstream-pr.sh`, as gaps-gcp has, to follow the PR heads copied
   from.
7. CI: `make check` on Linux, with the envtest assets and moto cached.
8. The OptionGroup test takes about 50 seconds: options are added on the
   second reconcile, after the runtime's requeue delay.
9. `DBRoleAssociation`'s delete is proven end to end only up to the
   `RemoveRoleFrom...` call (section 4.4, open question 6).
10. A generated kind cannot reference ACK's kind of the same service
    (`ScalingPolicy` to ACK's `AutoScalingGroup`, section 6.6):
    `hack/generate.sh` rewrites ACK's import path for the service to
    ours. It needs the rewrite limited to the packages generated here, or
    a hand-written references file.
11. The heavy checks run on a Spot VM (`../offload`), not the laptop. A
    fresh VM has no `third_party/` (gitignored), so the command is
    `make tools && make check && make operator-e2e`.

## 9. Open questions

1. **Fields on existing ACK kinds** (Secrets Manager rotation in PR #69,
   ACM import in PR #104). gaps-gcp's rule (builder, 2026-11-07): wait
   for the release; ship our own copy of the kind, in our group, only when
   a package cannot work without the field and the release is not near.
   Does the same rule hold for ACK? Proposed: yes.
2. **Database users and roles on RDS** have no AWS API. Out of scope
   here, or a separate project that speaks SQL (connection details from
   ACK's `DBInstance`, credentials from Secrets Manager)? Proposed: out of
   scope for this repository.
3. **`RolePolicyAttachment` on a role an ACK `Role` manages** loses the
   tug of war with ACK's `Role` (section 6.1). Options:
   - document it (done);
   - refuse a `roleRef` to an ACK `Role` with `spec.policies`;
   - ship PR #184's additive mode, which means changing ACK's `Role`,
     a fork.

   Proposed: refuse such a `roleRef` until upstream ships the mode.
4. **Identity default.** `own` is the default (narrower Kubernetes
   rights, one AWS grant per role); `ack` needs no AWS step. Keep `own`?
5. **One manager per ACK controller** (per service, per namespace). That
   follows ACK's model, and with the operator it is automatic. Confirm.
6. **moto lacks `RemoveRoleFromDBCluster` and `RemoveRoleFromDBInstance`**
   (section 4.4), so `DBRoleAssociation`'s delete is proven against a
   stand-in, not a fake with state. Options:
   - leave it (the stand-in unit test, and the end-to-end test up to the
     call);
   - carry a patch to moto here, applied by `hack/moto.sh` and in the
     kind image, which means building our own moto image;
   - contribute the two operations to moto (getmoto/moto, Apache-2.0)
     and move the pin when it ships.

   Proposed: contribute them to moto, and leave it until then. It does
   not change the kind.
7. **Standalone security group rules** (rank 6 in section 5). ACK's
   `SecurityGroup` holds its rules inline (`ingressRules`, `egressRules`)
   and syncs them as the whole set. A rule kind, for a stack that adds a
   rule to a group it does not own (an EKS node group's, an ALB target's),
   would be revoked by the group's next sync. The same tug of war happens
   between `RolePolicyAttachment` and an ACK `Role` (section 6.1, open
   question 3). Options:
   - build the kind for groups ACK does not manage, and document it, as
     section 6.1 does;
   - refuse a rule whose group is an ACK `SecurityGroup` that lists rules;
   - wait for an additive mode upstream.

   Proposed: the second, the same answer as question 3, so the two
   behave alike.
8. **A service ACK has no controller for** (EventBridge Scheduler,
   rank 10). The operator runs our manager beside an ACK controller, and
   takes its identity from it: the ServiceAccount's role annotations, the
   credentials Secret, the region, CARM and the flags (section 7). With no
   ACK Scheduler controller there is nothing to find, nothing to copy the
   identity from, and the manager would never run. Options:
   - run it beside another ACK controller (EventBridge's, as Scheduler is
     an EventBridge product), with that controller's identity, which then
     needs the `scheduler:*` rights too;
   - an install of its own: a Deployment, ServiceAccount and identity the
     user configures, as ACK's chart does, outside the operator;
   - leave such services out until ACK has a controller for them.

   Proposed: the first, beside EventBridge's controller, documented as
   needing `scheduler:*` on that role. Scheduler waits for the answer.
