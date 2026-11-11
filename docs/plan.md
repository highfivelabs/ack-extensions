# ack-extensions plan

Status, 2026-11-09: the repository has twenty-nine kinds:

- `applicationautoscaling.aws.hfl.dev` `ScheduledAction`;
- `autoscaling.aws.hfl.dev` `ScalingPolicy` and `ScheduledAction`;
- `ec2.aws.hfl.dev` `CustomerGateway`, `VPNGateway`, `VPNConnection`,
  `VPNGatewayRoutePropagation`, `NetworkInterfacePermission`, `Volume` and
  `VolumeAttachment`;
- `ecr.aws.hfl.dev` `ReplicationConfiguration`;
- `eventbridge.aws.hfl.dev` `Connection` and `APIDestination`;
- `iam.aws.hfl.dev` `RolePolicyAttachment`, `UserGroupMembership`, `LoginProfile`, `SSHPublicKey`, `AccountAlias`, `AccountPasswordPolicy`, `AccessKey` and `VirtualMFADevice`;
- `kms.aws.hfl.dev` `ReplicaKey`;
- `lambda.aws.hfl.dev` `Permission`;
- `organizations.aws.hfl.dev` `AccountPlacement`, `DelegatedAdministrator`,
  `Organization`, `Policy` and `PolicyAttachment`;
- `rds.aws.hfl.dev` `OptionGroup`;
- `rds.aws.hfl.dev` `DBProxyTarget`;
- `rds.aws.hfl.dev` `DBRoleAssociation`;
- `scheduler.aws.hfl.dev` `ScheduleGroup` and `Schedule`;
- `wafv2.aws.hfl.dev` `WebACLAssociation`.

There is an operator that runs our managers per environment (one per
controller in each namespace with an `ACKContext`, as that environment's
own role through EKS Pod Identity; section 7.6) or beside ACK's
controllers as the same AWS identity, and tests against a fake AWS (moto), in Go and on
kind (section 8). Beside them, a generic Cloud Control route
(`docs/cloudcontrol.md`): kinds generated from CloudFormation registry
schemas in `<service>.cloudcontrol.aws.hfl.dev` (three so far:
`iam` `SAMLProvider`, `ec2` `TransitGatewayRouteTable`, `guardduty`
`Detector`), served by one generic manager per environment that a
`CloudControlContext` enables, as that environment's own AWS role through
EKS Pod Identity. There is no image registry, release or remote yet.
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

`task check` fails if it changes anything committed. Generation is
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
  2. Run `task upstream generate`, then `task check`.

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

### 4.3 Results (`task test`; latest 2026-11-09 on a Spot VM, linux amd64, Go 1.27.3)

```text
--- PASS: TestUserGroupMembership
    the user is put in the listed groups and a group it was in for
    another reason is left (no update across three resyncs); a group
    taken off the list is removed and one added is added; a removal
    outside Kubernetes is put back; the user cannot change; userRef and
    groupRefs resolve to ACK's User and Group; deletion-policy retain
    leaves the user in the groups; delete removes the listed groups and
    leaves the others
--- PASS: TestManagedIgnoresOtherGroups, TestChanges,
          TestGroupOrderIsNoDrift (membership unit tests)
--- PASS: TestLoginProfile
    a login profile is made from a Kubernetes Secret's password and
    settles (no update across three resyncs); passwordResetRequired is
    updated; the user cannot change; deletion-policy retain leaves the
    profile; delete deletes it
--- PASS: TestDefaultResetIsNoDrift, TestSetResetIsCompared
          (login profile unit tests)
--- PASS: TestSSHPublicKey
    a key is uploaded, active, and settles (no update across three
    resyncs); it is deactivated; a reactivation outside Kubernetes is
    put back; the body cannot change; deletion-policy retain leaves the
    key; delete deletes it
--- PASS: TestStatusDefaultAndBodyAreNoDrift, TestStatusIsCompared
          (SSH public key unit tests)
--- PASS: TestAccountAlias
    the alias is set and settles (no update across three resyncs); an
    alias set outside Kubernetes is put back; the CRD refuses what IAM
    would; the alias cannot change; deletion-policy retain leaves it;
    delete deletes it
--- PASS: TestSameAliasIsNoDrift (account alias unit test)
--- PASS: TestAccountPasswordPolicy
    the policy is set, IAM's defaults are no drift, and it settles (no
    update across three resyncs); a changed length is put; a change
    outside Kubernetes is put back; the CRD refuses what IAM would;
    deletion-policy retain leaves the policy; delete deletes it
--- PASS: TestUnsetFieldsAreNoDrift, TestSetFieldsAreCompared
          (password policy unit tests)
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
--- PASS: TestSchedule
    a schedule is made in the referenced group and settles (no update
    across three resyncs: Scheduler's defaults are no drift); a changed
    expression and state are updated; the name cannot change;
    deletion-policy retain leaves the schedule; delete deletes it
--- PASS: TestDefaultsAreNoDrift, TestSetStateIsCompared
          (schedule unit tests)
--- PASS: TestScheduleGroup
    a group is made, tagged, and settles (no update across three
    resyncs); its tags are synced; the name cannot change;
    deletion-policy retain leaves the group; delete deletes it
--- PASS: TestOurServices, TestReconcileBeside
          (the operator runs the scheduler manager beside ACK's
          EventBridge controller, with its identity, and removes it
          with that controller)
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
--- PASS: TestWebACLAssociation
    the referenced web ACL (ACK's WebACL, its ARN from status) is
    associated and settles (no update across three resyncs); the
    resource cannot change; a disassociation outside Kubernetes is put
    back; deletion-policy retain leaves the association; delete
    disassociates
--- PASS: TestOtherWebACLIsDrift (web ACL association unit test)
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
- **Access keys** (probed 2026-11-09): moto answers as AWS does here: a
  40-character secret returned only by the create, a third key for one
  user refused (`LimitExceeded`), and `NoSuchEntity` for an unknown user's
  `ListAccessKeys` and for a key deleted twice. It does not enforce the
  key: any key signs a moto request.
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
  `EnableVgwRoutePropagation` or `DisableVgwRoutePropagation` (server
  errors, probed 2026-11-09), so `VPNGatewayRoutePropagation` is unproven
  on the fake (section 6.34).
- **EC2 VPN connections** (probed 2026-11-09). moto makes a connection
  `available` at once (AWS goes through `pending` for minutes), keeps a
  deleted one listed in state `deleted`, as AWS does, deletes one twice
  without an error, and answers `InvalidVpnConnectionID.NotFound` for an
  unknown ID. It returns no `Options` at all (AWS returns every tunnel's
  options, its outside IP address and its pre-shared key) and no device
  configuration with keys in it. It has none of
  `ModifyVpnTunnelOptions`, `ModifyVpnConnectionOptions`,
  `ModifyVpnConnection` or `CreateVpnConnectionRoute` (server errors).
- **EC2 network interface permissions** (probed 2026-11-09). moto has
  none of `CreateNetworkInterfacePermission`,
  `DescribeNetworkInterfacePermissions` or
  `DeleteNetworkInterfacePermission` (a server error), so the kind is
  unproven on the fake.
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
- **EventBridge Scheduler groups.** moto makes a group `ACTIVE` and
  deletes it at once (AWS goes through `DELETING`, and deletes the
  group's schedules), and keeps its tags.
- **EventBridge Scheduler schedules.** moto does not check that a
  schedule's target or role exist, or that the role trusts Scheduler; it
  fills in the same defaults AWS does (UTC, `ENABLED`, the retry
  policy). It never runs a schedule.
- **EventBridge API destinations.** moto marks a destination `ACTIVE` at
  once and, when the spec leaves the rate limit out, reports none (AWS
  reports 300 per second). A unit test shows that default is no drift.
- **WAFv2 associations.** moto keeps a web ACL's list of resources. It
  does not check that the resource exists, and associating a resource
  with a second web ACL adds it there without taking it off the first
  (AWS replaces the association).
- **IAM login profiles.** moto stores the password, which no IAM API
  returns, so the test cannot read it back. It ignores
  `PasswordResetRequired` on create (the update sets it) and does not
  check the account's password policy.
- **IAM SSH public keys.** moto does not parse the key body (IAM
  refuses one that is not a valid RSA key of 2048 bits or more), returns
  it as uploaded, and does not refuse a key the user already has.
- **IAM account alias.** moto replaces the alias on create, as AWS
  does, and does not check that another account already has it (AWS
  answers `EntityAlreadyExists`).
- **IAM password policy.** moto stores the policy and fills IAM's
  defaults for fields left out, as AWS does; it checks the lengths and
  ages IAM limits.
- **IAM virtual MFA devices** (probed 2026-11-09). moto returns the seed
  and the QR code from the create, as AWS does (the seed 64 Base32
  characters; the "QR code" 64 random bytes, not a PNG), refuses a second device of the same path and
  name (`EntityAlreadyExists`), pages the list at 100 with a marker, and
  answers `NoSuchEntity` for a device deleted twice. It differs from AWS
  in four ways: its `ListVirtualMFADevices` returns every device's seed
  and QR code again (AWS returns neither after the create; the kind
  ignores both fields, so nothing reads them); it deletes a device that is
  enabled for a user (AWS answers `DeleteConflict`); its
  `EnableMFADevice` takes any two six-digit codes; and it has none of the
  MFA device tag calls nor `GetMFADevice` (a server error).
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
- **Organizations policies.**
  - moto stores any text as a policy's content, whatever its type, and
    returns it re-serialised (`json.dumps`: a space after each colon and
    comma), where AWS returns the text as given. It creates a policy of
    type `TAG_POLICY`, `AISERVICES_OPT_OUT_POLICY` and
    `RESOURCE_CONTROL_POLICY`, answers `InvalidInputException` for
    `DECLARATIVE_POLICY_EC2` and `CHATBOT_POLICY`, and a server error for
    a `BACKUP_POLICY` with an empty document.
  - It refuses a second policy with the same name
    (`DuplicatePolicyException`), as AWS does, keeps tags and
    `UpdatePolicy`'s name, description and content, and answers
    `PolicyNotFoundException` for an unknown id.
  - It needs an organization first (`CreateOrganization`); the test
    makes one with the SDK.
  - It accepts a second `AttachPolicy` of the same policy to the same
    target and a second `DetachPolicy` (AWS answers
    `DuplicatePolicyAttachmentException` and `PolicyNotAttachedException`),
    does not check that the policy type is enabled on the root, and lets
    the last SCP be detached. It answers `OrganizationalUnitNotFoundException`
    for an unknown OU, and attaches `FullAWSAccess` to the root when the
    organization is made, as AWS does.
  - Delegated administrators: moto refuses the management account
    (`ConstraintViolationException`), a second registration
    (`AccountAlreadyRegisteredException`), a second deregistration
    (`AccountNotRegisteredException`) and a principal it does not know
    (`InvalidInputException`, including `sso.amazonaws.com`), as AWS does
    for the first three. It does not require trusted access for the
    service, and takes more than one administrator for a service.
  - Organizations (probed 2026-11-09): `DescribeOrganization` answers
    `AWSOrganizationsNotInUseException` with no organization, and
    `CreateOrganization` a second time `AlreadyInOrganizationException`,
    as AWS does. A new organization's root has no policy type enabled
    (`ListRoots`), while `DescribeOrganization`'s deprecated
    `AvailablePolicyTypes` says SCPs are. `EnablePolicyType` twice answers
    `PolicyTypeAlreadyEnabledException`, `DisablePolicyType` of a type not
    enabled `PolicyTypeNotEnabledException`, as AWS does; moto enables
    SCPs and trusted access on a consolidated-billing organization, which
    AWS refuses, and never reports a `PENDING_ENABLE` status.
    `EnableAWSServiceAccess` and `DisableAWSServiceAccess` are idempotent;
    moto refuses a principal it does not know (`InvalidInputException`)
    and accepts `sso.amazonaws.com`. `DeleteOrganization` deletes an
    organization with no member accounts and refuses one with members
    (`OrganizationNotEmptyException`), as AWS does. `EnableAllFeatures`
    is not implemented.
  - Account moves: moto's `MoveAccount` does not check that the account
    is in `SourceParentId`, and moves an account to the parent it is
    already in (AWS answers `SourceParentNotFoundException` and
    `DuplicateAccountException`); for an unknown parent it answers
    `ParentNotFoundException` (AWS: `SourceParentNotFoundException` or
    `DestinationParentNotFoundException`). `ListParents` and
    `DescribeAccount` answer as AWS does, `AccountNotFoundException` for
    an unknown account (AWS answers `ListParents` with
    `ChildNotFoundException`).
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
| 10 | EventBridge Scheduler `Schedule` and `ScheduleGroup`; EventBridge `Connection` and `ApiDestination` | Scheduler: no controller (`community` issue #2784). `Connection` and `ApiDestination`: ignored in eventbridge-controller `v1.9.2`. | yes | `Connection` and `ApiDestination` built; Scheduler: `ScheduleGroup` and `Schedule` built, beside EventBridge's controller (open question 8, answered) |
| 11 | ECR `ReplicationConfiguration`; `RegistryPolicy`, `RegistryScanningConfiguration` | `ReplicationConfiguration`: ecr-controller PR #121, open, conflicts with base, no review, last updated 2026-10-18. The others: none. | yes (one replication rule at most) | built: `ReplicationConfiguration` (PR #121's API; rechecked 2026-11-09: still open, conflicting, no review, head `f6c7d57e`). `RegistryPolicy` and `RegistryScanningConfiguration` next |
| 12 | WAFv2 `WebACLAssociation` | none (wafv2-controller `v1.8.1`; no pull request or issue, rechecked 2026-11-09) | yes | built |
| 13 | ElastiCache `GlobalReplicationGroup` | elasticache-controller PR #237: mergeable, active 2026-11-06 | none | skipped: may be close to merging, and moto has none of it |
| 14 | Route 53 DNSSEC and `KeySigningKey`, VPC association authorization | ignored in route53-controller's `generator.yaml` | none | skipped: moto has none of it |
| 15 | IAM `GroupMembership`, `AccessKey`, `LoginProfile`, `SSHPublicKey`, account alias, password policy | none (iam-controller `v1.11.3`; PR #147 for `LoginProfile` closed; rechecked 2026-11-09) | yes | built: `UserGroupMembership`, `LoginProfile`, `SSHPublicKey`, `AccountAlias`, `AccountPasswordPolicy`; `AccessKey` (6.30; open question 10, answered) |
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

### 6.17 `wafv2.aws.hfl.dev` `WebACLAssociation`

A regional web ACL associated with a resource it protects: an
Application Load Balancer, an API Gateway REST API stage, an AppSync API,
a Cognito user pool, an App Runner service or a Verified Access instance.
It is Terraform's `aws_wafv2_web_acl_association`. ACK's wafv2-controller
`v1.8.1` has `WebACL`, `RuleGroup` and `IPSet`, and no association; no
pull request or issue adds one (rechecked 2026-11-09). A CloudFront
distribution names its web ACL in its own configuration instead.

**Hand-written** on ACK's runtime, on PR #184's skeleton:
`AssociateWebACL`, `GetWebACLForResource` and `DisassociateWebACL` are no
create, describe and delete the generator reads. A new service,
`services/wafv2`, where the generator ignores every resource it finds.

- **Spec:** `resourceARN`, and `webACLARN` or `webACLRef` (ACK's own
  `WebACL`, its `status.ackResourceMetadata.arn`; wafv2-controller
  `v1.8.1` is a Go module here for its API types). Both are immutable,
  as in Terraform.
- **Read:** `GetWebACLForResource`. No web ACL, or no resource, is not
  found, so a disassociation outside Kubernetes is associated again.
  Another web ACL put on the resource outside Kubernetes is drift, and
  the update associates the spec's again (`AssociateWebACL` replaces).
- **Delete** disassociates only if the resource still has the spec's
  web ACL; one since given another web ACL is left alone.
- **Synced** once read: an association has no pending state.
  `status.webACLName` names the web ACL. WAFv2 has no ARN for an
  association; a WAFv2 ARN form with the resource's ARN appended stands
  in, for ACK's metadata only.
- **Errors:** a resource or web ACL not there yet is retried; a bad
  parameter or operation (a `CLOUDFRONT` web ACL, a resource WAFv2 cannot
  protect) is terminal.
- **No `resourceRef`.** The resources live in other ACK controllers
  (elbv2, apigateway, cognito); `resourceARN` takes the ARN.

**Not proven:** another web ACL put on the resource outside Kubernetes
being replaced (moto keeps both), the delete leaving such a resource
alone, that the resource exists (moto does not check), and the
terminal errors.
### 6.18 `iam.aws.hfl.dev` `UserGroupMembership`

An IAM user's membership of a list of groups: Terraform's
`aws_iam_user_group_membership`. ACK's iam-controller `v1.11.3` has
`User` and `Group` but no membership; no pull request adds one
(rechecked 2026-11-09).

**Not exclusive (assumed; open question 9).** Only the listed groups are
added or removed. Groups the user is in for any other reason (another
object, the console, Terraform) are left alone. `status.groups`
remembers what this object put, so a group taken off the list is
removed and nothing else is.

**Hand-written** on ACK's runtime, on PR #184's skeleton: IAM has
`AddUserToGroup`, `RemoveUserFromGroup` and `ListGroupsForUser`, no
create, describe and delete of a membership.

- **Spec:** `userName` or `userRef` (ACK's `User`, its `spec.name`),
  immutable; `groups` or `groupRefs` (ACK's `Group`s), which may change.
- **Read:** the user's groups, kept to those listed or put before. The
  groups compare as a set. A user that does not exist has no membership;
  so has one never made whose groups the user is in none of. Once made,
  a membership with all its groups gone outside Kubernetes is put back.
- **Delete** takes the user out of the listed and put groups; a user,
  group or membership already gone is removed.
- **Errors:** a user or group not there yet is retried; IAM's limit of
  groups per user (`LimitExceeded`) is terminal.
- **Synced** once read. IAM has no ARN for a membership; the IAM ARN form
  with the user appended stands in, for ACK's metadata only.

**Not proven:** IAM's limit of ten groups per user (not tested), and
real AWS.
### 6.19 `iam.aws.hfl.dev` `LoginProfile`

An IAM user's console password: Terraform's `aws_iam_user_login_profile`,
with the password from a Kubernetes Secret instead of a generated one.
ACK's iam-controller `v1.11.3` has none; its pull request #147 was closed
(rechecked 2026-11-09).

**Generated** from `CreateLoginProfile`, `GetLoginProfile`,
`UpdateLoginProfile` and `DeleteLoginProfile`.

- **Spec:** `userName` (required, immutable), `password` (a
  `SecretKeyReference`, required) and `passwordResetRequired`.
- **The password is never read back** (IAM returns none), so it is no
  drift; the spec's stays. A changed Secret is not pushed until the spec
  changes, as for `Connection`.
- **Server default is no drift:** IAM reports `passwordResetRequired`
  false when the spec leaves it out (`custom_delta.go`).
- **Not found:** `NoSuchEntity`. No tags: a login profile has none.
- **No `userRef`.** ACK's `User` is in the same service, and a generated
  reference to it is the case of section 8, item 10.

**Not proven:** that the password reached IAM (no API returns it), the
account's password policy refusing a password, and a changed Secret.
### 6.20 `iam.aws.hfl.dev` `SSHPublicKey`

An IAM user's SSH public key, for AWS CodeCommit: Terraform's
`aws_iam_user_ssh_key`. ACK's iam-controller `v1.11.3` has none, and no
pull request adds one (rechecked 2026-11-09).

**Hand-written** on ACK's runtime, on PR #184's skeleton: the key is made
with `UploadSSHPublicKey`, which the generator does not read as a
create, and it is known by an ID IAM gives it.

- **Spec:** `userName` or `userRef` (ACK's `User`), and
  `sshPublicKeyBody`, both immutable; `status`, `Active` (the default) or
  `Inactive`.
- **Status:** `sshPublicKeyID` (the key's name in CodeCommit and SSH
  configuration), `fingerprint`, `uploadDate`.
- **Read:** `GetSSHPublicKey` by the ID. The body is not compared: it is
  immutable, and IAM may return it in another form. The status is, so a
  key reactivated outside Kubernetes is set back.
- **Update** sets the status (`UpdateSSHPublicKey`). **Delete** deletes
  the key; a key or user already gone is deleted.
- **Errors:** a user not there yet is retried; a malformed or duplicate
  key, or a user at IAM's limit of keys, is terminal.
- **Synced** once read. IAM has no ARN for a key; the IAM ARN form with
  the user and key ID stands in, for ACK's metadata only.

**Not proven:** IAM parsing the key (moto does not), a duplicate key
being refused, and real AWS.
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
### 6.22 `iam.aws.hfl.dev` `AccountPasswordPolicy`

The AWS account's password policy for IAM users: Terraform's
`aws_iam_account_password_policy`. ACK's iam-controller `v1.11.3` has
none, and no pull request adds one (rechecked 2026-11-09).

**Hand-written** on ACK's runtime, on PR #184's skeleton: an
account-wide setting (`UpdateAccountPasswordPolicy`,
`GetAccountPasswordPolicy`, `DeleteAccountPasswordPolicy`), not a
resource the generator reads.

- **Spec:** the nine settings, each optional, with IAM's limits in the
  CRD. A field left out keeps IAM's default and is no drift; IAM reports
  every field.
- **Update** puts the whole policy (`UpdateAccountPasswordPolicy`
  replaces it), also when changed outside Kubernetes.
- **Delete** deletes the policy, so IAM's defaults apply.
- **One per account.** An account's existing policy is taken over by a
  new object (its first read finds nothing, so the create puts the
  policy, instead of ACK's "already exists"), and two objects would put theirs in turn, as for
  `AccountAlias`.
- **Synced** once read; `status.expirePasswords` as IAM reports it.

**Not proven:** real AWS.

### 6.23 `scheduler.aws.hfl.dev` `ScheduleGroup`

An EventBridge Scheduler schedule group. ACK has no Scheduler controller
(`community` issue #2784). The manager runs beside ACK's EventBridge
controller with its identity (open question 8, answered;
section 8, item 12).

**Generated** from `CreateScheduleGroup`, `GetScheduleGroup` and
`DeleteScheduleGroup`, in a new service, `services/scheduler`.

- **Tags:** a group has no update of its own; its tags are synced with
  `TagResource` and `UntagResource` (`customUpdate` and the tag
  comparison, copied from eventbridge-controller's `EventBus`).
  ACK's runtime adds its `services.k8s.aws/` tags, as for ACK's own
  kinds.
- **Immutable:** the name. **Not found:** `ResourceNotFoundException`.
- **The SDK client** is `service/scheduler` `v1.12.13`, the version in
  the aws-sdk-go-v2 release whose models the generator reads
  (`v1.34.0`), since no ACK controller pins one.

**Not proven:** the `DELETING` state, and that deleting a group deletes
its schedules (moto does both at once).

### 6.24 `scheduler.aws.hfl.dev` `Schedule`

An EventBridge Scheduler schedule: a cron, rate or one-time expression
that invokes a target (an SQS queue, a Lambda function, an ECS task, or
any AWS API through the universal target) as a role. ACK has no
Scheduler controller; the manager runs beside ACK's EventBridge
controller (section 6.23).

**Generated** from `CreateSchedule`, `GetSchedule`, `UpdateSchedule` and
`DeleteSchedule`.

- **Reference:** `groupRef` names our `ScheduleGroup`; without a group,
  the schedule is in `default`.
- **Immutable:** the name and the group.
- **Update** puts the whole schedule (`UpdateSchedule` replaces it).
- **Server defaults are no drift:** the group, the time zone (UTC), the
  state (`ENABLED`), the action after completion and the target's retry
  policy (`custom_delta.go`).
- **Not found:** `ResourceNotFoundException`. No tags: a schedule has
  none.
- **The role** the schedule invokes its target as must trust
  `scheduler.amazonaws.com`, and the EventBridge controller's role needs
  `iam:PassRole` on it (section 8, item 12).

**Not proven:** that the target and role exist and the role is trusted
(moto checks none of it), a schedule running, and real AWS.

### 6.25 `organizations.aws.hfl.dev` `Policy`

An AWS Organizations policy: a service control policy, resource control
policy, tag policy, backup policy or AI services opt-out policy. This is
the first item of `docs/coverage/foundation.md` (F01), the first
priority: declare an SCP. ACK's organizations-controller `v1.7.0` has `Account`
and `OrganizationalUnit` and ignores `Policy`, `Organization` and
`GovCloudAccount`. Rechecked read-only on 2026-11-09: no release after
`v1.7.0` (2026-10-23), no open pull request (the two OU-reference pull
requests, #71 and #76, are closed), community #2884 (`Policy`,
`PolicyAttachment`, `DelegatedAdministrator`, opened 2026-05-11) has no
pull request, and #2369 (`MoveAccount`) is open. So the kinds are built
here; recheck #2884 before each of the next ones.

**Generated** from `CreatePolicy`, `DescribePolicy`, `UpdatePolicy` and
`DeletePolicy`, in a new service, `services/organizations`; every
resource ACK has is ignored in its `generator.yaml`. The SDK client is
`service/organizations` `v1.37.4`, the version in the organizations-controller
`v1.7.0` `go.mod`.

- **The response wraps the policy** in a `PolicySummary` (id, ARN, name,
  description, type) beside the `content`, so the generator cannot read
  the identifier or the spec's name, description and type back. The
  identifier is a field made `from` `Policy.PolicySummary.Id`, and
  the hooks (`hooks.go`) set the id, the ARN and the spec fields from the
  summary on create and read. `status.policySummary` keeps the whole
  summary, including `awsManaged`.
- **The document** is compared as JSON (`custom_delta.go`), so key order
  and white space are no drift. Statement order, and a string against a
  one-element list, are drift: AWS keeps the text as given, and the
  comparison does not normalise IAM's two spellings.
- **Tags** are read with `ListTagsForResource` and synced with
  `TagResource` and `UntagResource` (copied from eventbridge-controller's
  `EventBus`); a change of tags alone does not call `UpdatePolicy`.
- **Immutable:** the type. The name, description and document update with
  `UpdatePolicy`. **Not found:** `PolicyNotFoundException`. **Terminal:**
  `InvalidInputException`, `MalformedPolicyDocumentException`,
  `DuplicatePolicyException`.
- **Which account.** Organizations calls are made in the management
  account, or a delegated administrator's, by the identity the manager
  shares with ACK's organizations-controller (section 7). Nothing in the
  kind names an account. See open question 11.
- **An AWS-managed policy** (`FullAWSAccess`) cannot be updated or
  deleted; adopting one is read-only, and deleting the object with
  `deletion-policy: retain` leaves it. Not tried (moto has none).
- **Deleting a policy that is attached** fails with `PolicyInUseException`
  until `PolicyAttachment` (next) or the console detaches it.

**Not proven on moto:** that AWS refuses a malformed document or one that
breaks the type's rules (moto stores any text); that AWS keeps the text as
given (moto re-serialises the JSON with its own white space; the tests
compare documents, and the JSON comparison is what keeps the object
settled); `DECLARATIVE_POLICY_EC2` and `CHATBOT_POLICY` (moto answers
`InvalidInputException`); `BACKUP_POLICY` (moto answers a server error to
an empty document); that a policy type must be enabled on the root before
a policy of that type can be attached; real AWS.

### 6.26 `organizations.aws.hfl.dev` `PolicyAttachment`

One Organizations policy attached to one target: the root, an OU or an
account. F01's second kind. Upstream rechecked 2026-11-09: as in section
6.25 (no release, no pull request; community #2884 still only proposes it).

**Generated**, from operations the generator does not find by itself,
mapped in `generator.yaml` as ACK maps its own Put-shaped kinds:
`AttachPolicy` is the create, `DetachPolicy` the delete, and
`ListTargetsForPolicy` the read, matched on `targetID` (`list_operation:
match_fields`). Roughly 90% of the new Go is generated; written here are
two hook templates (paging and an idempotent detach, `hooks.go`, about 60
lines), the alias that lets the generated references read ACK's kind, and
the scheme registration.

- **References:** `policyRef` names our `Policy` (its `status.id`);
  `targetRef` names ACK's `OrganizationalUnit` (`organizations.services.k8s.aws`,
  its `status.id`). ACK's generator writes a reference to a kind of the
  same service as a type of this package, so
  `apis/v1alpha1/ack_kinds.go` aliases ACK's type (the problem section 8,
  item 10 describes, solved by an alias rather than a rewrite). An account
  or the root is given by `targetID`; one field can reference one kind,
  and ACK's `Account` is not referenced yet.
- **Non-exclusive:** it attaches its policy and detaches only that one.
  The target's other policies (`FullAWSAccess` on the root, policies from
  other objects, the console) are left alone. CloudFormation's
  `Policy.TargetIds` is the exclusive form and is not built.
- **Immutable:** the policy and the target. **Read:** `ListTargetsForPolicy`,
  every page (Organizations returns at most 20). **Not found:**
  `PolicyNotFoundException`. **Delete** treats `PolicyNotAttachedException`
  and a gone policy, account, OU or target as done. **Terminal:**
  `InvalidInputException`, `PolicyTypeNotEnabledException` (enable the
  type on the root first: `Organization`, next).
- **`status.ackResourceMetadata.arn`** is the target's ARN (the generator
  sets it from the listed target).
- **Two objects for one attachment** fight: deleting one detaches it and
  the other attaches it again on its next resync (the test shows this).

**Not proven on moto:** that AWS refuses a second attach
(`DuplicatePolicyAttachmentException`; moto accepts it) and a second detach
(`PolicyNotAttachedException`; moto accepts it), so the idempotent detach
is unit-tested; that a policy type must be enabled on the root before a
policy of that type is attached (moto does not check); that the last SCP
cannot be detached from a target (AWS refuses, `ConstraintViolationException`);
real AWS.

### 6.27 `organizations.aws.hfl.dev` `DelegatedAdministrator`

A member account registered as the delegated administrator of an AWS
service for the organization: the account a security team runs GuardDuty,
Security Hub, Config or Identity Center from. F01's third kind. Upstream
rechecked 2026-11-09: no release after organizations-controller `v1.7.0`,
no pull request; community #2884 still only proposes it.

**Generated**, from operations mapped in `generator.yaml`:
`RegisterDelegatedAdministrator` is the create,
`DeregisterDelegatedAdministrator` the delete, and
`ListDelegatedAdministrators` the read, filtered by `servicePrincipal` and
matched on `accountID` (the list's `Id`, renamed). About 90% of the new
Go is generated; written here are two hook templates (paging, and an
idempotent deregister, `hooks.go`), the alias of ACK's `Account`
(`apis/v1alpha1/ack_kinds.go`) and the scheme registration.

- **Reference:** `accountRef` names ACK's `Account`
  (`organizations.services.k8s.aws`, its `status.accountID`), through a
  type alias, as `PolicyAttachment`'s `targetRef` does for ACK's OU. The
  SDK's own `Account` shape is ignored so the alias does not clash.
- **Immutable:** the account and the service principal. **Delete** treats
  `AccountNotRegisteredException` and a gone account as done.
  **Terminal:** `InvalidInputException` (an unknown principal),
  `AccountAlreadyRegisteredException`, `ConstraintViolationException` (the
  management account, or a service's limit of one administrator).
- **`status.ackResourceMetadata.arn`** is the account's ARN.
- **Trusted access first:** AWS requires the service's trusted access
  (`EnableAWSServiceAccess`) before most services accept a delegated
  administrator; that is `Organization.awsServiceAccessPrincipals`
  (6.28). moto does not require it.

**Not proven on moto:** that trusted access must be enabled first; each
service's own limit on administrators (moto takes several for GuardDuty;
AWS takes one); the principals moto does not know (it refuses
`sso.amazonaws.com`, which AWS accepts); real AWS.

### 6.28 `organizations.aws.hfl.dev` `Organization`

The account's organization: its feature set, the policy types enabled on
its root, and the AWS services given trusted access. F01's fourth kind.
Upstream rechecked 2026-11-09: organizations-controller `v1.7.0` ignores
`Organization` in its `generator.yaml`; no pull request or issue proposes
it (community #2884 covers policies and delegated administrators only).

**Generated**, from `CreateOrganization` (the create) and
`DescribeOrganization` (the read). About 83% of the new Go is generated;
written here are `hooks.go` (the adoption, the two lists' read and sync,
the update, the set comparison: about 280 lines), four short hook
templates, and the admission policy.

- **Adopted, or made.** `DescribeOrganization` takes no input: it finds
  the account's organization. As for the other account-wide singletons
  (`docs/handoff.md`), an object's read finds nothing until it has an ARN
  in its status, so the runtime calls the create, and the create adopts
  the existing organization (its lists synced to the spec) and calls
  `CreateOrganization` only when the account has none. Two objects adopt
  the same organization and fight over the lists; make one.
- **Never deleted** (question 12, answered): `DeleteOrganization` is
  ignored in `generator.yaml`, so the controller has no code that calls it
  and deleting the object, whatever its deletion policy, only stops
  managing the organization. The policy types and trusted access stay as
  they are. As well, `config/admission/retain-only.yaml`, a
  ValidatingAdmissionPolicy (CEL in the API server, Kubernetes 1.30 or
  later, no webhook server) installed with `config/default`, refuses an
  Organization without ACK's annotation
  `services.k8s.aws/deletion-policy: retain` (the key and value the
  runtime reads, `apis/core/v1alpha1/annotations.go` at `v0.67.0`) and any
  update that changes or removes it, so the object says what deleting it
  does. Objects being deleted are not checked, so one made before the
  policy can still be deleted. A MutatingAdmissionPolicy could set the
  annotation itself, but it is not GA on Kubernetes 1.35 (envtest's here)
  or the older versions EKS supports, so it is required, and the message
  says how. The policy lists retain-only kinds by resource: a later kind
  that cannot be deleted (an account's closure, root-level settings) joins
  by adding its resource there.
- **`featureSet`** (`ALL` or `CONSOLIDATED_BILLING`) is immutable (CEL
  `self == oldSelf`). Unset, it takes the organization's (late
  initialization); set to something else on adoption, the object is
  terminal (`EnableAllFeatures` is a handshake every member account
  accepts; not done from Kubernetes).
- **`enabledPolicyTypes`** (the root's: `EnablePolicyType`,
  `DisablePolicyType`, read with `ListRoots`; `PENDING_ENABLE` counts as
  enabled) and **`awsServiceAccessPrincipals`** (`EnableAWSServiceAccess`,
  `DisableAWSServiceAccess`, read with
  `ListAWSServiceAccessForOrganization`) are **additive** (decision
  A3, 2026-11-10; this reverses the exclusive lists first built): compared
  as sets, what is listed and missing is enabled, and an entry enabled
  outside Kubernetes is kept, never disabled, and reported for import.
  An out-of-band action is treated as additive and flagged for import
  when taking an asset inventory and giving adoption recommendations. A listed entry disabled
  outside Kubernetes is still put back.
  - **Ownership.** The object disables only what it owns:
    `status.ownedEnabledPolicyTypes` and
    `status.ownedAWSServiceAccessPrincipals`, the entries it enabled or
    found enabled while they were listed. Listing an entry that is
    already enabled takes it on (importing it), so taking it off the list
    later disables it. An owned entry no longer listed is disabled; once
    gone (or disabled outside Kubernetes) it is let go.
  - **Reported.** `status.unmanagedEnabledPolicyTypes` and
    `status.unmanagedAWSServiceAccessPrincipals` list what is enabled and
    neither listed nor owned: the import candidates Bundt offers. The
    shape is the same for any list made additive here: beside spec list
    `X`, `status.owned<X>` and `status.unmanaged<X>`, sorted, left out
    when empty.
  - **How.** `planList` (`hooks.go`) is the whole rule. The read puts in
    the spec's list only the enabled entries that are listed or owned, so
    the comparison sees what to enable and what to disable and never an
    unmanaged entry, and the runtime never writes one into the spec.
  - An unset or empty list manages nothing (an empty list cannot be told
    from an unset one once it has been through the API types): nothing is
    enabled or disabled, the object owns nothing, and everything enabled
    is reported unmanaged. So the last entry cannot be disabled from here.
    Ownership lives in the status: an object restored without its status
    owns nothing until the next sync, so it disables nothing it should
    not (the safe direction).
  - **Disabling is destructive:** disabling a policy type detaches every
    policy of that type in the organization, and disabling trusted access
    stops that service's organization features. Only owned entries are
    ever disabled.
- **Status:** `id`, `rootID` (for a `PolicyAttachment` to the root), the
  management account's ID, ARN and email; the owned and unmanaged lists
  above; `ackResourceMetadata.arn` is
  the organization's ARN. **Terminal:** `InvalidInputException` (an
  unknown principal or type), `ConstraintViolationException`,
  `AccessDeniedForDependencyException`,
  `PolicyTypeNotAvailableForOrganizationException` (a policy type on a
  consolidated-billing organization).

**Proven on moto** (`TestOrganizationsOrganization`): with no
organization one is made, with SCPs and GuardDuty's trusted access; an
object with `deletion-policy: delete`, made before the admission policy,
is deleted and the organization stays (moto deletes an empty organization,
so a `DeleteOrganization` would have shown); with the policy installed,
an object without the annotation, or with `delete`, is refused; an
existing organization is adopted, its feature set late-initialized, and
it settles; a changed or removed annotation and a changed feature set are
refused; the adopted entries are owned; a listed policy type disabled
outside Kubernetes is put back; a policy type and a principal enabled
outside Kubernetes are kept across resyncs and reported in the unmanaged
status lists; listing the unmanaged principal takes it on; owned entries
taken off the lists are disabled while the unmanaged policy type stays;
delete leaves the organization and its settings. `planList` is
unit-tested case by case (`hooks_test.go`). The admission policy is also
checked on kind (`tests/operator/run.sh`).

**Not proven on moto:** that AWS refuses SCPs and trusted access on a
consolidated-billing organization (moto allows both);
`PENDING_ENABLE`/`PENDING_DISABLE` (unit-tested); that disabling a
policy type detaches its policies; a delegated administrator's account
(its rights differ); real AWS.

### 6.29 `organizations.aws.hfl.dev` `AccountPlacement`

The OU (or the root) a member account is in. F01's fifth and last kind.
Upstream rechecked 2026-11-09: no release after organizations-controller
`v1.7.0`; ACK's `Account` has no parent field (`CreateAccount` takes
none) and nothing calls `MoveAccount`; community #2369 (open since 2025,
"How to support MoveAccount in organizations") asks for a field on
`Account` or a separate resource, with no pull request. CloudFormation
has it as `AWS::Organizations::Account`'s `ParentIds`. A field on ACK's
`Account` is ACK's to add; a sidecar here is the other option #2369
names, and moves to ACK if ACK adds either.

**Generated**, from operations mapped in `generator.yaml`: `MoveAccount`
is both the create and the update (`operation_type: [Create, Update]`),
`ListParents` the read (its one parent is `spec.parentID`). About 95% of
the new Go is generated (about 1,770 lines); written here are four
one-to-five-line hook templates and `hooks.go` (87 lines): the parent to
move from, the skip when the account is already there, and the account's
ARN.

- **Spec:** `accountID` or `accountRef` (ACK's `Account`, its
  `status.accountID`), immutable; `parentID` (a root `r-...` or an OU
  `ou-...`) or `parentRef` (ACK's `OrganizationalUnit`, its `status.id`).
  `Organization.status.rootID` (6.28) is the root's ID.
- **Placed, then kept there.** Until the object has placed the account
  (no ARN in its status), its read finds nothing, as for the other
  account-wide settings (`docs/handoff.md`); the create reads the parent
  the account is in now and calls `MoveAccount` from it, or, when the
  account is already there, nothing (AWS would refuse the move,
  `DuplicateAccountException`). It then reads the account's ARN
  (`DescribeAccount`) into `status.ackResourceMetadata.arn`. From then
  on the read is `ListParents`: a changed `parentID` moves the account
  (`SourceParentId` is the parent read), and a move outside Kubernetes is
  moved back.
- **Delete leaves the account where it is.** There is no delete call
  (none is mapped, so `sdkDelete` is a no-op): an account always has a
  parent, and moving it back to the root on delete would take it out
  from under its OU's policies, which is the opposite of what deleting a
  placement is for. **Assumed, for the builder to confirm:** the
  answer to question 12 (an organization is not deletable, so
  it is not treated as such) applied to a placement; likely and cheap.
  Following it, the kind joins the retain-only admission policy (6.28,
  `config/admission/retain-only.yaml`), so every object carries
  `services.k8s.aws/deletion-policy: retain`. Were the builder to want a
  delete to move the account back to the root, the kind would map
  `MoveAccount` as the delete too and leave the policy.
- **One object per account.** Two placements of one account move it
  back and forth (#2369 names this); make one.
- **Terminal:** `InvalidInputException`, `ConstraintViolationException`.
  An unknown account or parent is retried (it may be being made: ACK's
  `Account` and `OrganizationalUnit` are referenced once they have IDs).

**Proven on moto** (`TestOrganizationsAccountPlacement`): ACK's
`Account` moved into ACK's `OrganizationalUnit` by reference, the status
ARN the account's, and no create or update across resyncs; moved to the
root outside Kubernetes, it is moved back; a new `parentID` moves it; an
account already in its parent is placed (synced, ARN set); `accountID`
cannot change; delete leaves both accounts in their parents. With the
retain-only policy installed: an object without the annotation, or with
`delete`, is refused; one with `retain` places the account, its
annotation cannot be changed or removed, and deleting it leaves the
account in its OU. `tests/operator/run.sh` checks the refusal on kind.

**Not proven on moto:** that the create makes no `MoveAccount` call when
the account is already in place (moto accepts the call, so the test
shows only the outcome); AWS's `SourceParentNotFoundException` when the
account moved between the read and the move (retried, then read again);
the management account (AWS lets it move between OUs); real AWS.

### 6.30 `iam.aws.hfl.dev` `AccessKey`

An IAM user's access key (F03; rank 15). Built on the answer to
open question 10. Upstream rechecked 2026-11-09: iam-controller `v1.11.3`
ignores `AccessKey` in its `generator.yaml`; no pull request adds it;
community #1879 asks for a way to get a key's secret. Cloud Control cannot
serve it either (`AWS::IAM::AccessKey` is not provisionable,
`docs/cloudcontrol.md` section 8, PR #36), so it is built here on ACK's
generator.

**Generated** from `CreateAccessKey` (create), `ListAccessKeys` (read,
matched on the key's ID, `list_operation.match_fields`) and
`DeleteAccessKey`. About 85% of the new Go is generated (about 1,590
lines); written here are four hook templates and `hooks.go` (about 220
lines: the Secret). `UpdateAccessKey` is ignored and a custom update
refuses every change.

- **Spec:** `userName` or `userRef` (ACK's `User`, its `spec.name`,
  through a type alias in `apis/v1alpha1/ack_kinds.go`), and
  `secretName`, both immutable. The create writes the default Secret
  name into `spec.secretName`: the CRD's rule (`self == oldSelf`) does not
  stop a field being set from unset, which would lose track of the
  Secret (the first `task check` run showed it). **Status:** `accessKeyID`, `createDate`,
  `status` (`Active`; the key's status is not managed).
- **The secret, once, into a Secret.** `CreateAccessKeyOutput`'s
  `SecretAccessKey` is ignored by the generator, so no generated code
  copies it anywhere. A hook right after the create writes it, with the
  key's ID, into a new Secret in the object's namespace, named
  `spec.secretName` or else the object's name, keys `AWS_ACCESS_KEY_ID`
  and `AWS_SECRET_ACCESS_KEY` (the names the AWS SDKs read, so a pod can
  take both with `envFrom`), `immutable: true`, owned by the object
  (controller reference, no `blockOwnerDeletion`), and clears the
  secret from the response. The manager makes the Secret with its own
  client (ACK's runtime gives a resource manager none that can create:
  `WriteToSecret` patches an existing Secret, as acm-controller's
  `AcmeExternalAccountBinding` uses it). The manager's role gains
  `secrets` `create` and `delete` (section 7.3).
- **Never written over.** A create whose Secret already exists is refused
  (terminal) before any key is made: if the object does not own it, name
  another; if it does, its key was deleted outside Kubernetes, and only a
  new object (or deleting the Secret) makes a new key. If the Secret
  cannot be made after the key was, the key is deleted (nobody could read
  its secret again) and the create is tried later; if that delete fails,
  the error is terminal and names the key, so that no more keys are made
  up to IAM's limit of two a user.
- **The Secret is the record.** The read, while the status has no key ID,
  takes it from the Secret the object owns, so a status write that failed
  after the create does not lead to a second key.
- **Never logged.** The secret is in no error, event or status; the
  manager's debug log in the tests does not contain it.
- **Delete** deletes the key, then the Secret if the object owns it (the
  owner reference does the same where garbage collection runs). A Secret
  deleted by hand is not made again; the key's secret is gone, so rotate.
- **Assumed, likely and cheap** (the answer named a spec field with a
  default, not the default): the default Secret name is the object's
  name, and the Secret's keys are `AWS_ACCESS_KEY_ID` and
  `AWS_SECRET_ACCESS_KEY`.

**Proven on moto** (`TestIAMAccessKey`): a key made for ACK's `User` by
reference, its Secret holding the ID in status and a 40-character
secret, owned by the object and immutable; no second create and the
Secret not written again across resyncs; a status cleared of the ID takes
it back from the Secret with no second key; the default written into
`spec.secretName`, which then cannot change; a
Secret not the object's is left untouched and no key is made; a key
deleted outside Kubernetes is not made again over its Secret; delete
removes the key and the Secret; the secret is in no manager log line, in
nothing of the object's and in no event.

**Not proven on moto:** a Secret that cannot be made after the key was
(the key deleted, then made again: the path needs the API server to
refuse a create that it accepted a moment before, and is unit-tested only
in its parts); IAM's eventual consistency for a new key; the owner
reference's garbage collection (envtest runs no garbage collector; the
explicit delete is what the test shows); real AWS.

### 6.31 `iam.aws.hfl.dev` `VirtualMFADevice`

A virtual MFA device (F03). Built on the decision of 2026-11-09,
closing PR #44: Cloud Control never returns a device's seed or QR code,
so a device made through it cannot be enrolled; this kind writes the seed
into a Kubernetes Secret, on `AccessKey`'s pattern (section 6.30).
Upstream rechecked 2026-11-09: iam-controller `v1.11.3` ignores
`VirtualMFADevice` in its `generator.yaml`, on master too; no pull request
or issue adds it.

**Generated** from `CreateVirtualMFADevice` (create),
`ListVirtualMFADevices` (read, matched on the serial number,
`list_operation.match_fields`) and `DeleteVirtualMFADevice`. About 82% of
the new Go is generated (about 1,460 lines); written here are six hook
templates and `hooks.go` (about 310 lines together: the Secret and the
list's pages). The create's `VirtualMFADeviceName` is renamed `name`, as
ACK's `User` and `Role` do. There is no update call, and a custom update
refuses every change.

- **Spec:** `name`, `path` and `secretName`, all immutable; the create
  writes the default Secret name into `spec.secretName`, as `AccessKey`
  does. **Status:** `serialNumber` (the device's ARN) and `enableDate`
  (set once a user enables it). The SDK's `User` shape is ignored, so the
  user a device is enabled for is not shown.
- **The seed, once, into a Secret.** `CreateVirtualMFADeviceOutput`'s
  `Base32StringSeed` and `QRCodePNG` are ignored by the generator, so no
  generated code copies them anywhere. A hook right after the create
  writes them, with the serial number, into a new Secret in the object's
  namespace, named `spec.secretName` or else the object's name, keys
  `serialNumber`, `base32StringSeed` and `qrCodePNG` (the QR code is
  part of the same response, so it costs nothing to keep),
  `immutable: true`, owned by the object, and clears both from the
  response. The manager makes the Secret with its own client, as
  `AccessKey` does.
- **Never written over**, **the Secret is the record**, **never logged**
  and **delete** as for `AccessKey`: a create whose Secret exists is
  refused (terminal) before any device is made; a Secret that cannot be
  made after the device was deletes the device; the read, while the
  status has no serial number, takes it from the Secret the object owns;
  delete deletes the device, then the Secret if the object owns it.
- **The list's pages.** `ListVirtualMFADevices` lists every device in the
  account, 100 a page. The generated read takes one page, so a hook reads
  on with the marker until a page holds the device.
- **Not this kind's:** enabling the device for a user. `EnableMFADevice`
  takes two consecutive codes computed from the seed, so it is a person's
  step, or a later kind's that reads the Secret. A device enabled for a
  user cannot be deleted (`DeleteConflict`): the delete fails and is
  retried, and the error says why, until the device is deactivated.
  Tags are not managed (the list does not return them, and moto has no
  MFA device tag calls).
- **Assumed, likely and cheap** (the decision named the seed and the QR
  code, not the names): the default Secret name is the object's name, and
  the Secret's keys are `serialNumber`, `base32StringSeed` and `qrCodePNG`.

**Proven on moto** (`TestIAMVirtualMFADevice`): a device made after 105
others, found on the list's second page; its Secret holding the serial
number, the seed (Base32 text) and the QR code, owned by the object and
immutable; no
second create and the Secret not written again across resyncs; a status
cleared of the serial number takes it back from the Secret with no second
device; the default written into `spec.secretName`, which then cannot
change; a Secret not the object's is left untouched and no device is made;
a device deleted outside Kubernetes is not made again over its Secret;
delete removes the device and the Secret; the seed is in no manager log
line, in nothing of the object's and in no event.

**Not proven on moto:** a delete refused for a device enabled for a user
(moto deletes it); a Secret that cannot be made after the device was (as
for `AccessKey`); the QR code (moto's is 64 random bytes, not a PNG);
the owner reference's garbage
collection; real AWS.

### 6.32 `ec2.aws.hfl.dev` `VPNConnection`

A site-to-site VPN connection (F05), handed over by the Cloud Control
route: it fits ACK's generator better, with references to our
`CustomerGateway` and `VPNGateway` and the tunnels' pre-shared keys from
Secrets. Upstream rechecked 2026-11-09: ec2-controller `v1.23.1` ignores
`VpnConnection` and `VpnConnectionRoute` in its `generator.yaml`, on master
too; community #1868 ("VPN resources", open since 2023) has no pull
request.

**Generated** from `CreateVpnConnection` (create), `DescribeVpnConnections`
(read) and `DeleteVpnConnection`, with ACK ec2-controller's tag hooks
(`InternetGateway`'s, through `VPNGateway`'s). About 82% of the new Go is
generated (about 2,240 lines); written here are four hook templates and
`hooks.go` (about 500 lines together: the update, the options' read and
the static routes).

- **Spec:** `type`, `customerGatewayID` or `customerGatewayRef` (our
  `CustomerGateway`), `vpnGatewayID` or `vpnGatewayRef` (our
  `VPNGateway`), `transitGatewayID`, all immutable; `options` (the
  connection's CIDRs, static-routes-only, acceleration, and per tunnel the
  inside CIDR, IKE and phase settings, logging and
  `preSharedKey`, a `SecretKeyReference`); `staticRoutes` (CIDRs, for a
  static-routes-only connection); `tags`. **Status:** `vpnConnectionID`,
  `state` (synced when `available`), `routes`, `vgwTelemetry` (each
  tunnel's outside IP address and status), `category`,
  `gatewayAssociationState`.
- **The pre-shared keys.** The generated create reads each tunnel's key
  from its Secret, as ACK's kinds read secrets. EC2 returns the keys in
  two places, every tunnel's options and the device configuration (an
  XML document for the customer's router); the read clears both from the
  response before anything else reads it, and the configuration is never
  a field. So a key is never in the object, a log or an event. EC2's keys
  are not compared with the Secrets: a changed Secret is pushed when its
  reference changes (a new name or key), not on its own.
- **The options' read.** EC2 returns the options in another shape and
  fills a default for every tunnel setting left out, so the read takes
  from EC2 only the fields the spec sets (tunnels by position), and the
  spec's key references. A setting changed outside Kubernetes is then
  drift and is put back. With no options from EC2 (moto), the read keeps
  the spec's.
- **Updates:** tags (`CreateTags`/`DeleteTags`); static routes
  (`CreateVpnConnectionRoute`/`DeleteVpnConnectionRoute`, read from the
  connection's routes of source `Static`); the connection's four CIDRs
  (`ModifyVpnConnectionOptions`); each changed tunnel
  (`ModifyVpnTunnelOptions`, by the tunnel's outside IP address read from
  EC2, its key read from its Secret). Acceleration, static-routes-only,
  the inside IP version, the outside IP address type, the transport
  attachment and the number of tunnels are refused (terminal): EC2 cannot
  change them. Changing the gateways (`ModifyVpnConnection`) is not
  offered: the fields are immutable, and a new connection is the way.
- **Not built:** a reference to ACK's `TransitGateway` (`transitGatewayID`
  is a plain ID): it needs ACK's ec2-controller as a Go module, a new
  upstream dependency (open question 16). The newer
  `PreSharedKeyStorage` (keys in Secrets Manager) and
  `VpnConcentratorId` are not in the code generator's pinned EC2 model.

**Proven on moto** (`TestVPNConnection`): a connection made from our
`CustomerGateway` and `VPNGateway` by reference, its keys from a Secret,
with its type, gateways and tags in the cloud, `available`; no second
create and no update across resyncs; the keys in no manager log line, in
nothing of the object's and in no event; tags changed; the type refused
by the CRD; delete leaves the connection `deleted` in the cloud.

**Not proven on moto** (unit-tested in their parts, `hooks_test.go`):
the options' read against what EC2 returns (moto returns none), tunnel
option and CIDR changes, static routes, that the keys reach EC2 (moto
returns nothing to check them by), `pending` before `available`, and real
AWS. The foundation queue's "done when" asked for a tunnel option change
end to end; moto cannot show it.

### 6.33 `ec2.aws.hfl.dev` `NetworkInterfacePermission`

Another account's, or an AWS service's, permission on one of this
account's network interfaces (F04/F05's networking gaps; one of the two
foundation types Cloud Control cannot provision, `docs/cloudcontrol.md`
section 8, handed to this route). Upstream rechecked 2026-11-09:
ec2-controller `v1.23.1` ignores `NetworkInterfacePermission` in its
`generator.yaml`; no pull request or issue adds it.

**Generated**, all of it but one read hook: `CreateNetworkInterfacePermission`
(its output wrapped in `InterfacePermission`, `output_wrapper_field_path`),
`DescribeNetworkInterfacePermissions` (read by ID) and
`DeleteNetworkInterfacePermission`. About 99% of the new Go is generated
(about 1,480 lines); written here is a five-line template that reads a
permission `revoking` or `revoked` as gone. There is no update call: every
field is immutable, and the generated update refuses.

- **Spec:** `networkInterfaceID`, `permission` (`INSTANCE-ATTACH` or
  `EIP-ASSOCIATE`), `awsAccountID` or `awsService`, all immutable; no
  tags (the API has none). **Status:** `networkInterfacePermissionID`,
  `permissionState` (`state`, `statusMessage`); synced when `granted`.
- **Not built:** a reference to the network interface. ACK's
  ec2-controller has no `NetworkInterface` kind (F06 proposes ours), so
  the interface is named by ID.
- **Assumed, unproven:** the error code for an unknown permission ID,
  `InvalidPermissionID.NotFound`, is not documented for these calls; an
  empty answer is also read as gone, so either way works.

**Unproven on the fake:** moto has none of the API. A stand-in unit test
(`sdk_test.go`, on `pkg/fakeaws`'s guarded client, as `DBRoleAssociation`'s
is) answers as the EC2 API reference documents and checks what the
generated create, read and delete send and how they read the answers: the
ID and state from the create, `granted` and `pending` kept, `revoking`,
`revoked`, an empty list and the not-found code read as gone, another
error passed on, no call before the create. No end-to-end test. The
coverage data's "unproven on fake" note exists only for Cloud Control
kinds (`hack/coverage`, from a CRD annotation); it is said here and in the
README instead.

### 6.34 `ec2.aws.hfl.dev` `VPNGatewayRoutePropagation`

A VPN gateway's routes propagated into a route table (F05; one of the two
foundation types Cloud Control cannot provision, `docs/cloudcontrol.md`
section 8, handed to this route). Upstream rechecked 2026-11-09:
ec2-controller `v1.23.1` reports a table's propagating gateways in
`RouteTable`'s status (`propagatingVGWs`, read-only) and has no way to
set one, on master too; no pull request adds one.

**Generated** from the attach pair, mapped in `generator.yaml`
`operations` as `PolicyAttachment`'s are: `EnableVgwRoutePropagation`
(create), `DisableVgwRoutePropagation` (delete), `DescribeRouteTables`
(read, matched on the table's ID). About 98% of the new Go is generated
(about 1,490 lines); written here are two read templates and `hooks.go`
(about 35 lines): the read asks for the one table, and finds the
propagation only when the table lists the gateway among its propagating
gateways. There is no update call: both fields are immutable, and the
generated update refuses.

- **Spec:** `gatewayID` or `gatewayRef` (our `VPNGateway`), and
  `routeTableID`, all immutable. **Status:** only the runtime's.
- **Not built:** a reference to ACK's `RouteTable`: it needs ACK's
  ec2-controller as a Go module, a new upstream dependency (open question 16,
  raised by `VPNConnection`'s `transitGatewayRef` in PR #48).
- **A propagation that exists already** (made outside Kubernetes) is
  found by the first read, so the object adopts it, and its delete stops
  it. Two objects for one gateway and table would share it; the first
  deleted stops it for both.

**Unproven on the fake:** moto has neither call. A stand-in unit test
(`sdk_test.go`, on `pkg/fakeaws`'s guarded client) answers as the EC2 API
reference documents and checks that the read asks for the one table and
finds the propagation only when the gateway is listed (another gateway,
none, and a table gone read as absent; another error passed on), and that
the create and delete send the gateway and the table. No end-to-end test.
As for `NetworkInterfacePermission`, the coverage data's "unproven on
fake" note is for Cloud Control kinds only, so it is said here and in the
README.

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
   `aws.hfl.dev/ack-service`, for each service we extend. A service ACK
   has no controller for runs beside the controller `Beside` names, with
   its identity: `scheduler` beside `eventbridge` (open question 8).
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
7. **An organization-scoped controller runs alone** (section 7.5).
   Beside ACK's organizations-controller, the operator runs our manager
   only while no other ACK controller (one we extend or not) shares its
   namespace; otherwise it runs none, removes one it ran, and records a
   Warning event, `SharedNamespace`, on that controller's Deployment.
   The account-level managers in that namespace are not affected.
8. **The operator holds no AWS credentials.** It may bind only our two
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

One manager has more: the iam manager also creates and deletes Secrets,
cluster-wide, for `AccessKey` (6.30): it makes one Secret per key, owned
by the object, and deletes only one its object owns. With identity `ack`
that right comes to ACK's iam-controller's ServiceAccount with our role.

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

**`tests/operator/run.sh` (`task operator-e2e`) on kind** (kindest/node
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

**The organization-scoped guard** (`TestReconcileOrganizationScoped`
against envtest; `tests/operator/run.sh` on kind, 2026-11-09, with ACK's
real organizations-controller `v1.7.0` from its own chart): beside ACK's
rds-controller in `ack-system` it gets no manager and a `SharedNamespace`
Warning event, and the rds manager keeps running; moved alone into
`ack-organizations-system` (no role annotation, as for Pod Identity) it
gets one, whose ServiceAccount carries no IRSA annotation, and a `Policy`
syncs through it against moto. In envtest, ACK's s3 controller (one we
do not extend) also counts, and the manager comes back once the
namespace is the controller's alone. On the way, the `aws.hfl.dev/source`
label (the ACK Deployment's `namespace.name`) passed a label value's 63
characters for a controller in `ack-organizations-system`, and every
object was refused; past 63 it is now the start and a hash. And two
checks of `run.sh` had been joined on one line, so the check that ACK's
controller holds none of our kinds' rights had not run since it was
written; it runs again, and passes.

**Not proven, and not possible on kind:**

- IRSA and EKS Pod Identity themselves: the webhook and agent exchanging
  the pod's token for the role's credentials;
- that AWS would authorise the role, since moto does not enforce IAM.

What is proven is everything on our side of that:

- our pod presents a ServiceAccount carrying exactly ACK's role
  annotation, in ACK's namespace, with ACK's credentials file;
- on EKS, the one extra trust-policy entry or association then applies to
  it.

### 7.5 An organization-scoped identity (open question 11, answered)

The Organizations kinds act on the whole organization, so they get an
identity scoped to it, and nothing else does:

- **The install.** ACK's organizations-controller (which our operator
  finds by its chart label) installed alone in a namespace kept for
  organization-level objects, `ack-organizations-system` by convention,
  with a role in the management account (or a delegated administrator's
  account), `ack-organizations-controller`. The operator then runs
  `ack-extensions-organizations-manager` there, with that role (section
  7.2), and no other namespace gets an Organizations manager. Account-level
  ACK controllers (iam, rds, ...) run in their own namespaces with
  account-level roles that hold no `organizations:*` permission. A
  manager only ever takes the identity of the ACK controller beside it,
  and the operator refuses to run the Organizations manager while any
  other ACK controller shares that namespace (section 7.2, item 7).
- **The role is bound with EKS Pod Identity, not IRSA** (decided
  2026-11-09: through Pod Identity rather than Workload Identity, an
  important distinction since Pod Identity is newer). ACK's
  ServiceAccount carries no `eks.amazonaws.com/role-arn` annotation (do
  not set `serviceAccount.annotations` in ACK's chart); the role is bound
  outside the cluster, by one Pod Identity association per
  ServiceAccount, cluster, namespace `ack-organizations-system`:
  - `ack-organizations-controller`, ACK's controller;
  - `ack-extensions-organizations-manager`, our manager (identity `own`,
    the default). With identity `ack` our manager runs as ACK's
    ServiceAccount and this second association is not needed.

  The operator needs no change for this: it copies ACK's
  `eks.amazonaws.com/*` annotations and finds none, and the Pod Identity
  agent (the `eks-pod-identity-agent` add-on, which does not run on
  Fargate) gives our pod the role through our ServiceAccount's
  association. The operator holds no AWS credentials and does not make
  associations; the platform's account-level install does, for example
  with ACK's eks-controller `PodIdentityAssociation` kind
  (`eks.services.k8s.aws`; `clusterName`, `namespace`,
  `serviceAccount`, `roleARN`, `targetRoleARN` at `v1.26.0`, with
  `disableSessionTags` left false, since the trust below reads the
  session tags), never the Organizations manager, whose role holds no
  `eks:*` permission.
- **Trust, narrowed to the namespace.** The associated role trusts the
  service principal `pods.eks.amazonaws.com` for `sts:AssumeRole` and
  `sts:TagSession`. EKS tags each session with the cluster, namespace
  and ServiceAccount, so the trust policy can say what the namespace rule
  says, only these two ServiceAccounts in that namespace of that
  cluster (the same conditions as the Cloud Control route's generated
  trust policy, `docs/cloudcontrol.md`):

  ```json
  {
    "Effect": "Allow",
    "Principal": {"Service": "pods.eks.amazonaws.com"},
    "Action": ["sts:AssumeRole", "sts:TagSession"],
    "Condition": {
      "StringEquals": {
        "aws:SourceAccount": "<cluster account>",
        "aws:RequestTag/kubernetes-namespace": "ack-organizations-system",
        "aws:RequestTag/kubernetes-service-account": ["ack-organizations-controller", "ack-extensions-organizations-manager"]
      },
      "ArnEquals": {"aws:SourceArn": "arn:aws:eks:<region>:<cluster account>:cluster/<cluster>"}
    }
  }
  ```
- **The cluster's account and the management account.** An
  association's role is in the cluster's account. A cluster in the
  management account (which AWS advises against running workloads in)
  associates `ack-organizations-controller` directly. Otherwise the
  association names a role in the cluster's account that may do nothing
  but assume the next, as `roleArn`, and the management account's (or
  delegated administrator's) `ack-organizations-controller` as
  `targetRoleArn`: EKS chains the two. The target role trusts the first
  role's ARN only (`sts:AssumeRole`, `sts:TagSession`), and holds the
  actions below.
- **Proven and not.** `task operator-e2e` installs ACK's real
  organizations-controller `v1.7.0` in `ack-organizations-system` with no
  role annotation and checks that our manager's ServiceAccount carries
  none either, and that a `Policy` reconciles through it against moto
  (section 7.4). The associations, the agent and the trust conditions
  need EKS and AWS: written from AWS's EKS Pod Identity documentation,
  not tested here.
- **Least privilege, per kind** (all `Resource: "*"` unless narrowed; the
  runtime also calls `sts:GetCallerIdentity`):

  | Kind | Actions |
  | --- | --- |
  | `Policy` | `organizations:CreatePolicy`, `DescribePolicy`, `UpdatePolicy`, `DeletePolicy`, `ListTagsForResource`, `TagResource`, `UntagResource` |
  | `PolicyAttachment` | `organizations:AttachPolicy`, `DetachPolicy`, `ListTargetsForPolicy` |
  | `DelegatedAdministrator` | `organizations:RegisterDelegatedAdministrator`, `DeregisterDelegatedAdministrator`, `ListDelegatedAdministrators` |
  | `AccountPlacement` | `organizations:MoveAccount`, `ListParents`, `DescribeAccount` |
  | `Organization` | `organizations:DescribeOrganization`, `ListRoots`, `EnablePolicyType`, `DisablePolicyType`, `EnableAWSServiceAccess`, `DisableAWSServiceAccess`, `ListAWSServiceAccessForOrganization`; and `organizations:CreateOrganization` with `iam:CreateServiceLinkedRole` only if the object is to make the organization |

  The role should also deny `organizations:DeleteOrganization` and
  `organizations:LeaveOrganization` explicitly: no kind calls them.
  `Policy`'s actions can be narrowed with the `organizations:PolicyType`
  condition key to the types in use.
- **Out of scope here:** granting a workload account's team more access
  across accounts. That is an access request to the owning team, in
  Bundt.

### 7.6 Per environment: `ACKContext` (open question 5, answered)

Decided 2026-11-10: ACK's per-controller managers follow the same
environment-scoped Pod Identity model as Cloud Control
(`docs/cloudcontrol.md` section 10), with one manager per controller per
namespace. So an environment (a namespace) enables the
ACK-generated managers it uses, and each runs as the environment's role:

```yaml
apiVersion: core.aws.hfl.dev/v1alpha1
kind: ACKContext
metadata: {name: ack, namespace: team-a-prod}   # one per namespace; CEL refuses any other name
spec:
  roleARN: arn:aws:iam::111122223333:role/team-a-prod-ack
  region: us-west-2
  controllers:
    - service: rds
      kinds: [OptionGroup, DBProxyTarget]   # optional; empty: every kind of the service
    - service: scheduler
  deletionPolicy: delete                    # optional; or retain
```

**A sibling kind, not a wider `CloudControlContext`.** The two options
were (a) widen `CloudControlContext` into one context per environment for
both routes, or (b) a sibling kind. (b) is built, because:

- the routes are separate products: separate Bundt providers
  (`hfl.aws-cloudcontrol` beside the ACK one), separate API groups,
  separately switched on in the operator (Cloud Control only when its
  image is given). A `CloudControlContext` that enables ACK managers
  would name one route after the other, and couple their versions;
- the controllers differ in shape: Cloud Control is one manager for a list
  of `Kind.service`; ACK is one manager per service, each with its own
  kinds, its own ClusterRole and its own ServiceAccount;
- each route's role stays small. One role holding both routes' policies
  would meet IAM's ten managed policies per role sooner, and a team that
  uses only one route never holds the other's permissions. An
  environment may still use one role for both: name the same `roleARN`
  in both contexts, and generate both policies onto it;
- what is shared is code, not API: the plan of ServiceAccount,
  Deployment and RoleBinding, the finalizer and status shape
  (`pkg/operator/ackcontext.go` beside `cloudcontrol.go`), and the trust
  policy and the policy split (`cloudcontrol/pkg/iamgen`'s
  `PodIdentityTrust` and `Split`, used by both).

The group is `core.aws.hfl.dev`, as Config Connector's context is in
`core.cnrm.cloud.google.com`: not a service, so no `<service>.aws.hfl.dev`
fits (open question 17 asks the builder to confirm the name).

**What the operator makes** (`pkg/operator/ackcontext.go`), for each
controller a context lists:

| Where | What | Why |
| --- | --- | --- |
| the operator's namespace | ServiceAccount `ack-extensions-<service>-<environment>`, no IRSA annotation | the identity the Pod Identity association names; out of the environment's namespace, so nobody who may only create pods there can run as the role |
| the operator's namespace | Deployment `ack-extensions-<service>-<environment>`: the service's manager, `--watch-namespace=<environment>`, `--aws-region`, `--deletion-policy`, `--enable-carm=false`, `--reconcile-resources` when the context names kinds, leader election in the environment | one manager per controller per environment |
| the environment's namespace, owned by the context | RoleBinding `ack-extensions-<service>-environment` to the service's manager ClusterRole (`config/rbac`), and a leader-election Role and RoleBinding | Kubernetes rights in that namespace only: its kinds, the ACK kinds it references, its Secrets, its leases and events; nothing cluster-wide, nothing in another namespace |
| the context | a finalizer, and `status`: `managers`, `podIdentityAssociations` (one per manager), `refused`, `Ready` | an owner reference cannot cross namespaces; the status says exactly which associations to make |

A service's kinds are read from the API server (the CRDs served in
`<service>.aws.hfl.dev`), so a kind added later needs no operator change.
With one watch namespace ACK's runtime runs no namespace or CARM cache;
CARM is off: the environment's role is the identity. A reference to an
object in another namespace fails on RBAC, which is the scoping wanted.

**Refused, with the reason in `status.refused`** (the other controllers of
the context still run; `Ready` is `False`, `ControllersRefused`):

- `OrganizationScoped`: `organizations`. Its kinds keep the
  organization-scoped install of section 7.5, beside ACK's
  organizations-controller alone in its namespace; no environment gets
  an Organizations manager.
- `UnknownService`: no manager image for the service, no kinds of it
  installed, or `cloudcontrol` (a `CloudControlContext`'s).
- `UnknownKinds`: a kind the service does not serve. A manager already
  running is left alone until the context is fixed, as Cloud Control does.
- `NamespaceClaimed`: our manager of the same service already runs
  beside an ACK controller that watches this namespace (its
  `--watch-namespace` empty, unreadable, or naming it). Two managers would
  reconcile the same objects as two identities. Narrow ACK's
  `watchNamespace`, or leave the service out of the context. The beside
  manager is not touched: it was there first and serves other namespaces.

Two contexts cannot claim one namespace: the name is fixed (`ack`), so a
second is refused by the API server. A `CloudControlContext` in the same
namespace is no conflict: it serves other kinds.

**The role.** One per environment by default, every manager of the
context associated to it:

```sh
task ack:iam CONTEXT=team-a-prod-ack.yaml ACCOUNT=111122223333 \
  CLUSTER_ARN=arn:aws:eks:us-west-2:111122223333:cluster/platform OUT=iam/team-a-prod-ack
aws eks create-pod-identity-association --cluster-name platform \
  --namespace ack-extensions-system --service-account ack-extensions-rds-team-a-prod \
  --role-arn arn:aws:iam::111122223333:role/team-a-prod-ack     # and one per other manager
```

`permissions-<n>.json` are the actions of the kinds enabled, split into
as few managed policies as IAM's size limit allows (every account-level
kind here together fits in one); `trust.json` lets `pods.eks.amazonaws.com` assume
the role only for the cluster named, and only when Pod Identity's session
tags name the operator's namespace and one of the context's managers'
ServiceAccounts (`aws:RequestTag/kubernetes-service-account` as a list).

**Where the permissions come from** (`pkg/ackiam`). ACK's generator
writes no IAM policy (each ACK controller ships a curated
`config/iam/recommended-inline-policy`), but the code it generates names
every AWS operation it calls. So the Cloud Control generator's approach
is reused on the code: each kind's package and the service's own packages
it imports (ec2's `tags`) are scanned for calls on the SDK client
(`rm.sdkapi.<Operation>`, a `client.<Operation>` for another region,
`svcsdk.New<Operation>Paginator`), each name is kept only if the SDK
has that operation, and the IAM action is the operation in the service's
IAM namespace (`events` for eventbridge, `application-autoscaling`), plus
`sts:GetCallerIdentity` for the runtime. A kind added later is covered
without a change here. What a call needs beyond its own action is not in
the code, so it is curated per kind (`ackiam.Curated`), from AWS's
documentation and unchecked on real AWS:

| Kind | Curated |
| --- | --- |
| `DBRoleAssociation.rds`, `Schedule.scheduler` | `iam:PassRole` |
| `Connection.eventbridge` | `secretsmanager:CreateSecret`, `DescribeSecret`, `UpdateSecret`, `PutSecretValue`, `DeleteSecret` (EventBridge keeps the connection's credentials in a secret it makes as the caller) |
| `ReplicaKey.kms` | `kms:CreateKey` (in the replica's region) |
| `ReplicationConfiguration.ecr` | `iam:CreateServiceLinkedRole` |
| `WebACLAssociation.wafv2` | the protected resource's action: `elasticloadbalancing:SetWebAcl`, `apigateway:SetWebACL`, `appsync:SetWebACL`, `cognito-idp:AssociateWebACL` and the rest |

`task ack:iam LIST=1` prints every kind's actions; the Organizations
kinds' match section 7.5's hand-written table. The generated actions
are what the code calls; whether AWS asks for more on some call (a
condition, a dependent action) shows only on real AWS (section 8).

**The beside mode stays**, for the Organizations kinds (section 7.5) and
for clusters that install ACK the classic way. For an account-level
service an environment should use one or the other, not both: the
`NamespaceClaimed` refusal holds that line.

**Proven** (`TestACKContext` and `TestWatchNamespaces` against envtest;
`TestScan` for the permissions; `tests/operator/run.sh` on kind):

- a manager per controller per namespace: two environments with a
  scheduler controller each get `ack-extensions-scheduler-env-a` and
  `-env-b` in the operator's namespace, each its own ServiceAccount with no
  IRSA annotation, `--watch-namespace` its environment,
  `--reconcile-resources` the kinds named, and the association it needs in
  status;
- watch scoping: rights in the environment only (SubjectAccessReviews:
  its kinds there, nothing in another environment or the operator's
  namespace, no namespace list); a `ScheduleGroup` in env-a syncs against
  moto through env-a's manager, one in env-c (no context) is never touched
  and never reaches moto;
- the refusals: a second context in a namespace (not named `ack`) by the
  API server; `rds` in env-a while ACK's real rds-controller watches every
  namespace with our rds manager beside it (`NamespaceClaimed`; in envtest
  it comes back once ACK's `watchNamespace` names another namespace);
  `organizations` (`OrganizationScoped`); unknown services and kinds;
- a controller taken out of a context, and the context deleted: its
  managers, ServiceAccounts and bindings go; another environment's stay.

**Not proven, and not possible on kind:** EKS Pod Identity itself (the
agent exchanging the pod's token for the role's credentials) and AWS
honouring the trust policy's conditions, as for Cloud Control.

**Not built:** running ACK's own controllers per environment (open
question 18). Today ACK's kinds in an environment are reconciled by
ACK's controller wherever it is installed, as its identity; only our
kinds follow the environment's role.

## 3a. The coverage dashboard

How far ACK and this repository reach is measured, not guessed
(asked for 2026-11-09): `hack/coverage/` reads the CloudFormation
resource schemas (the denominator: every writable property of every
resource, per service), every ACK controller's latest released CRDs
(official) and our `config/crds/bases/` (ours), matches CRD spec paths
to CloudFormation properties, and writes `docs/coverage/` (CSV per
service, resource and attribute; an SVG bar per service) and the
Coverage section of `README.md`. `docs/coverage/README.md` has the
method, pins, matching rules, known error and spot checks;
`task coverage` regenerates, `task coverage:check` fails when the
committed outputs are stale. The CloudFormation zip is committed
because AWS replaces the file at its URL every hour. The Google Cloud
and Azure repositories get the same dashboard with the same CSV schema.

- **Tiers and categories.** `docs/coverage/priority.csv` (hand-edited,
  never written by the generator) gives each service a category
  (`platform-foundation`, `security-adjacent`, `application`, `other`)
  and a tier (1 platform-foundation, 2 security-adjacent plus the
  application services a tracked terraform-aws-module uses, 3 the rest,
  the default); the README and `bars/_tiers.svg` and
  `bars/_categories.svg` roll coverage up by both, `tiers.csv` and
  `categories.csv` have the numbers.
- **The foundation gap report.** `docs/coverage/foundation.md` is the
  ranked work queue for the kind agents (every platform-foundation and
  security-adjacent resource, with what upstream has, moto, module need,
  size and the proposed kinds; first item: Organizations policies, SCP
  attachment, delegated administrators). The numbers are regenerated; the
  research is `hack/coverage/foundation.yaml`, a snapshot of 2026-11-09
  that must be re-read against upstream before an item is started.
- **After adding or changing a kind:** run `task coverage` and commit
  the result; a kind that is a property of a larger CloudFormation
  resource, or a new rename, is a line in `hack/coverage/mappings.yaml`.
- **When ACK releases:** `hack/coverage/run.sh --update-pins`, review
  `docs/coverage/sources.yaml`, commit.
- **Known limits:** official coverage is under-counted where ACK follows
  the SDK's shape and CloudFormation its own and nobody has written the
  alias (about 2% of covered attributes in the spot checks); services
  ACK covers that CloudFormation lacks are listed, not measured.

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
     `RemoveRoleFromDBInstance`, which moto lacks (section 4.4);
   - virtual MFA devices: the seed decodes as Base32 and the QR code is a
     PNG that an authenticator reads, a device so made can be enabled for
     a user with two codes from the seed, and the delete of an enabled
     device is refused with `DeleteConflict` (section 6.31).
   - VPN connections: the options' read against a real connection (no
     update across resyncs, a tunnel setting changed outside Kubernetes
     put back), a tunnel option change and a CIDR change, static routes
     added and removed, `pending` to `available`, and the keys absent from
     the manager's log at debug level (section 6.32).
   - network interface permissions, the whole kind (moto has none of the
     API): a permission granted to another account, its state, the error
     code for an unknown ID, and the delete (section 6.33).
   - VPN gateway route propagation, the whole kind (moto has neither
     call): a propagation enabled and disabled, read back from the route
     table, and EC2's answer to disabling one already stopped (section
     6.34).
4. CRD labels marking a gap kind and naming the upstream kind it
   anticipates, as gaps-gcp does.
5. Patches 0001 and 0002 offered upstream on 2026-11-10
   (code-generator #762 and #763, UPSTREAM.md). Comments on PRs #184 and
   #257 with what we found (both conflict with their base) are drafted
   and wait for the builder's approval.
6. `hack/upstream-pr.sh`, as gaps-gcp has, to follow the PR heads copied
   from.
7. CI: `task check` on Linux, with the envtest assets and moto cached.
8. The OptionGroup test takes about 50 seconds: options are added on the
   second reconcile, after the runtime's requeue delay.
9. `DBRoleAssociation`'s delete is proven end to end only up to the
   `RemoveRoleFrom...` call (section 4.4, open question 6).
10. A generated kind cannot reference ACK's kind of the same service
    (`ScalingPolicy` to ACK's `AutoScalingGroup`, section 6.6):
    `hack/generate.sh` rewrites ACK's import path for the service to
    ours. It needs the rewrite limited to the packages generated here, or
    a hand-written references file. `PolicyAttachment` (section 6.26)
    does it with a type alias of ACK's kind in our API package instead;
    `ScalingPolicy` could do the same.
11. The heavy checks run on a Spot VM (`../offload`), not the laptop. A
    fresh VM has no `third_party/` (gitignored), so the command is
    `task tools && task check && task operator-e2e`.
12. EventBridge Scheduler runs beside ACK's EventBridge controller (open
    question 8). A cluster with Scheduler kinds therefore needs ACK's
    EventBridge controller installed, even with no EventBridge kinds, and
    that controller's role needs `scheduler:*` (and `iam:PassRole` for the
    roles schedules invoke their targets with). Should ACK ship a
    Scheduler controller, the manager moves beside it on its own
    (`pkg/operator`, `ourServices`).
13. Cloud Control (`docs/cloudcontrol.md` section 12): one real-AWS run per
    kind (the sandbox account), types in foundation order, the overlay
    (renames, references, removed fields), selective CRD installation, and
    a non-EKS identity (documented in section 10, not built). The coverage
    dashboard does not count the Cloud Control kinds as ours yet.

## 9. Open questions

1. **Fields on existing ACK kinds** (Secrets Manager rotation in PR #69,
   ACM import in PR #104). gaps-gcp's rule (2026-11-07): wait
   for the release; ship our own copy of the kind, in our group, only when
   a package cannot work without the field and the release is not near.
   Does the same rule hold for ACK? Proposed: yes.
2. **Database users and roles on RDS** have no AWS API. Out of scope
   here, or a separate project that speaks SQL (connection details from
   ACK's `DBInstance`, credentials from Secrets Manager)? Proposed: out of
   scope for this repository.

   **Answered** (2026-11-10): RDS database users and roles are not
   necessarily out of scope; if they exist in the API, include them. Checked 2026-11-10 (read-only):
   - **The RDS API** (aws-sdk-go-v2 `service/rds` v1.93.8, every
     operation): nothing makes, changes or lists a database user or role.
     They live inside the engine (`CREATE USER`, `CREATE ROLE`, `GRANT`
     in SQL). What the API has: the master user (`masterUsername`,
     `manageMasterUserPassword` on ACK's `DBInstance` and `DBCluster`);
     IAM database authentication (`enableIAMDatabaseAuthentication` on
     the same kinds; connecting then needs `rds-db:connect` on
     `arn:aws:rds-db:<region>:<account>:dbuser:<resource-id>/<user>` in an
     IAM policy, which ACK's iam `Policy` writes, while the user itself is
     still made in SQL with `GRANT rds_iam`); a proxy's per-user secrets
     (ACK's `DBProxy` `auth`); and `CreateTenantDatabase` (RDS for Oracle
     multitenant: a tenant database with its own master user), which
     neither ACK nor CloudFormation has.
   - **IAM**: no database user; only the `rds-db:connect` action.
   - **CloudFormation** (the registry schemas pinned in
     `docs/coverage/inputs`): no `AWS::RDS` user or role type, and no
     tenant database. ElastiCache and MemoryDB do have users in their
     APIs (`AWS::ElastiCache::User`, `AWS::MemoryDB::User`; ACK's
     elasticache-controller serves `User` and `UserGroup`), but not RDS.
   - **ACK**: rds-controller `v1.14.2` (the latest release) and `master`
     serve no user kind, and no open pull request adds one; community
     issue #1793 ("Ability to create RDS DB users", 2023) was closed by
     the maintainers as data plane: "There is no AWS API for creating DB
     users."
   - **One AWS API reaches into the engine**: the RDS Data API
     (`rds-data` `ExecuteStatement`), for Aurora PostgreSQL and MySQL
     clusters with the HTTP endpoint on (`EnableHttpEndpoint`),
     authenticated by a Secrets Manager secret. A kind could run
     `CREATE ROLE` and `GRANT` through it and read them back from the
     catalog. It is SQL through an API: engine-specific, Aurora only (not
     RDS for PostgreSQL or MySQL instances), and moto's `rds-data` returns
     canned results, so it could not be proven on the fake.

   So nothing for users and roles is buildable from the RDS, IAM or
   CloudFormation APIs. Buildable from the API, and adjacent:
   `rds.aws.hfl.dev` `TenantDatabase` (ACK's generator; moto has none, so
   unproven on the fake), added to section 5's candidates when a package
   needs it. Recommended for users: IAM database authentication as the
   path (ACK's field, an IAM policy with `rds-db:connect`, one `GRANT
   rds_iam` per user at bootstrap); a Data API kind for Aurora roles only
   if the builder wants SQL spoken here (open, not built).
3. **`RolePolicyAttachment` on a role an ACK `Role` manages** loses the
   tug of war with ACK's `Role` (section 6.1). Options:
   - document it (done);
   - refuse a `roleRef` to an ACK `Role` with `spec.policies`;
   - ship PR #184's additive mode, which means changing ACK's `Role`,
     a fork.

   Proposed: refuse such a `roleRef` until upstream ships the mode.
4. **Identity default.** `own` is the default (narrower Kubernetes
   rights, one AWS grant per role); `ack` needs no AWS step. Keep `own`?

   **Answered** by question 5's answer (2026-11-10): for the
   account-level kinds the identity is now the environment's, per
   manager: each runs as its own ServiceAccount, bound by EKS Pod
   Identity to the environment's role (section 7.6). Beside ACK's
   controller (the Organizations kinds, and clusters that install ACK the
   classic way), `own` stays the default.
5. **One manager per ACK controller** (per service, per namespace). That
   follows ACK's model, and with the operator it is automatic. Confirm.

   **Answered** (2026-11-10): ACK's per-controller managers follow the
   same environment-scoped Pod Identity model as Cloud Control, and one
   manager per controller per namespace is confirmed. Built (section 7.6): an `ACKContext` per namespace lists
   the controllers; the operator runs one manager per controller, in its
   own namespace as `ack-extensions-<service>-<namespace>`, watching only
   that namespace, its IAM policy generated from the kinds enabled
   (`task ack:iam`). The Organizations kinds keep their own install
   (section 7.5).
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
   needing `scheduler:*` on that role.

   **Answered** (2026-11-09): build it, at the implementer's judgment.
   Built as proposed: `pkg/operator` runs the scheduler
   manager beside ACK's EventBridge controller (`Beside`), copying its
   identity; section 8, item 12, says what that asks of the role.
9. **IAM group membership: exclusive or not** (rank 15). Terraform has
   both: `aws_iam_group_membership` owns a group's whole list of users,
   `aws_iam_user_group_membership` adds one user to listed groups and
   leaves the rest. An exclusive kind removes any member another writer
   added (a second object, the console, Terraform), and that writer adds
   it back: the tug of war of questions 3 and 7. `UserGroupMembership`
   is built non-exclusive, remembering in `status.groups` what it put so
   a group taken off its list is still removed. Assumed (coordinator,
   2026-11-09); the builder to confirm. An exclusive group-side kind can
   be added later beside it.

   **Answered** (2026-11-09): group membership stays
   non-exclusive, as built.
10. **IAM `AccessKey`** (rank 15). `CreateAccessKey` returns the secret
    access key once, and never again. A kind has to put it somewhere,
    and ACK's runtime has no pattern for writing a secret out (it only
    reads them, as `SecretKeyReference`). Options:
    - the controller writes a Kubernetes Secret, named in the spec, in
      the object's namespace. The manager then needs rights to create
      and update Secrets, and a lost Secret means rotating the key;
    - the controller writes to an external secret store (Secrets Manager,
      through a reference), which adds a second service and its rights
      to the IAM manager;
    - leave `AccessKey` out.

    Recommended: leave it out. A static key is a long-lived credential
    for a user, where the identity in this repository is a workload's
    role (IRSA or EKS Pod Identity, section 7), and the coordinator
    reports that the direction for identity points the same way.
    Each of the other options would make this repository a writer of
    credentials, which nothing else here is. If a package needs one
    later, the first option is the smallest. `LoginProfile`,
    `SSHPublicKey`, the account alias and the password policy do not
    depend on the answer and go ahead.

    **Answered** (2026-11-09): build it, writing a Kubernetes
    Secret. The secret key, returned only by the create, goes into a
    Secret in the object's namespace, named by a spec field with a default,
    owned by the `AccessKey` object; it is never overwritten on a resync,
    never logged, and never put in status or events. Rotation is a new
    `AccessKey`, never a change to one. Deleting the object deletes the key
    and its Secret. Queued after F01 (`docs/handoff.md`), with tests that
    the secret value appears in no log and no status.
11. **Which account an Organizations kind acts in** (F01 and the kinds
    after it: Identity Center, Control Tower, the security services'
    organization settings). Organization-level APIs run in the
    organization's management account, and some in a delegated
    administrator's account (Identity Center, and policies once a
    delegation policy allows it). Our manager runs with the identity of
    ACK's controller for the same service, as every manager here does
    (section 7), and ACK's own `Account` and `OrganizationalUnit` act in
    the same account. Nothing in a kind names an account. Options:
    - the manager acts in whatever account its identity is, and a
      platform team installs the Organizations managers with the
      management account's role (or the delegated administrator's) and
      keeps them out of workload clusters;
    - a per-object account field, which ACK's CARM already gives:
      annotate the namespace with `services.k8s.aws/owner-account-id`
      and map it to a role in the management account;
    - refuse to start unless the account is the organization's
      management account (checked with `DescribeOrganization`), which
      would also refuse a delegated administrator.

    Recommended: the first, with the second where one cluster serves
    several accounts, and document that these kinds must not run with a
    workload account's role. The cluster holding them holds the
    organization's guardrails (an SCP can lock out every account), so the
    namespace they live in needs the tightest RBAC; that is a note in the
    README, not a code check. The builder to confirm. Not blocking the
    kinds.

    **Answered** (2026-11-09): the scope of permissions attached to any
    identity in a namespace is commensurate with what it manages. An
    identity for organization-level things is constrained at the
    organization level; an identity that manages a GCP project has no
    org-level roles. A principal's permission scopes align with what it
    operates against. Cross-project or cross-account role grants or
    resource builds are therefore trickier but not impossible: identities
    need careful management and a way to request more access from the
    owning team when needed.

    So the Organizations kinds get an organization-scoped identity of their
    own (the management account's, or a delegated administrator's), in a
    namespace that holds only organization-level objects, and no
    account-level manager ever holds an Organizations permission. Section
    7.5 names that install and gives each kind's least-privilege IAM
    policy. Cross-account access (a workload team asking the organization
    team for more) is Bundt's feature, an access request to the owning
    team, and out of scope for this repository.
12. **What deleting an `Organization` does** (F01). `DeleteOrganization`
    works only on an organization with no member accounts and cannot be
    undone. Recommended: deleting the object never deletes the
    organization, it only stops managing it.

    **Answered** (2026-11-09): an organization is not deletable, so it is
    not treated as such. The deletion-policy annotation is forced to
    retain (abandon), which reflects the operational reality anyway, and
    made immutable, enforced by a webhook, a schema or both.
    Built (section 6.28): the controller has no `DeleteOrganization` call
    at all (the guarantee), and a ValidatingAdmissionPolicy requires
    `services.k8s.aws/deletion-policy: retain` on every Organization and
    refuses its change or removal. A CRD's own CEL cannot see
    `metadata.annotations`, and the repository has no webhook server, so
    the policy is CEL in the API server. The policy's list of retain-only
    kinds is where later undeletable kinds join.
13. **Identity of the Cloud Control manager** (the spike in
    `docs/cloudcontrol.md`, branch `agent/cloudcontrol-spike`). The generic
    Cloud Control manager serves types ACK has no controller for, so the
    operator has no ACK controller to run it beside and copy the identity
    from (section 7; `Beside` answered the same question for one service in
    question 8). The manager is built on ACK's runtime, so CARM, team CARM
    and `IAMRoleSelector` work unchanged once it has a base identity.
    Options (`docs/cloudcontrol.md` section 10 has the table):
    - beside an ACK controller with its identity, as Scheduler is beside
      EventBridge: but one process serves many services, and that role
      would need every Cloud Control type's permissions;
    - **its own manager per AWS service**, installed by the operator from a
      small configuration (role ARN or Pod Identity, region, the kinds to
      serve): ServiceAccount `ack-extensions-cloudcontrol-<service>`, one
      image with `--reconcile-resources` for that service's kinds, only
      those CRDs installed, in ACK's namespace so CARM's ConfigMaps are
      shared, its role from the generated IAM policy merged per service;
    - one manager and one role for every Cloud Control type;
    - a static bundle outside the operator.

    Recommended: the second, which keeps ACK's one controller and one role
    per service (question 5) and least privilege per service. The builder
    to decide, with the other Cloud Control decisions in
    `docs/cloudcontrol.md` section 12 (go or not, the API group, the
    default for write-only properties, a sandbox account for real-AWS runs).

    **Answered** (2026-11-09), all five Cloud Control decisions,
    recorded in `docs/cloudcontrol.md` ("Decisions"):
    1. go, with limits: productionize the generic manager, roll types out
       in foundation order, each proven on the fake and marked untested
       on real AWS until it has a real run; ACK-generated kinds where
       ACK's generator gives better behaviour; Upjet the fallback;
    2. the API group `<service>.cloudcontrol.aws.hfl.dev`, matching a
       separate Bundt Provider, `hfl.aws-cloudcontrol`;
    3. identity: "I think it's closer to the one shared cloud control
       manager, but not exactly. I think the scoping should be at the
       environment level, so each environment gets an identity attached,
       and not through Workload Identity but instead through Pod
       Identity. Important distinction there, Pod Identity is newer. I
       don't think a per-service manager is the correct thing. Loosely,
       this follows the KCC model of a GSA and KSA workload identity
       bound that is enabled within a namespace to run KCC, and really
       that namespace has a scope that it works within." Built: a
       `CloudControlContext` per namespace; the operator runs that
       namespace's manager in its own namespace (as Config Connector does
       in `cnrm-system`) as `ack-extensions-cloudcontrol-<namespace>`,
       bound by an EKS Pod Identity association to the environment's
       role, watching only that namespace, with Kubernetes rights only
       there (`docs/cloudcontrol.md` section 10). The per-service
       managers recommended above are not built;
    4. Secret references only for write-only fields marked as secrets,
       opt-in per field;
    5. a sandbox AWS account is coming for one real run per type (the
       builder creates it); the live-test switch and the account's
       CloudFormation template are prepared, credentials by federation
       from the offload VMs' Google service account, never stored keys.
14. **IAM `VirtualMFADevice`** (F03). **Decided** (2026-11-09):
    built here on ACK's generator (hand-written if it must be), not on
    Cloud Control, since Cloud Control never returns the seed (the Cloud
    Control `VirtualMFADevice`, PR #44, is closed). `CreateVirtualMFADevice`
    returns the Base32 seed (and a QR code PNG) once; the create writes the
    seed, and optionally the PNG, into a Kubernetes Secret owned by the
    object, never logged and never written over: the same pattern as
    `AccessKey` (6.30). Queued after `AccessKey` (`docs/handoff.md`).
15. **Control Tower's `LandingZone` through Cloud Control** (F07). Deleting
    an `AWS::ControlTower::LandingZone` decommissions the landing zone: the
    organization's guardrails, the log archive and audit accounts' setup,
    and every enabled control. That is `Organization`'s case (question 12)
    and worse, since it can be undone only by setting the landing zone up
    again. Options:
    - serve it retain-only: add its resource to
      `config/admission/retain-only.yaml`, so every object must carry
      `services.k8s.aws/deletion-policy: retain`. The Cloud Control
      reconciler would still call `DeleteResource` for an object without
      the annotation that predates the policy, so the guarantee is the
      policy alone, not a missing call as for `Organization`; a per-type
      "never delete" in `cloudcontrol/types.yaml`, which the generic
      reconciler honours, would close that;
    - leave `LandingZone` out: a landing zone is set up once, by hand or by
      AWS's own tooling, and adopted nowhere;
    - serve it like any type.

    Recommended: the first, with the per-type "never delete" in the same
    change, so it is the same guarantee as `Organization`'s. Not built
    until the builder answers; `EnabledControl` and `EnabledBaseline` (a
    control or baseline on an OU, which a delete only disables) are served
    without it, unproven on the fake (no emulator has Control Tower).

    **Answered** (2026-11-10): the first, retain-only like
    `Organization`, with `neverDelete` and the retain-only admission
    policy; and for the fake, a test double or recordings for Control
    Tower. Built (`docs/cloudcontrol.md` section 7, batch 3):
    `neverDelete: true` in `cloudcontrol/types.yaml`, `landingzones` in
    `config/admission/retain-only.yaml`, an in-memory test double in
    `cloudcontrol/test/fake/controltower.go` and `TestLandingZone`. A
    never-deleted type is left out of the live-test role and never swept.
16. **A reference from `VPNConnection` to ACK's `TransitGateway`** (F05,
    section 6.32). The connection's other end is our `VPNGateway` or a
    transit gateway, which ACK's ec2-controller manages. A
    `transitGatewayRef` needs ACK's `TransitGateway` type, as `AccessKey`
    and the Organizations kinds reference ACK's kinds: ACK's ec2-controller
    as a Go module here (today only its `pkg/tags` is copied), a new
    upstream dependency. Options:
    - add `github.com/aws-controllers-k8s/ec2-controller` `v1.23.1` (the
      version already pinned), alias `TransitGateway` in
      `services/ec2/apis/v1alpha1/ack_kinds.go`, and register its scheme;
    - leave `transitGatewayID` a plain ID.

    Recommended: the first, the same pattern as the IAM and
    Organizations references. Built meanwhile: the plain ID.
    `VPNGatewayRoutePropagation`'s `routeTableRef` (PR #50) waits on the
    same answer.
17. **The name of the per-environment context for the ACK-generated
    kinds** (section 7.6). Built as `core.aws.hfl.dev/v1alpha1`
    `ACKContext`, one per namespace, named `ack`: a sibling of
    `CloudControlContext`, in a group that is not a service's, as Config
    Connector's context is in `core.cnrm.cloud.google.com`. Names and API
    groups are the builder's. Recommended: keep it; a rename now costs a
    CRD and the docs, later it costs every environment's object.
18. **ACK's own controllers per environment.** An `ACKContext` runs our
    managers per environment; ACK's own controllers (its `DBInstance`,
    `Role`, `VPC`) still run wherever ACK's chart put them, cluster-wide
    as their own role. So an environment's AWS surface is on two
    identities: its role for our kinds, ACK's for ACK's. Options:
    - the operator also runs ACK's controller per context entry
      (`ack-<service>-controller-<namespace>`, ACK's image at the version
      UPSTREAM.md pins, ACK's ClusterRole bound in the namespace), so one
      context is the environment's whole ACK surface on one role, with
      ACK's recommended policy added to `task ack:iam`. The operator would
      then deploy upstream images and their RBAC: a new upstream
      dependency of the install;
    - Bundt installs ACK's chart per environment itself (`watchNamespace`
      the environment, an association for its ServiceAccount), and the
      operator only refuses an overlap, as it does for ours;
    - leave ACK's controllers cluster-wide.

    Recommended: the first, as the next change, so an environment has one
    role and one place that says what it runs. Not built until the
    builder answers.

19. **Do other list-like fields follow A3?** Decision A3 made
    `Organization`'s two lists additive (A3, 2026-11-10; section 6.28):
    what is enabled outside Kubernetes is kept and reported in
    `status.unmanaged<List>`. Two cases A3 does not settle:
    - **Tags.** `Policy` (and every ACK kind) syncs tags exclusively:
      a tag added outside Kubernetes is removed (ACK's `syncTags`, copied
      from upstream). Making tags additive here would diverge from ACK
      and from every ACK controller beside ours.
    - **Taking on by listing.** Built: listing an entry that is already
      enabled makes the object own it, so taking it off the list later
      disables it (an import). The alternative is to own only what the
      object itself enabled, so a found entry is never disabled from
      Kubernetes even after it is listed.

    Recommended: tags stay as ACK has them (A3 was asked about the
    organization's settings, and a tag is not one); listing takes an entry
    on, as built, since that is what importing it means. Not changed until
    the builder answers.

20. **Validation on Cloud Control kinds' status** (found by
    `TestEveryKindOnSchemaFake`, 2026-11-11). The generated CRDs keep the
    registry schema's `enum`, `minLength` and `maxLength` on read-only
    properties in `status` (`status.status` of Security Hub's
    `DelegatedAdmin`: `ENABLED`, `DISABLE_IN_PROGRESS`). When AWS returns
    a value the pinned schema does not list (a new state, a longer
    string), the API server refuses the manager's status patch, and the
    object stays unsynced until the schema zip is updated and the CRD
    regenerated. The schema fake showed it for seven types (it filled
    read-only values outside the enums; it now keeps to them).
    Options: drop value validation (`enum`, lengths, bounds, patterns
    already dropped) from `status` in `crdgen`, keeping types; or keep it
    as documentation and accept the risk.

    Recommended: drop it from `status` only. Cloud Control is the source
    of a status value, not the user, so validating it can only wedge an
    object; `spec` keeps its validation. Not changed until the builder
    answers.

    **Answered** (2026-11-11): the recommendation, value checks dropped
    from `status` only. `crdgen` keeps a status property's type and
    structure but writes no `enum`, `minLength`/`maxLength`,
    `minimum`/`maximum` or `minItems`/`maxItems` under `status`
    (`pattern` was already dropped everywhere); `spec` keeps all of them.
    Regenerated: 103 of the 292 Cloud Control CRDs changed.
    `TestNoValueChecksInStatus` fails if any committed CRD (Cloud Control
    and ACK-generated) has a value check under `status`, and
    `TestGenerateDropsValueChecksFromStatusOnly` checks the generator
    itself on every registry type.

21. **The live-test role at IAM's limit** (2026-11-11). With 292 Cloud
    Control types the sandbox template's role needs 10 managed policies
    of 6,144 characters (54,401 characters of actions, already one
    statement per service, `iamgen.Compact`): IAM's default maximum per
    role. The next type added makes `task cloudcontrol:generate` fail.
    Options:
    - the role holds only the kinds chosen for live runs: a `live: true`
      per type in `cloudcontrol/types.yaml` (or a list beside
      `liveTestActions`). Live runs are sampled anyway, and a kind new to
      the role needs a redeploy of the stack either way;
    - wildcards per service and verb (`ec2:Describe*`, `guardduty:Get*`),
      wider than the schemas ask for, in the sandbox account only;
    - raise the quota on managed policies per role to 20 (Service Quotas,
      in the sandbox account), which only defers it.

    Recommended: the first, starting with the kinds proven on moto (the
    moto recheck) and the free kinds the next live runs sample; the
    template then shrinks to a few policies. Until the builder answers,
    no more types are added (the moto upgrades go on).

    **Answered** (2026-11-11): the first option. The live-test
    role holds permissions only for the kinds sampled on live runs,
    starting with the free ones; every other kind is proven on the fakes
    only. Built as `liveKinds`, a list beside `liveTestActions` in
    `cloudcontrol/types.yaml` (first: `AWS::IAM::SAMLProvider`,
    `AWS::SSO::PermissionSet`, `AWS::SSO::Assignment`, the kinds with an
    end-to-end test that AWS does not charge for); the registry marks them
    `Live`, the sandbox template is built from them alone (one managed
    policy) and the sweeper lists them alone. The limit stays IAM's
    default of 10 managed policies: the generator fails beyond it and
    `TestLiveRoleWithinManagedPolicyLimit` checks the committed template.
    The type freeze above is lifted.
