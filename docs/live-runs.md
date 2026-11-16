# Live runs: the kinds against a real AWS account

Every kind, Cloud Control or ACK-generated, is **untested on real AWS**
until it has a row in the ledger below (`docs/cloudcontrol.md`, decision
1; for the ACK-generated kinds `docs/plan.md` section 8 item 3 lists what
each run must check). Two suites run live: the Cloud Control tests
(`cloudcontrol/test/e2e`, `SUITE=cloudcontrol`, the default) and the
ACK-kind tests (`tests/e2e`, `SUITE=ack`, since 2026-11-15, HFL-623). This
file says how a live run is made, what keeps it apart from everything else,
and what each run found. The GCP repository's `docs/live-runs.md` is the
model.

**Region (2026-11-15, run `ackx-11151659-72c3`):** live runs go to
us-east-2 (`LIVE_REGION`, default `live.DefaultRegion`). The sandbox
account's organization has a service control policy that denies every
other region's regional services: in us-west-2, where the tests ran
before, Cloud Control `ListResources` was refused "with an explicit deny
in a service control policy", and EC2 is refused in us-west-2 and
us-east-1 alike. The tests keep us-west-2 on the fakes; on a live run
both suites take the run's region (`region` in `cloudcontrol/test/e2e`,
`harness.Region` in `tests/e2e`). A kind whose test needs a second
region (a KMS replica key, ECR cross-region replication) cannot run
live until the policy allows one.

**Status, 2026-11-15:** the sandbox account is the AWS test account
`hfl-test` (`$AWS_SANDBOX_ACCOUNT_ID`), created 2026-11-14 for tests only.
The stack of 2026-11-10 sat in the wrong account and the builder deleted
it (Open decisions O26c), which is why runs from 2026-11-12 were refused.
Earlier (2026-11-10): the sandbox account was the O5 account. The builder deployed the stack
`ack-extensions-live-tests` there (in us-east-2; IAM is global), trusting the offload runner service account
`$OFFLOAD_SA` in the new organization's dev project, so live runs start
offload VMs in that project. The identifiers come from the environment
(a git-ignored `.env`; `.env.example` lists them), never from the
repository:

```sh
set -a; . ./.env; set +a   # AWS_SANDBOX_ACCOUNT_ID, OFFLOAD_PROJECT, OFFLOAD_BUCKET, OFFLOAD_SA
../offload/offload run --machine e2-standard-8 --sa "$OFFLOAD_SA" ...
```

That project has 12 vCPUs of quota: one 8-vCPU VM at a time.

## What keeps it apart

Only one path in the repository may reach real AWS, and `task check`
cannot take it:

| Lock | Where |
| --- | --- |
| The code that holds real credentials is compiled only with `-tags live` | `cloudcontrol/test/live`, `hack/live/sweep`, `cloudcontrol/test/e2e/live_test.go`, `tests/e2e/live_test.go` |
| No task but `test-live`, `live-sweep` and `live-list` mentions `hack/live`, `-tags live` or the real target, and none calls them; `task check` compiles the live code with `go vet` and runs none of it | `hack/noaws` rules L1 and L2, in `task check` |
| The Taskfile sets `CLOUDCONTROL_E2E_TARGET=fake` for every task; built without `-tags live`, `real` fails | `Taskfile.yml`, `cloudcontrol/test/e2e/harness_test.go` |
| An explicit opt-in on every run: `ACK_EXT_LIVE=create-real-aws-resources` | `hack/live/run.sh`, `cloudcontrol/test/live` |
| The account must be `AWS_SANDBOX_ACCOUNT_ID` (from the environment; unset refuses the run), and STS must say the credentials are that account's | the same |
| Federated credentials only; stored keys, a profile or an endpoint override in the environment refuse the run | `hack/live/credentials.sh`, `cloudcontrol/test/live` |
| The manager never holds real credentials: it still runs under `pkg/fakeaws` (fake keys, dead proxy, a loopback endpoint), and a recorder in the test process signs its requests again with the run's credentials and forwards only Cloud Control and STS `GetCallerIdentity` | `cloudcontrol/test/live/recorder.go` |

For the ACK-kind tests the forwarder (`cloudcontrol/test/live/forward.go`)
stands where moto stands: the managers and the tests' own SDK clients
keep pkg/fakeaws's loopback endpoint and fake keys; the forwarder reads
each request's service and region from its fake SigV4 scope, signs it
again with the run's credentials, and sends it to that service's real
endpoint (IAM and Organizations at their global endpoints, S3 Control at
the sandbox account's own host). Services not on its list are refused.
The tests reach the forwarder through `harness.Live`, which only
`tests/e2e/live_test.go` sets (`ACK_TEST_BACKEND=live`).

The recorder also keeps the tests' call-log checks (one `replace` per change,
no update on a resync) valid on a live run: it records every Cloud Control
call as the fake does.

## Credentials: federation from the offload VMs

No AWS keys are stored anywhere. On an offload VM started with `--sa`,
`hack/live/credentials.sh` asks Google's metadata server for an ID token of
the VM's service account, with the audience `ack-extensions-live-tests`,
writes it to a file, and exports `AWS_WEB_IDENTITY_TOKEN_FILE` and
`AWS_ROLE_ARN`; the AWS SDK then calls `AssumeRoleWithWebIdentity`. A
background loop writes a fresh token every 30 minutes (tokens last an
hour); the role's sessions last an hour at most.

## The live-test role: a catalog chart instance

Setup like this role goes through Bundt's own machinery, catalog charts on
ACK, never hand-written CloudFormation or CLI commands (the builder's rule,
bundt `asks/estate-answers.md`, 2026-11-13). The role
`ack-extensions-live-tests` is an instance of the
catalog's
[`google-federated-role`](https://github.com/highfivelabs/catalog/tree/main/charts/aws/google-federated-role)
chart: `charts/aws/google-federated-role/tests/ack-extensions-live-tests.values.yaml`
there. It renders the same role, trust (IAM's built-in
`accounts.google.com` principal, conditions on `sub` and `oaud`, no IAM
OIDC provider), session limit, tag and managed policy statements as the
CloudFormation template that made it (`7d67acc`), compared statement for
statement on 2026-11-15 (the chart's README, "The ack-extensions live-test
role"). It differs in two ways: its managed policy is named
`ack-extensions-live-tests-part-1` (CloudFormation generated one), and the
budget alarm is not in it (no ACK controller or ack-extensions kind serves
AWS Budgets yet: a gap here, so the alarm stays in the stack).

The instance carries a placeholder unique ID (the catalog is public); the
real one is set where it is deployed (`--set-string
google_service_account_unique_id=...`, from `gcloud iam service-accounts
describe "$OFFLOAD_SA" --format='value(uniqueId)'`).

**The builder's answer (2026-11-15, item AWSROLE-ADOPT):** "as recommended
(Deploy the chart instance through ACK with the role adopted in place (same
name, no gap in live runs), check one task test-live run, then delete the
CloudFormation stack and drop the template's role and policies from the
generator's output. Keep the budget alarm as the one CloudFormation piece
until ack-extensions serves AWS Budgets.)"

**Status, 2026-11-15: deployed, the role adopted in place.** On an offload
Spot VM, a kind cluster ran ACK's iam-controller v1.11.3 (the catalog's
pin) with the test account's admin credentials (region us-east-2, the only
one the organization's SCP allows; ACK's own resource tags off). The chart
instance was rendered with the offload service account's real unique ID,
`deletion_policy: ABANDON` (so deleting an object never deletes the role or
policy) and the stack's two tags (`hfl.dev/owner`, `hfl.dev/purpose`) beside
the chart's `purpose`; the `Role` carried
`services.k8s.aws/adoption-policy: adopt-or-create`. What it showed:

- before applying, the render's trust policy and managed policy document
  equalled the live role's, statement for statement;
- ACK adopted the role (`services.k8s.aws/adopted: "true"`, both objects
  `ACK.ResourceSynced`); its one change was the attached policy: it made
  `ack-extensions-live-tests-part-1` (the same document) and attached it in
  place of the stack's `ack-extensions-live-tests-LiveTestPolicy1-...`. The
  role kept its `RoleId` and creation date: it was never deleted or
  recreated;
- the kind cluster and the VM were deleted after; nothing in a cluster
  manages the role now. To change it (a new `liveKinds` entry), apply the
  instance again the same way (`adopt-or-create` adopts it again);
- `task test-live` assumed the adopted role on the next run (ledger,
  2026-11-15).

Then the stack let go of the role and its policy without deleting them: an
update set `DeletionPolicy: Retain` on both, and the next update, from the
regenerated template, removed them (`DELETE_SKIPPED`, 2026-11-16). The
stack-made policy `ack-extensions-live-tests-LiveTestPolicy1-...` is still
in the account, detached (no attachments) and in no stack: nothing uses it,
and deleting it is left to the builder. The stack keeps the budget alarm
only.

## The live-test role's policies and the sandbox account's template

`task cloudcontrol:generate` writes both from `cloudcontrol/types.yaml`
(`task check` fails if either is stale):

- `cloudcontrol/config/live/role-policies.yaml`: the role's managed
  policies, as the chart instance's `policies` value (keys `part-1`, ...).
  They hold the Cloud Control actions and handler permissions of the kinds
  sampled on live runs (`liveKinds` in `types.yaml`), their list handlers'
  and `cloudformation:ListResources` for the sweeper, and the tests' own
  fixtures (`liveTestActions`), split to IAM's 6,144-character limit and
  kept within IAM's default of 10 managed policies per role (the generator
  fails beyond it, and `TestLiveRoleWithinManagedPolicyLimit` checks the
  committed file). Every other kind is proven on the fakes only (plan
  question 21): a live run of a test whose kind is not in `liveKinds` is
  refused by IAM. Adding a kind to `liveKinds` needs these statements
  copied into the catalog instance and the instance applied again. The
  trust (IAM's built-in `accounts.google.com` principal, conditions on
  `accounts.google.com:sub`, the service account's unique ID, and
  `accounts.google.com:oaud`, the audience `ack-extensions-live-tests`, at
  most an hour) is the chart's. No IAM OIDC provider: one for
  `https://accounts.google.com` takes over every Google ID token in the
  account and refuses any whose audience is not on its client ID list; the
  first deploy in `hfl-test` (2026-11-15) broke the account's other
  Google-federated role (`hfl-test-admin`, audience `sts.amazonaws.com`)
  until the stack was deleted.
  For the ACK-kind suite the policies also hold statement
  `LiveRunACKKinds` (`hack/cloudcontrol-gen/liveack.go`): every
  ACK-generated kind's calls as `task ack:iam` reads them (`pkg/ackiam`),
  the Organizations kinds left out (they need the organization's
  management account, which the sandbox account is not); every SDK
  operation the tests call themselves (each `&<service>.<Operation>Input`
  in `tests/e2e`, the Organizations tests left out); and
  `tests/e2e/live-actions.yaml` (ACK's own kms and s3 controllers that the
  extension tests run, fixtures' extra actions, the sweeper's ACK pass). A
  test that adds a fixture call needs the file regenerated, its statements
  copied into the catalog instance, and the instance applied again.
- `cloudcontrol/config/live/sandbox-account.yaml`: the stack
  `ack-extensions-live-tests` (us-east-2), holding only `BudgetAlarm`, a
  monthly cost budget (default USD 50) with e-mail alerts at 80% of actual
  and 100% of forecast spend. It stays CloudFormation until an ACK
  controller or an ack-extensions kind serves AWS Budgets. To change it, in
  the sandbox account only (profile `hfl-test`, the account's keyless admin
  role; never the management or Identity Center account):

```sh
AWS_PROFILE=hfl-test aws cloudformation deploy --stack-name ack-extensions-live-tests --region us-east-2 \
  --template-file cloudcontrol/config/live/sandbox-account.yaml \
  --parameter-overrides AlertEmail=<address>
```

Set `AWS_SANDBOX_ACCOUNT_ID` to the account ID in the `.env` of whoever
starts live runs (never in the repository).

**Found on the first deploy (2026-11-10, run `ackx-11101802-5696`):** the
trust works. The VM's ID token for `$OFFLOAD_SA`
carries `iss` `https://accounts.google.com`, `aud`
`ack-extensions-live-tests`, and `azp` = `sub` = the service account's unique ID;
`AssumeRoleWithWebIdentity` into `ack-extensions-live-tests` succeeded and
STS named account `$AWS_SANDBOX_ACCOUNT_ID`. The provider lists both the audience
and the unique ID, so this run cannot tell which claim IAM compares with
the list; removing one of the two client IDs and running again would
tell. The role's permissions had a gap: the template's sweeper and fixture
statement (`LiveRunSweeperAndFixtures`) was dropped when generated, so the
sweep before the run was refused `cloudformation:ListResources` and the
run stopped before any test reached AWS (nothing was created). Fixed in
`hack/cloudcontrol-gen/live.go`; the stack needs redeploying from the
regenerated template.

**What was to be confirmed on the first deploy:**

- IAM also accepts `accounts.google.com` as a built-in federated principal
  without an OIDC provider resource; the template makes the provider, as
  required. Which token claim IAM compares with the provider's
  client ID list for Google (`aud`, the audience, or `azp`, the service
  account's unique ID) is why both are listed. For the built-in principal
  AWS documents `accounts.google.com:aud` as the `azp` claim,
  `accounts.google.com:oaud` as `aud` and `accounts.google.com:sub` as
  `sub`; the trust policy conditions only on `sub`, the unique ID, which
  no other principal's token carries.
- The role's resources are `*` (the schemas name actions, not ARNs);
  scoping is a reviewed step after the first run shows what is used.

## Running one

On an offload VM (credentials need its service account):

```sh
../offload/offload run --sa "$OFFLOAD_SA" --prep 'task tools' -- \
  env ACK_EXT_LIVE=create-real-aws-resources AWS_SANDBOX_ACCOUNT_ID="$AWS_SANDBOX_ACCOUNT_ID" \
  task test-live ACCOUNT="$AWS_SANDBOX_ACCOUNT_ID" \
  ROLE_ARN="arn:aws:iam::$AWS_SANDBOX_ACCOUNT_ID:role/ack-extensions-live-tests" TESTS=TestDetector
```

`hack/live/run.sh` runs the tests against the fake, then against the
account with `CLOUDCONTROL_E2E_TARGET=real`. `SUITE=ack` runs `tests/e2e`
instead (moto, then `ACK_TEST_BACKEND=live`); `TESTS` picks tests in
either suite. One `-run` pattern per run: give several tests as one
quoted value, `TESTS='TestA TestB'`. Every resource the manager
makes carries the tag `hfl.dev/live-run=<run ID>` (ACK's `--resource-tags`,
filtered from specs like ACK's own tags). Before the run, leftovers of runs
older than 2 hours are swept; after it, pass or fail, everything of the run
is swept and what is left is listed. Output:
`offload-artifacts/live/<run ID>/` (`summary.md`, `fake.log`, `live.log`,
`left.txt`).

The sweeper's ACK pass (`--suite ack`, or `all`, the default) finds what
the ACK managers made by the run tag where it is billed or lasts: EC2
volumes, customer gateways, VPN gateways and connections, and KMS keys
(scheduled for deletion, 7 days), in us-west-2, us-east-1 and
us-east-2; every other ACK kind and every untagged fixture is deleted by
its test. `--list` also prints the IAM users, groups, customer-managed
policies, virtual MFA devices, account alias and password policy, none
of which the sandbox account has of its own. `run.sh` sweeps its own
suite only.

The sweeper (`task live-sweep`, `task live-list`, the same opt-in and
federated credentials) lists every live kind (`liveKinds`; no live run
makes another) through Cloud Control `ListResources` and deletes what
carries the run tag. Types that take no
tags (`Assignment`, `InstanceAccessControlAttributeConfiguration`) are
deleted by the tests that made them; a run cut off before then leaves them,
and `left.txt` cannot show them.

**What the first runs will need** (the tests were written for moto):
`TestSAMLProvider`'s metadata and private key are placeholders that real
IAM will refuse; `TestDetector` fails in an account that already has a
detector (one per account and region); `TestIdentityCenter` needs Identity
Center enabled in the account; `TestTransitGatewayRouteTable` makes a
transit gateway (billed hourly) and deletes it at the end, but a run cut
off before then leaves it, untagged. `TestTransitGatewayRouting` makes two
transit gateways, a VPC with a subnet, a VPC attachment and a route table
with the EC2 SDK, untagged, and deletes them at the end; on AWS the
gateways and the attachment take minutes to become `available` (the test
does not wait for that yet), the attachment is billed hourly, and the
peering request between the two gateways stays `pendingAcceptance`.
`TestDNS` makes a public hosted zone, a log group and a VPC, untagged,
and deletes them at the end; on AWS a query
logging configuration needs the log group in us-east-1 with a resource
policy letting Route 53 write to it, which the test does not make yet. Each is a fixture change, made with the first run of that test.

## Ledger

| Date | Run | Commit | Account | Tests | Fake | Live | Found |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 2026-11-10 | `ackx-11101802-5696` | `a448a3d` + the account | `$AWS_SANDBOX_ACCOUNT_ID` | TestDetector | PASS | not run | Trust passed. The sweep before the run was refused `cloudformation:ListResources` (the template dropped `LiveRunSweeperAndFixtures`); the run stopped there, nothing created |
| 2026-11-12 | `ackx-11121603-a0b7` | `52556a2` | `$AWS_SANDBOX_ACCOUNT_ID` | TestSAMLProvider | PASS | not run | **Trust refused.** From an offload VM run with `--sa "$OFFLOAD_SA"` in `$OFFLOAD_PROJECT`, `AssumeRoleWithWebIdentity` into `ack-extensions-live-tests` answered `AccessDenied: Not authorized to perform sts:AssumeRoleWithWebIdentity` (four tries: the sweep before, and the clean-up after). Nothing reached AWS beyond STS, nothing was created. The same trust passed on 2026-11-10; the role's trust policy, the OIDC provider's client IDs or the service account's unique ID has changed since. Needs the builder (IAM is not changed from here) |
| 2026-11-15 | `ackx-11151659-72c3` | `3fd2f99` | `$AWS_SANDBOX_ACCOUNT_ID` | TestSAMLProvider, TestIdentityCenter (in us-west-2) | PASS | not run | **Trust passed** (the built-in Google principal, PR #107). The sweep before the tests was refused `cloudformation:ListResources` in us-west-2 "with an explicit deny in a service control policy": the organization allows regional services in us-east-2 only. Live runs moved to us-east-2 (`LIVE_REGION`). Nothing created |
| 2026-11-15 | `ackx-11151707-7cc5` | `5b4c2bb` | `$AWS_SANDBOX_ACCOUNT_ID` | TestSAMLProvider, TestIdentityCenter | PASS | not run | The sweep listed `AWS::IAM::SAMLProvider` (none left); `AWS::SSO::PermissionSet`'s list was refused without the instance ("Required property: [InstanceArn]"). The sweeper now lists per Identity Center instance. Nothing created |
| 2026-11-15 | `ackx-11151713-11b9` | `7c50294` | `$AWS_SANDBOX_ACCOUNT_ID` | TestSAMLProvider, TestIdentityCenter | PASS | FAIL | Sweeps clean (no Identity Center instance in the account: nothing to list). **IdentityCenter (`PermissionSet`, `Assignment`): pending**: the sandbox account has no Identity Center instance (`sso:ListInstances` is empty; the organization's instance lives in its management account), so the test stops before it makes anything. **SAMLProvider**: the manager lost its leader lease 12 s after start (the client rate limiter, while 1,345 kinds' informers started) and exited, so the object was never reconciled; nothing reached IAM. Rerun once (the handoff's rule for a timeout) |
| 2026-11-15 | `ackx-11151722-eda8` | `7c50294` | `$AWS_SANDBOX_ACCOUNT_ID` | TestSAMLProvider | PASS | FAIL | **IAM `SAMLProvider` on real Cloud Control:** the create with a write-only private key from a Secret, the ARN in status, the metadata read back through IAM, an update sent as one replace of `/SamlMetadataDocument`, the create-only refusals, and the delete all passed. Real IAM parses the metadata's certificate and the private key (the fixture now makes a real pair; moto took placeholders). One check failed on a fake-only detail: the fake's request tokens end in `-create`, real ones are UUIDs, so the count of the create's status polls was 0; the test now counts the polls before the first update on a live run. Sweeps clean |
| 2026-11-15 | `ackx-11151728-fbe4` | `3cfe5f4` | `$AWS_SANDBOX_ACCOUNT_ID` | TestSAMLProvider | PASS | FAIL | The manager lost its leader lease 7 s after start, as in `ackx-11151713-11b9` ("Failed to update lease optimistically ... client rate limiter Wait returned an error: context deadline exceeded", then "leader election lost"), and exited; nothing reached IAM. Two of three live runs of the Cloud Control manager so far, none of the fake runs: the lease renewal waits behind the start of 1,345 kinds' informers in the one client rate limiter. Recorded in plan section 8 |
| 2026-11-15 | `ackx-11151735-cca8` | `3cfe5f4` | `$AWS_SANDBOX_ACCOUNT_ID` | TestSAMLProvider | PASS | PASS | **IAM `SAMLProvider` (Cloud Control) passes on real AWS**, us-east-2: create with the write-only private key, status, one-replace update, create-only refusals, delete; the create's status polled once (real Cloud Control) where the fake answers IN_PROGRESS twice. Sweeps clean, nothing left (`left.txt` empty) |
| 2026-11-16 | `ackx-11160005-069c` | `93aafbe` | `$AWS_SANDBOX_ACCOUNT_ID` | TestSAMLProvider | PASS | not run | First run on the role adopted by ACK (AWSROLE-ADOPT): **trust passed** (`AssumeRoleWithWebIdentity` into the adopted role). The sweep before was refused `cloudformation:ListResources` in us-west-2 by the organization's SCP (main still defaults to us-west-2; PR #111 moves live runs to us-east-2). Nothing created |
| 2026-11-16 | `ackx-11160010-bf3d` | `93aafbe` | `$AWS_SANDBOX_ACCOUNT_ID` | TestSAMLProvider (`LIVE_REGION=us-east-2`) | PASS | not run | The adopted role's policy let the sweep list `AWS::IAM::SAMLProvider` in us-east-2; main's sweeper then failed on `AWS::SSO::PermissionSet` ("Missing Or Invalid ResourceModel", no `InstanceArn`; fixed in PR #111). Nothing created |
| 2026-11-16 | `ackx-11160028-462f` | `4c56e3e` (PR #111) | `$AWS_SANDBOX_ACCOUNT_ID` | TestSAMLProvider | PASS | FAIL | Sweeps clean. The manager lost its leader lease 7 s after start (client rate limiter), as in `ackx-11151713-11b9`; nothing reached IAM. Rerun once |
| 2026-11-16 | `ackx-11160035-feaf` | `4c56e3e` (PR #111) | `$AWS_SANDBOX_ACCOUNT_ID` | TestSAMLProvider | PASS | PASS | **The catalog chart instance's role passes a live run** (adopted by ACK in place, policy `ack-extensions-live-tests-part-1`), us-east-2. Sweeps clean, nothing left. The CloudFormation stack then let go of the role and policy (budget alarm only) |

## Kinds

One row per kind: its last live result, or why it has none. Updated with
each run; the ledger above has the runs.

| Route | Service | Kind | Test | Live |
| --- | --- | --- | --- | --- |
| Cloud Control | iam | SAMLProvider | TestSAMLProvider | PASS, `ackx-11151735-cca8` (2026-11-15); the manager lost its lease in two earlier runs (plan section 8 item 17) |
| Cloud Control | sso | PermissionSet | TestIdentityCenter | pending: no Identity Center instance in the sandbox account (the organization's is in its management account) |
| Cloud Control | sso | Assignment | TestIdentityCenter | pending: as PermissionSet |
| ACK | applicationautoscaling | ScheduledAction | TestApplicationScheduledAction | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestApplicationScheduledAction` |
| ACK | autoscaling | ScalingPolicy | TestScalingPolicy | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestScalingPolicy` |
| ACK | autoscaling | ScheduledAction | TestScheduledAction | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestScheduledAction` |
| ACK | ec2 | CustomerGateway | TestCustomerGateway | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestCustomerGateway` |
| ACK | ec2 | DefaultSecurityGroup | TestDefaultSecurityGroup | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestDefaultSecurityGroup` |
| ACK | ec2 | NetworkInterfacePermission | (none: moto has no API) | pending: no test yet (moto has none of the API, plan 6.33); a live test needs a second account to grant to, and the catalog instance updated and applied again |
| ACK | ec2 | ResourceTag | TestResourceTag | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestResourceTag` |
| ACK | ec2 | Volume | TestVolume | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestVolume` |
| ACK | ec2 | VolumeAttachment | TestVolumeAttachment | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestVolumeAttachment` |
| ACK | ec2 | VPNConnection | TestVPNConnection | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestVPNConnection` |
| ACK | ec2 | VPNGateway | TestVpnGateway | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestVpnGateway` |
| ACK | ec2 | VPNGatewayRoutePropagation | (none: moto has neither call) | pending: no e2e test yet (moto has neither call, plan 6.34); a live test needs one written against a VPC route table, and the catalog instance updated and applied again |
| ACK | ecr | ReplicationConfiguration | TestReplicationConfiguration | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestReplicationConfiguration` |
| ACK | eventbridge | APIDestination | TestApiDestination | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestApiDestination` |
| ACK | eventbridge | Connection | TestConnection | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestConnection` |
| ACK | iam | AccessKey | TestIAMAccessKey | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestIAMAccessKey` |
| ACK | iam | AccountAlias | TestAccountAlias | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestAccountAlias` |
| ACK | iam | AccountPasswordPolicy | TestAccountPasswordPolicy | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestAccountPasswordPolicy` |
| ACK | iam | LoginProfile | TestLoginProfile | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestLoginProfile` |
| ACK | iam | RolePolicyAttachment | TestRolePolicyAttachment | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestRolePolicyAttachment` |
| ACK | iam | SSHPublicKey | TestSSHPublicKey | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestSSHPublicKey` |
| ACK | iam | UserGroupMembership | TestUserGroupMembership | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestUserGroupMembership` |
| ACK | iam | VirtualMFADevice | TestIAMVirtualMFADevice | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestIAMVirtualMFADevice` |
| ACK | kms | ReplicaKey | TestReplicaKey | pending: needs a second region, which the organization's service control policy denies (us-east-2 only); and the catalog instance updated and applied again |
| ACK | kms | Key extension (ACK's Key) | TestKeyExtension | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestKeyExtension` |
| ACK | lambda | Permission | TestPermission | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestPermission` |
| ACK | organizations | AccountPlacement | TestOrganizationsAccountPlacement | pending: organization-level identity (plan 7.5); the sandbox account is a member, not the management account |
| ACK | organizations | DelegatedAdministrator | TestOrganizationsDelegatedAdministrator | pending: organization-level identity (plan 7.5); the sandbox account is a member, not the management account |
| ACK | organizations | Organization | TestOrganizationsOrganization | pending: organization-level identity (plan 7.5); the sandbox account is a member, not the management account |
| ACK | organizations | Policy | TestOrganizationsPolicy | pending: organization-level identity (plan 7.5); the sandbox account is a member, not the management account |
| ACK | organizations | PolicyAttachment | TestOrganizationsPolicyAttachment | pending: organization-level identity (plan 7.5); the sandbox account is a member, not the management account |
| ACK | rds | DBProxyTarget | TestDBProxyTarget | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestDBProxyTarget` |
| ACK | rds | DBRoleAssociation | TestDBRoleAssociation | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestDBRoleAssociation` |
| ACK | rds | OptionGroup | TestOptionGroup | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestOptionGroup` |
| ACK | s3 | Bucket extension (ACK's Bucket) | TestBucketExtension | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestBucketExtension` |
| ACK | s3control | PublicAccessBlock | TestPublicAccessBlock | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestPublicAccessBlock` |
| ACK | scheduler | Schedule | TestSchedule | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestSchedule` |
| ACK | scheduler | ScheduleGroup | TestScheduleGroup | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestScheduleGroup` |
| ACK | wafv2 | WebACLAssociation | TestWebACLAssociation | pending: the role lacks this kind's permissions; update the catalog instance (statement `LiveRunACKKinds`) and apply it again, then `task test-live SUITE=ack TESTS=TestWebACLAssociation` |
