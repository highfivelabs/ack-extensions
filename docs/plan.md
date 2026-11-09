# ack-extensions plan

Status, 2026-11-08: the repository has four kinds:

- `iam.aws.hfl.dev` `RolePolicyAttachment`;
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
   generator recognises (`OptionGroup`). The `generator.yaml` entry and
   hook templates are ACK's, copied from upstream where they exist, and
   the generated code is regenerated here, never copied.
2. **Hand-written on ACK's runtime interfaces**, when the API's shape is
   not one the generator reads:
   - `AttachRolePolicy`/`DetachRolePolicy` (`RolePolicyAttachment`, as
     upstream PR #184 does);
   - `RegisterDBProxyTargets`/`DeregisterDBProxyTargets` (`DBProxyTarget`);
   - `AddRoleToDBCluster`/`AddRoleToDBInstance` and their
     `RemoveRoleFrom...` (`DBRoleAssociation`).

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

### 4.3 Results (2026-11-08, macOS arm64, Go 1.27.1, `make test`)

```text
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
- **An association's status.** moto marks a new association `ACTIVE` at
  once. On AWS it is `PENDING` first, and `INVALID` when RDS cannot assume
  the role. The rule (synced only when `ACTIVE`) is unit-tested.
- **Role ARNs and features.** moto checks neither that the role exists
  nor that the engine supports the feature (AWS does both). It refuses a
  second association with the same role or the same feature, as AWS
  does.

## 5. Candidates, ranked

Read on 2026-11-07 with read-only `gh` against `aws-controllers-k8s/*`,
which has 80 or so controller repositories, and against the gap scan
`krm-gapscan-results-2026-10-19` (AWS part: 1,732 Terraform resources,
470 with an ACK kind; read as data, unverified). Ranked by how likely a
typical application stack (a database, its secrets and access, a
workload's IAM, DNS, a registry) is to block on it.

**Rechecked 2026-11-08** (read-only `gh`, and `git fetch` of
rds-controller), before building `DBRoleAssociation`:

- rds-controller: latest release `v1.14.2` (2026-11-03), and `main` has
  nothing after it. Its open pull requests are #335 (tags-only updates),
  #293 (`DBInstanceParameterGroupName` on `DBCluster`) and #257
  (`OptionGroup`). Searching its code, issues and pull requests, and
  `community`'s issues, for `AddRoleToDBCluster`, `AddRoleToDBInstance`
  and associated roles finds nothing. `DBCluster` and `DBInstance` carry
  `associatedRoles` in their status only.
- The pull requests in table (a): ssm-controller #85, ecr-controller
  #121, secretsmanager-controller #69, acm-controller #104 and
  iam-controller #184 are open and conflict with their base.
  elasticache-controller #237 (`GlobalReplicationGroup`) is open,
  mergeable and was updated on 2026-11-06, so it may be close to merging:
  wait for it.
- Secrets Manager resource policy: no secretsmanager-controller or
  `community` issue or pull request.

Why `DBRoleAssociation` came next: this table's ranks 3 to 5 wait on open
question 1 or would change ACK's own `Role`; rank 6 is marked "not an
application-stack need", the measure this ranking uses; rank 7 may be
close to merging. Table (b) marked RDS role associations "next", first.

### (a) Upstream code exists, not released

| Rank | Candidate | Upstream | Why | Done |
| --- | --- | --- | --- | --- |
| 1 | `RolePolicyAttachment` | iam-controller PR #184 (open since 2026-06, conflicts with base) | grant a workload access by attaching a policy to a role the stack does not own (EKS node roles, a platform team's roles) | built |
| 2 | `OptionGroup` | rds-controller PR #257 (open since 2025-12, conflicts with base) | MySQL, MariaDB, SQL Server and Oracle features (audit plugin, native backup) need an option group; ACK ignores the kind | built |
| 3 | Secrets Manager rotation on `Secret` | secretsmanager-controller PR #69 | rotating database passwords | not built: a field on an existing kind (open question 1) |
| 4 | `Role` additive policy mode | iam-controller PR #184, second commit | lets an ACK `Role` and a `RolePolicyAttachment` share a role (section 6.1) | not built: changes ACK's own `Role` |
| 5 | ACM import from a Kubernetes TLS Secret | acm-controller PR #104 | bring-your-own certificates | not built: field (open question 1) |
| 6 | SSM `Association` | ssm-controller PR #85 | agent configuration on instances | not built: not an application-stack need |
| 7 | ElastiCache `GlobalReplicationGroup` | elasticache-controller PR #237 (mergeable, active 2026-11-06) | cross-region caches | not built: may be close to merging |
| 8 | ECR `ReplicationConfiguration` | ecr-controller PR #121 | an account-wide singleton | not built |
| 9 | CloudWatch Logs metric filters, EC2 `KeyPair`, KMS replica keys | PRs #61, #396, #112 | | not built |

### (b) In the AWS API, in no ACK code or pull request

| Candidate | Why | Done |
| --- | --- | --- |
| `DBProxyTarget` (`RegisterDBProxyTargets`) | ACK's `DBProxy` cannot be used without it: a proxy with no target accepts no connections. No ACK code or issue covers it (code search, 2026-11-07). | built |
| RDS role associations (`AddRoleToDBCluster`/`AddRoleToDBInstance`) | Aurora's S3 import and export, and Lambda calls; RDS for PostgreSQL's S3 import and export; SQL Server's and Oracle's S3 integration. No ACK code, issue or pull request (2026-11-08). moto supports adding them, not removing them (section 4.4). | built: `DBRoleAssociation` |
| Secrets Manager resource policy (`PutResourcePolicy`) | cross-account access to a secret | next |
| Route 53 DNSSEC and key-signing keys, S3 access grants, RDS event subscriptions | ignored in ACK's generator configurations | later |

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
ok: CRDs installed: rolepolicyattachments.iam, dbproxytargets.rds, dbroleassociations.rds, optiongroups.rds (*.aws.hfl.dev); operator running
== identity own: our manager beside ACK's, as ACK's role
ok: manager ack-system/ack-extensions-rds-manager rolled out
ok: manager pod runs as ack-system/ack-extensions-rds-manager, annotated with ACK's role arn:aws:iam::123456789012:role/ack-rds-controller
ok: same credentials Secret (aws-creds), region (us-west-2) and endpoint as ACK's controller
ok: no iam manager: no ACK iam-controller is installed
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
10. The heavy checks run on a Spot VM (`../offload`), not the laptop. A
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
