# Live runs: the Cloud Control kinds against a real AWS account

Every Cloud Control kind is **untested on real AWS** until it has a row in
the ledger below (`docs/cloudcontrol.md`, decision 1). This
file says how a live run is made, what keeps it apart from everything else,
and what each run found. The GCP repository's `docs/live-runs.md` is the
model.

**Status, 2026-11-15:** the sandbox account is the AWS test account
`hfl-test` (`$AWS_SANDBOX_ACCOUNT_ID`), created 2026-11-14 for tests only.
The stack of 2026-11-10 sat in the wrong account and the builder deleted
it (Open decisions O26c), which is why runs from 2026-11-12 were refused.
Earlier (2026-11-10): the sandbox account was the O5 account. The builder deployed the stack
`ack-extensions-live-tests` there (in us-east-2; IAM is global, the tests
run in us-west-2), trusting the offload runner service account
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
| The code that holds real credentials is compiled only with `-tags live` | `cloudcontrol/test/live`, `hack/live/sweep`, `cloudcontrol/test/e2e/live_test.go` |
| No task but `test-live`, `live-sweep` and `live-list` mentions `hack/live`, `-tags live` or the real target, and none calls them; `task check` compiles the live code with `go vet` and runs none of it | `hack/noaws` rules L1 and L2, in `task check` |
| The Taskfile sets `CLOUDCONTROL_E2E_TARGET=fake` for every task; built without `-tags live`, `real` fails | `Taskfile.yml`, `cloudcontrol/test/e2e/harness_test.go` |
| An explicit opt-in on every run: `ACK_EXT_LIVE=create-real-aws-resources` | `hack/live/run.sh`, `cloudcontrol/test/live` |
| The account must be `AWS_SANDBOX_ACCOUNT_ID` (from the environment; unset refuses the run), and STS must say the credentials are that account's | the same |
| Federated credentials only; stored keys, a profile or an endpoint override in the environment refuse the run | `hack/live/credentials.sh`, `cloudcontrol/test/live` |
| The manager never holds real credentials: it still runs under `pkg/fakeaws` (fake keys, dead proxy, a loopback endpoint), and a recorder in the test process signs its requests again with the run's credentials and forwards only Cloud Control and STS `GetCallerIdentity` | `cloudcontrol/test/live/recorder.go` |

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
`ack-extensions-live-tests` is now expressed as an instance of the
catalog's
[`google-federated-role`](https://github.com/highfivelabs/catalog/tree/main/charts/aws/google-federated-role)
chart: `charts/aws/google-federated-role/tests/ack-extensions-live-tests.values.yaml`
there. It renders the same role, trust (IAM's built-in
`accounts.google.com` principal, conditions on `sub` and `oaud`, no IAM
OIDC provider), session limit, tag and managed policy statements as the
template below at `7d67acc`, compared statement for statement on
2026-11-15 (the chart's README, "The ack-extensions live-test role"). It
differs in two ways: its managed policy is named
`ack-extensions-live-tests-part-1` (CloudFormation generated one), and the
budget alarm is not in it (no ACK controller or ack-extensions kind serves
AWS Budgets yet: a gap here).

The instance carries a placeholder unique ID (the catalog is public); the
real one is set where it is deployed, as the template takes it as a
parameter. Its statements are generated from the template below: when
`liveKinds` or `liveTestActions` change, regenerate the template
(`task cloudcontrol:generate`), then the instance's statements from it.

**Status, 2026-11-15:** not deployed. The role the CloudFormation stack
made in the test account works today and stays until the chart instance
is proven there (adopting the stack's role or replacing it is the
builder's call). Until then the template and the stack below are the
current deployment; once the instance is proven, the stack is deleted and
the template's role and policies go (the generator keeps producing the
statements the instance takes).

## The sandbox account's template (current deployment, until the chart is proven)

`cloudcontrol/config/live/sandbox-account.yaml`, generated from
`cloudcontrol/types.yaml` by `task cloudcontrol:generate` (so its
permissions follow the served types; `task check` fails if it is stale).
On the builder's request (O26c, 2026-11-15) an agent deploys it, in the
AWS test account only, with that account's admin role. It holds:

- no IAM OIDC provider. An IAM OIDC provider for `https://accounts.google.com`
  takes over every Google ID token in the account and refuses any whose
  audience is not on its client ID list: the first deploy in `hfl-test`
  (2026-11-15) broke the account's other Google-federated role
  (`hfl-test-admin`, audience `sts.amazonaws.com`) until the stack was
  deleted. The role trusts IAM's built-in `accounts.google.com` principal
  instead;
- `LiveTestRole` (`ack-extensions-live-tests`): `sts:AssumeRoleWithWebIdentity`
  from `accounts.google.com`, only when `accounts.google.com:sub` is the
  service account's unique ID (a parameter) and `accounts.google.com:oaud`
  (the token's `aud`) is the audience `ack-extensions-live-tests`, at most
  an hour; managed
  policies with the Cloud Control actions and handler permissions of the
  kinds sampled on live runs (`liveKinds` in `types.yaml`), their list
  handlers' and `cloudformation:ListResources` for the sweeper, and the
  tests' own fixtures (`liveTestActions`), split to IAM's 6,144-character
  limit and kept within IAM's default of 10 managed policies per role
  (the generator fails beyond it, and `TestLiveRoleWithinManagedPolicyLimit`
  checks the committed template). Every other kind is proven on the fakes
  only (plan question 21): a live run of a test whose kind is not in
  `liveKinds` is refused by IAM. Adding a kind to `liveKinds` needs the
  stack redeployed (and the catalog instance's statements regenerated);
- `BudgetAlarm`: a monthly cost budget (default USD 50) with e-mail alerts
  at 80% of actual and 100% of forecast spend.

How the existing stack was deployed, kept only to redeploy it until the
catalog instance replaces it (not for new setup), in the sandbox account
(profile `hfl-test`, the account's keyless admin role; never the
management or Identity Center account):

```sh
gcloud iam service-accounts describe <offload VM service account> --format='value(uniqueId)'
AWS_PROFILE=hfl-test aws cloudformation deploy --stack-name ack-extensions-live-tests --region us-east-2 \
  --template-file cloudcontrol/config/live/sandbox-account.yaml \
  --capabilities CAPABILITY_NAMED_IAM \
  --parameter-overrides GoogleServiceAccountUniqueId=<unique ID> AlertEmail=<address>
```

then set `AWS_SANDBOX_ACCOUNT_ID` to the account ID in the `.env` of
whoever starts live runs (never in the repository).

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
account with `CLOUDCONTROL_E2E_TARGET=real`. Every resource the manager
makes carries the tag `hfl.dev/live-run=<run ID>` (ACK's `--resource-tags`,
filtered from specs like ACK's own tags). Before the run, leftovers of runs
older than 2 hours are swept; after it, pass or fail, everything of the run
is swept and what is left is listed. Output:
`offload-artifacts/live/<run ID>/` (`summary.md`, `fake.log`, `live.log`,
`left.txt`).

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
