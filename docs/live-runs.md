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

**Applied again, 2026-11-16 (HFL-623), three times:** with the ACK-kind
suite's statements (catalog #229), then the Auto Scaling and fixture
clean-up actions (#231), then the IAM, ECS and Lambda clean-ups (#233).
Each time the same way on an offload Spot VM: the chart instance rendered
with the real unique ID, `deletion_policy: ABANDON`, the stack's tags,
`adopt-or-create` on every object, ACK's resource tags off. A `Policy` is
adopted by its ARN, not its name: each existing one also carries
`services.k8s.aws/adoption-fields: {"arn": "arn:aws:iam::<account>:policy/<name>"}`
(without it ACK called `CreatePolicy` and was refused
`EntityAlreadyExists`). The role now has three policies, `part-1` to
`part-3`, split to 5,900 characters (the chart writes a longer document
than iamgen measures). The temporary admin credentials went to the
offload bucket for the run and were deleted after it.

**Applied a fifth time, 2026-11-16 (HFL-623 follow-ups):** with the
calls of the default EC2 kinds (ACKQ16-1: `DefaultVPC`,
`DefaultNetworkACL`, `DefaultRouteTable`; `ec2:CreateDefaultVpc`,
`ec2:ReplaceNetworkAclEntry` and others), their fixtures' clean-up
(`ec2:DeleteNetworkAcl`) and `s3:PutObjectRetention` (the Bucket
extension's Object Lock test), from the catalog branch of the follow-up
PR, the same way (offload run `off-apply-1116064933-4643`; all four
objects `ACK.ResourceSynced`, each existing policy adopted by its ARN).
Each policy's default version was then read back with the account's
admin profile: all three equal the render, statement for statement, each
attached once. The temporary credentials object was deleted after.

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
| 2026-11-16 | `ackx-11160252-3835` | `9423b2e` (PR #111) | `$AWS_SANDBOX_ACCOUNT_ID` | SUITE=ack: TestApiDestination, TestConnection, TestSchedule, TestScheduleGroup | PASS | FAIL | **First ACK-kind run on the role with `LiveRunACKKinds`** (catalog #229, applied through ACK). ApiDestination, Connection, Schedule PASS. ScheduleGroup FAIL: its tag check counted the run's tag `hfl.dev/live-run` (fixed). Left behind and deleted by hand: the retained connection `kept` and destination `audit`, the connection `partner`, schedule groups `reports` and `exports`, schedule `hourly-ping`, role `scheduler-sqs` (the tests now delete them) |
| 2026-11-16 | `ackx-11160252-142c` | `9423b2e` (PR #111) | `$AWS_SANDBOX_ACCOUNT_ID` | SUITE=ack: TestCustomerGateway, TestDefaultSecurityGroup, TestResourceTag, TestVolume, TestVolumeAttachment, TestVPNConnection, TestVpnGateway | PASS | FAIL | CustomerGateway, DefaultSecurityGroup, ResourceTag, Volume, VolumeAttachment, VPNConnection PASS. VpnGateway FAIL: `VpcLimitExceeded` (five VPCs per region), the other tests' fixture VPCs never deleted. Swept by tag: volumes, a VPN gateway, customer gateways. Left behind and deleted by hand: four VPCs, a t3.micro instance, a transit gateway (the tests now delete their fixtures) |
| 2026-11-16 | `ackx-11160256-57fe` | `9423b2e` (PR #111) | `$AWS_SANDBOX_ACCOUNT_ID` | SUITE=ack: the eight IAM tests | PASS | FAIL | The VM was preempted once in the fake phase and resumed. AccessKey, LoginProfile, UserGroupMembership, AccountPasswordPolicy, RolePolicyAttachment, VirtualMFADevice PASS. AccountAlias FAIL: `acme-prod` belongs to another AWS account (aliases are unique across AWS; a live run now takes `hfl-<run ID>`). SSHPublicKey FAIL: IAM returns the key without its comment (now compared without). Left behind and deleted by hand: 7 users, 4 groups, 2 roles, policy `app-s3-read`, 105 virtual MFA devices, the alias `someone-else` |
| 2026-11-16 | `ackx-11160302-80c8` | `9423b2e` (PR #111) | `$AWS_SANDBOX_ACCOUNT_ID` | SUITE=ack: TestApplicationScheduledAction, TestScalingPolicy, TestScheduledAction, TestScheduleGroup | PASS | FAIL | ScalingPolicy: `CreateAutoScalingGroup` refused `ec2:DescribeLaunchTemplateVersions` (added, catalog #231). ScheduledAction: the launch template the failed test left. ApplicationScheduledAction: Application Auto Scaling could not yet assume its ECS service-linked role. ScheduleGroup: the group the run before retained. Left behind and deleted by hand: launch template `web`, ECS cluster `app`, service `web`, task definitions |
| 2026-11-16 | `ackx-11160326-87d3` | `3fc0389` (PR #111) | `$AWS_SANDBOX_ACCOUNT_ID` | SUITE=ack: TestAccountAlias, TestSSHPublicKey | PASS | PASS | **IAM `AccountAlias` and `SSHPublicKey` pass on real AWS.** Left behind and deleted by hand: user `erin` |
| 2026-11-16 | `ackx-11160326-bbdb` | `3fc0389` (PR #111) | `$AWS_SANDBOX_ACCOUNT_ID` | SUITE=ack: TestApplicationScheduledAction, TestScalingPolicy, TestScheduledAction, TestScheduleGroup | PASS | FAIL | **Auto Scaling `ScalingPolicy`, `ScheduledAction` and Scheduler `ScheduleGroup` pass on real AWS** (role from catalog #231). ApplicationScheduledAction FAIL: `CreateService` not idempotent against the service the run before left. Left behind and deleted by hand: schedule group `reports`, the ECS cluster and service |
| 2026-11-16 | `ackx-11160326-b656` | `3fc0389` (PR #111) | `$AWS_SANDBOX_ACCOUNT_ID` | SUITE=ack: the seven EC2 tests | PASS | FAIL | CustomerGateway, DefaultSecurityGroup, ResourceTag, Volume, VolumeAttachment PASS again; their fixture VPCs, instance and transit gateway deleted by the tests. VpnGateway FAIL: a gateway moved between VPCs takes minutes to detach on real AWS, longer than the test's minute (`Eventually` now waits five times longer on a live run). VPNConnection FAIL in the same run, beside VpnGateway's gateways still detaching. Swept by tag: 8. Left behind and deleted by hand: two VPCs whose gateway was still detaching |
| 2026-11-16 | `ackx-11160339-689d` | `25bc50c` (PR #111) | `$AWS_SANDBOX_ACCOUNT_ID` | SUITE=ack: TestApiDestination, TestConnection, TestSchedule, TestScheduleGroup | PASS | PASS | **EventBridge `ApiDestination`, `Connection` and Scheduler `Schedule`, `ScheduleGroup` pass on real AWS**, and the tests deleted all they made and retained: nothing left |
| 2026-11-16 | `ackx-11160339-2424` | `25bc50c` (PR #111) | `$AWS_SANDBOX_ACCOUNT_ID` | SUITE=ack: TestReplicationConfiguration, TestKeyExtension, TestPermission, TestWebACLAssociation | PASS | FAIL | **KMS Key extension passes on real AWS** (key swept, pending deletion 7 days). ReplicationConfiguration FAIL: the test replicates to its own region and account, which real ECR keeps no rule for; it needs a second region (the SCP allows us-east-2 only). Permission FAIL: Lambda could not yet assume the role IAM had just made (now retried). WebACLAssociation FAIL: the fixture ALB ARN is a made-up us-west-2 one; a live test needs a real load balancer. Left behind and deleted by hand: web ACL `edge`, role `app-fn` (the Lambda fixture is now deleted by the test) |
| 2026-11-16 | `ackx-11160402-b8de` | `cf0b4d2` (PR #111) | `$AWS_SANDBOX_ACCOUNT_ID` | SUITE=ack: the eight IAM tests | PASS | PASS | **All eight IAM kinds pass on real AWS in one run** (role from catalog #233), and the tests deleted their users, groups, roles, policy and 105 MFA devices: nothing left |
| 2026-11-16 | `ackx-11160402-b394` | `cf0b4d2` (PR #111) | `$AWS_SANDBOX_ACCOUNT_ID` | SUITE=ack: TestApplicationScheduledAction, TestPermission, TestBucketExtension, TestPublicAccessBlock | PASS | FAIL | **Application Auto Scaling `ScheduledAction` and S3 Control `PublicAccessBlock` pass on real AWS**; the ECS fixtures deleted by the test. Permission FAIL: `PublishVersion` refused while the new function was `Pending` (now retried). BucketExtension FAIL: `plain-bucket`, `marker-bucket` and `force-bucket` belong to other accounts (bucket names are unique across AWS; live runs now take the run ID). Left behind and deleted by hand: `kept-bucket` with its five versions (the test now deletes it) |
| 2026-11-16 | `ackx-11160402-fec2` | `cf0b4d2` (PR #111) | `$AWS_SANDBOX_ACCOUNT_ID` | SUITE=ack: TestVpnGateway, TestVPNConnection | PASS | FAIL | **EC2 `VPNGateway` passes on real AWS** (677 s: its moves between VPCs wait on real detaches). VPNConnection FAIL again: after VpnGateway, `office-hub` never synced in five minutes and `office-core` (through a transit gateway) neither; it passed alone in `ackx-11160252-142c`. Left behind and deleted by hand: the transit gateway (still attached when the test's clean-up ran) and one VPC |
| 2026-11-16 | `ackx-11160411-5872` | `fb675a1` (PR #111) | `$AWS_SANDBOX_ACCOUNT_ID` | SUITE=ack: TestPermission, TestBucketExtension | PASS | FAIL | Permission FAIL: real Lambda refused the test's principal (`InvalidParameterValueException: The provided principal was invalid`); the fixture needs a principal real AWS accepts. BucketExtension FAIL: ACK's `DeleteBucket` of `force-bucket-<run>` was refused 409 (not empty) for five minutes: the force-destroy emptying did not empty the versioned bucket on real S3. Needs a look before the extension is called proven. Left behind and deleted by hand: the three buckets of the run (one with five versions) |
| 2026-11-16 | `ackx-11160415-8b7c` | `fb675a1` (PR #111) | `$AWS_SANDBOX_ACCOUNT_ID` | SUITE=ack: TestOptionGroup | PASS | FAIL | Real RDS kept no options on `app-mysql` (`MARIADB_AUDIT_PLUGIN`, `MEMCACHED`) in 7.5 minutes; MEMCACHED needs settings and a security group on real RDS. The option group was deleted by the test |
| 2026-11-16 | `ackx-11160729-366f` | `e084e97` (HFL-623 follow-ups) | `$AWS_SANDBOX_ACCOUNT_ID` | SUITE=ack: TestBucketExtension, TestPermission, TestOptionGroup | FAIL | FAIL | **The S3 Bucket extension's force-destroy empties a versioned bucket on real S3** (five versions and delete markers, one multipart upload; ACK's `DeleteBucket` then succeeded). The cause of `ackx-11160411-5872` was ours: every `DeleteObjects` asked to bypass governance retention, which real S3 refuses with `InvalidArgument` on a bucket without Object Lock (moto and the AWS Emulator accept it); the extension now reads the bucket's Object Lock configuration and bypasses only under Object Lock. **Lambda `Permission` passes** (the alias granted to the sandbox account itself: real Lambda refuses an account principal that is no account). **RDS `OptionGroup` passes** (`oracle-se2` 19 with `STATSPACK` and `TIMEZONE_FILE_AUTOUPGRADE`, options that need no settings, port or security group). The new Object Lock subtest failed on moto and on AWS alike: S3 needs `Content-MD5` on a put with Object Lock parameters (fixed in the test). Sweeps clean; the tests deleted their buckets, function, role and option group |
| 2026-11-16 | `ackx-11160805-57c4` | `8fe266c` (HFL-623 follow-ups) | `$AWS_SANDBOX_ACCOUNT_ID` | SUITE=ack: TestBucketExtension | PASS | PASS | **The S3 Bucket extension passes on real AWS**, every subtest: force-destroy of a versioned bucket (no governance bypass asked), and of an Object Lock bucket holding an object under governance retention (the bypass asked, and the bucket deleted), the retain policy, the annotation removed, a Bucket ACK never made. Sweeps clean. Left behind and deleted by hand: the empty buckets `plain-bucket-*` and `changed-bucket-*` of this run and `ackx-11160729-366f` (the test now deletes them) |
| 2026-11-16 | `ackx-11160847-4089` | `8fe266c` (HFL-623 follow-ups) | `$AWS_SANDBOX_ACCOUNT_ID` | SUITE=ack: TestDefaultNetworkACL, TestDefaultRouteTable, TestDefaultVPC, TestVpnGateway, TestVPNConnection | PASS | PASS | **EC2 `DefaultNetworkACL`, `DefaultRouteTable` and `DefaultVPC` pass on real AWS** (role from catalog #238); the default VPC was replaced and left with EC2's settings and no tags. **EC2 `VPNConnection` passes on real AWS, after `VPNGateway` in the same run** (VPNGateway passes again, 723 s). It had never passed live: `ackx-11160252-142c`, recorded as a pass, failed as `ackx-11160326-b656` and `ackx-11160402-fec2` did. The cause was not the order. A real connection takes about six to ten minutes to become available (346 s here, 9.5 minutes in `fec2`), longer than the test's five; the tests now wait 20 minutes on a live run. Also, real EC2's `CreateVpnConnection` answers without the type, so the spec patch after the create was refused (`spec.type_: Required value`); the controller now keeps the type asked for. Real EC2 lists deleted connections for a while, so the settle check counts only live ones; a connection being deleted counts as deleted; the transit gateway's clean-up waits for its attachment to go. Sweeps deleted two VPN gateways and a customer gateway by tag. Left behind and deleted by hand: one VPC whose gateway was still detaching |
| 2026-11-16 | `ackx-11161114-80b7` | `27eeb4b` (HFL-623 follow-ups) | `$AWS_SANDBOX_ACCOUNT_ID` | SUITE=ack: TestOptionGroup | PASS | PASS | **RDS `OptionGroup` passes again with `STATSPACK` and `NATIVE_NETWORK_ENCRYPTION`**: the merge gate's AWS Emulator run refused `TIMEZONE_FILE_AUTOUPGRADE` (it publishes only `NATIVE_NETWORK_ENCRYPTION`, `OEM` and `STATSPACK` for `oracle-se2`), so the fixture moved to an option both publish. The option group was deleted by the test; sweeps clean |
| 2026-11-16 | `ackx-11161612-d265` | `5a65041` (PR #123) | `$AWS_SANDBOX_ACCOUNT_ID` | SUITE=ack: TestPermission | PASS | PASS | **`Permission.targetGroupRef` passes on real AWS**: the statement's `AWS:SourceArn` is the ARN read from a stand-in ACK `TargetGroup` (Lambda takes a target group ARN that does not exist; no load balancer is made), nothing added before the target group synced, the delete removes it. Nothing left behind |

## Kinds

One row per kind: its last live result, or why it has none. Updated with
each run; the ledger above has the runs.

| Route | Service | Kind | Test | Live |
| --- | --- | --- | --- | --- |
| Cloud Control | iam | SAMLProvider | TestSAMLProvider | PASS, `ackx-11151735-cca8` (2026-11-15); the manager lost its lease in two earlier runs (plan section 8 item 17) |
| Cloud Control | sso | PermissionSet | TestIdentityCenter | pending: no Identity Center instance in the sandbox account (the organization's is in its management account) |
| Cloud Control | sso | Assignment | TestIdentityCenter | pending: as PermissionSet |
| ACK | applicationautoscaling | ScheduledAction | TestApplicationScheduledAction | PASS, `ackx-11160402-b394` (2026-11-16) |
| ACK | autoscaling | ScalingPolicy | TestScalingPolicy | PASS, `ackx-11160326-bbdb` (2026-11-16) |
| ACK | autoscaling | ScheduledAction | TestScheduledAction | PASS, `ackx-11160326-bbdb` (2026-11-16) |
| ACK | ec2 | CustomerGateway | TestCustomerGateway | PASS, `ackx-11160326-b656` (2026-11-16) |
| ACK | ec2 | DefaultSecurityGroup | TestDefaultSecurityGroup | PASS, `ackx-11160326-b656` (2026-11-16) |
| ACK | ec2 | DefaultNetworkACL | TestDefaultNetworkACL | PASS, `ackx-11160847-4089` (2026-11-16) |
| ACK | ec2 | DefaultRouteTable | TestDefaultRouteTable | PASS, `ackx-11160847-4089` (2026-11-16) |
| ACK | ec2 | DefaultVPC | TestDefaultVPC | PASS, `ackx-11160847-4089` (2026-11-16); the test replaces the region's default VPC (deletes it, then `CreateDefaultVpc`) and afterwards leaves a default VPC with EC2's settings and none of its tags |
| ACK | ec2 | NetworkInterfacePermission | (none: moto has no API) | pending: no test yet (moto has none of the API, plan 6.33); a live test needs a second account to grant to, and the catalog instance updated and applied again |
| ACK | ec2 | LaunchTemplate | TestLaunchTemplate | pending: the live-test role lacks `ec2:CreateLaunchTemplate` and the other launch template actions until the catalog instance is updated from `cloudcontrol/config/live/role-policies.yaml` and applied again (fixed copy of ACK's, plan 6.43) |
| ACK | ec2 | ResourceTag | TestResourceTag | PASS, `ackx-11160326-b656` (2026-11-16) |
| ACK | ec2 | TransitGateway extension (ACK's TransitGateway) | TestTransitGatewayExtension | pending: not run live yet (built 2026-11-23); the live-test role needs `ec2:ModifyTransitGateway` and the transit gateway route table actions, so the catalog instance updated from `cloudcontrol/config/live/role-policies.yaml` and applied again first; a real gateway takes minutes to become available |
| ACK | ec2 | Volume | TestVolume | PASS, `ackx-11160326-b656` (2026-11-16) |
| ACK | ec2 | VolumeAttachment | TestVolumeAttachment | PASS, `ackx-11160326-b656` (2026-11-16) |
| ACK | ec2 | VPNConnection | TestVPNConnection | PASS, `ackx-11160847-4089` (2026-11-16), after TestVpnGateway; its three earlier runs failed (a real connection takes six to ten minutes to become available, and the create's answer carries no type), not because of the order |
| ACK | ec2 | VPNGateway | TestVpnGateway | PASS, `ackx-11160847-4089` (2026-11-16) |
| ACK | ec2 | VPNGatewayRoutePropagation | (none: moto has neither call) | pending: no e2e test yet (moto has neither call, plan 6.34); a live test needs one written against a VPC route table, and the catalog instance updated and applied again |
| ACK | ecr | ReplicationConfiguration | TestReplicationConfiguration | pending: the test replicates to its own region and account, which real ECR keeps no rule for (`ackx-11160339-2424`); a live run needs a second region, which the organization's service control policy denies (us-east-2 only) |
| ACK | eventbridge | APIDestination | TestApiDestination | PASS, `ackx-11160339-689d` (2026-11-16) |
| ACK | eventbridge | Connection | TestConnection | PASS, `ackx-11160339-689d` (2026-11-16) |
| ACK | iam | AccessKey | TestIAMAccessKey | PASS, `ackx-11160402-b8de` (2026-11-16) |
| ACK | iam | AccountAlias | TestAccountAlias | PASS, `ackx-11160402-b8de` (2026-11-16); live runs take aliases of the run ID (aliases are unique across AWS) |
| ACK | iam | AccountPasswordPolicy | TestAccountPasswordPolicy | PASS, `ackx-11160402-b8de` (2026-11-16) |
| ACK | iam | LoginProfile | TestLoginProfile | PASS, `ackx-11160402-b8de` (2026-11-16) |
| ACK | iam | RolePolicyAttachment | TestRolePolicyAttachment | PASS, `ackx-11160402-b8de` (2026-11-16) |
| ACK | iam | SSHPublicKey | TestSSHPublicKey | PASS, `ackx-11160402-b8de` (2026-11-16) |
| ACK | iam | UserGroupMembership | TestUserGroupMembership | PASS, `ackx-11160402-b8de` (2026-11-16) |
| ACK | iam | VirtualMFADevice | TestIAMVirtualMFADevice | PASS, `ackx-11160402-b8de` (2026-11-16) |
| ACK | kms | ReplicaKey | TestReplicaKey | pending: needs a second region, which the organization's service control policy denies (us-east-2 only) |
| ACK | kms | Key extension (ACK's Key) | TestKeyExtension | PASS, `ackx-11160339-2424` (2026-11-16) |
| ACK | lambda | Permission | TestPermission | PASS, `ackx-11160729-366f` (2026-11-16); `targetGroupRef` PASS, `ackx-11161612-d265` (2026-11-16); a live run grants the alias to the sandbox account itself (real Lambda refused a made-up account, `ackx-11160411-5872`) |
| ACK | organizations | AccountPlacement | TestOrganizationsAccountPlacement | pending: organization-level identity (plan 7.5); the sandbox account is a member, not the management account |
| ACK | organizations | DelegatedAdministrator | TestOrganizationsDelegatedAdministrator | pending: organization-level identity (plan 7.5); the sandbox account is a member, not the management account |
| ACK | organizations | Organization | TestOrganizationsOrganization | pending: organization-level identity (plan 7.5); the sandbox account is a member, not the management account |
| ACK | organizations | Policy | TestOrganizationsPolicy | pending: organization-level identity (plan 7.5); the sandbox account is a member, not the management account |
| ACK | organizations | PolicyAttachment | TestOrganizationsPolicyAttachment | pending: organization-level identity (plan 7.5); the sandbox account is a member, not the management account |
| ACK | rds | DBProxyTarget | TestDBProxyTarget | pending: not run live yet; its fixtures (a DB instance and cluster, a proxy) take many minutes and cost money, and the test does not delete them on a live run yet |
| ACK | rds | DBRoleAssociation | TestDBRoleAssociation | pending: not run live yet; its fixtures (a DB instance and cluster) take many minutes and cost money, and the test does not delete them on a live run yet |
| ACK | rds | OptionGroup | TestOptionGroup | PASS, `ackx-11161114-80b7` (2026-11-16), on `oracle-se2` 19's `STATSPACK` and `NATIVE_NETWORK_ENCRYPTION`, which need no setting, port or security group (MySQL's MEMCACHED needs a port and a security group, so real RDS refused the whole change in `ackx-11160415-8b7c`) |
| ACK | s3 | Bucket extension (ACK's Bucket) | TestBucketExtension | PASS, `ackx-11160805-57c4` (2026-11-16); its force-destroy had asked real S3 to bypass governance retention on buckets without Object Lock, which S3 refuses (`ackx-11160411-5872`), fixed |
| ACK | s3control | PublicAccessBlock | TestPublicAccessBlock | PASS, `ackx-11160402-b394` (2026-11-16) |
| ACK | scheduler | Schedule | TestSchedule | PASS, `ackx-11160339-689d` (2026-11-16) |
| ACK | scheduler | ScheduleGroup | TestScheduleGroup | PASS, `ackx-11160339-689d` (2026-11-16) |
| ACK | wafv2 | WebACLAssociation | TestWebACLAssociation | pending: the test associates made-up us-west-2 load balancer ARNs, which real WAF refuses (`ackx-11160339-2424`); a live test needs a real ALB fixture (a VPC, two subnets, a load balancer) made and deleted by the test |
