# Live runs: the Cloud Control kinds against a real AWS account

Every Cloud Control kind is **untested on real AWS** until it has a row in
the ledger below (`docs/cloudcontrol.md`, the builder's decision 1). This
file says how a live run is made, what keeps it apart from everything else,
and what each run found. The GCP repository's `docs/live-runs.md` is the
model.

**Status, 2026-11-09: no live run yet.** The sandbox account does not exist;
`hack/live/accounts.txt` is empty, so every live run refuses to start.

## What keeps it apart

Only one path in the repository may reach real AWS, and `task check`
cannot take it:

| Lock | Where |
| --- | --- |
| The code that holds real credentials is compiled only with `-tags live` | `cloudcontrol/test/live`, `hack/live/sweep`, `cloudcontrol/test/e2e/live_test.go` |
| No task but `test-live`, `live-sweep` and `live-list` mentions `hack/live`, `-tags live` or the real target, and none calls them; `task check` compiles the live code with `go vet` and runs none of it | `hack/noaws` rules L1 and L2, in `task check` |
| The Taskfile sets `CLOUDCONTROL_E2E_TARGET=fake` for every task; built without `-tags live`, `real` fails | `Taskfile.yml`, `cloudcontrol/test/e2e/harness_test.go` |
| An explicit opt-in on every run: `ACK_EXT_LIVE=create-real-aws-resources` | `hack/live/run.sh`, `cloudcontrol/test/live` |
| The account must be in `hack/live/accounts.txt`, a reviewed change, and STS must say the credentials are that account's | the same |
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

## The sandbox account's template (the builder deploys it)

`cloudcontrol/config/live/sandbox-account.yaml`, generated from
`cloudcontrol/types.yaml` by `task cloudcontrol:generate` (so its
permissions follow the served types; `task check` fails if it is stale).
Nothing is deployed by an agent. It holds:

- `GoogleOIDCProvider`: an IAM OIDC provider for `https://accounts.google.com`,
  client IDs the audience and the service account's unique ID;
- `LiveTestRole` (`ack-extensions-live-tests`): `sts:AssumeRoleWithWebIdentity`
  through that provider, only when `accounts.google.com:sub` is the
  service account's unique ID (a parameter), at most an hour; managed
  policies with every served type's Cloud Control actions and handler
  permissions, the list handlers' and `cloudformation:ListResources` for
  the sweeper, and the tests' own fixtures (`liveTestActions` in
  `types.yaml`), split to IAM's 6,144-character limit;
- `BudgetAlarm`: a monthly cost budget (default USD 50) with e-mail alerts
  at 80% of actual and 100% of forecast spend.

To deploy, in the sandbox account:

```sh
gcloud iam service-accounts describe <offload VM service account> --format='value(uniqueId)'
aws cloudformation deploy --stack-name ack-extensions-live-tests \
  --template-file cloudcontrol/config/live/sandbox-account.yaml \
  --capabilities CAPABILITY_NAMED_IAM \
  --parameter-overrides GoogleServiceAccountUniqueId=<unique ID> AlertEmail=<address>
```

then add the account ID to `hack/live/accounts.txt` in a pull request.

**To confirm on the first deploy** (not checkable without an account):

- IAM also accepts `accounts.google.com` as a built-in federated principal
  without an OIDC provider resource; the template makes the provider, as
  the builder asked. Which token claim IAM compares with the provider's
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
../offload/offload run --sa <service account> --prep 'task tools' -- \
  env ACK_EXT_LIVE=create-real-aws-resources \
  task test-live ACCOUNT=<id> ROLE_ARN=arn:aws:iam::<id>:role/ack-extensions-live-tests TESTS=TestDetector
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
federated credentials) lists every served type through Cloud Control
`ListResources` and deletes what carries the run tag. Types that take no
tags (`Assignment`, `InstanceAccessControlAttributeConfiguration`) are
deleted by the tests that made them; a run cut off before then leaves them,
and `left.txt` cannot show them.

**What the first runs will need** (the tests were written for moto):
`TestSAMLProvider`'s metadata and private key are placeholders that real
IAM will refuse; `TestDetector` fails in an account that already has a
detector (one per account and region); `TestIdentityCenter` needs Identity
Center enabled in the account; `TestTransitGatewayRouteTable` makes a
transit gateway (billed hourly) and deletes it at the end, but a run cut
off before then leaves it, untagged. `TestVirtualMFADevice` makes an IAM
user, `ack-ext-e2e-mfa`, and deletes it at the end; a run cut off before
then leaves it (untagged, so the sweeper cannot see it) for the next run
to reuse. Each is a fixture change, made with the first run of that test.

## Ledger

| Date | Run | Commit | Account | Tests | Fake | Live | Found |
| --- | --- | --- | --- | --- | --- | --- | --- |
| | | | | | | | none yet: no sandbox account |
