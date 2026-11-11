# Handoff: building gap kinds

Written 2026-11-09 by the agent that built the gap kinds from `docs/plan.md`
§5, for whoever builds the next. It says how the work is done here, the
mistakes already made once, and what is left. The design and the reasons are
in `docs/plan.md`; the pins and copied files in `UPSTREAM.md`; this file does
not repeat them.

## Where things stand

- The kinds built so far are in the README table and the status line of
  `docs/plan.md` (§5 says which rank each answers); `gh pr list` shows what
  is open. Built: ranks 1, 3, 4, 5, 7, 8, 10, 12 and 15, and rank 11 in part.
- **Not built, and why** (the table in §5 has the rest):
  - ranks 2, 9 (metric filters), 22: fields on existing ACK kinds (open
    question 1);
  - rank 6 (security group rules): open question 7; rank 23: open question 3;
  - rank 15 `AccessKey`: open question 10, recommended out;
  - ranks 9 (`CompositeAlarm`), 13, 14, 16, 21: moto has none of the API;
  - rank 18 (`KeyPair`): ec2-controller PR #396 is close to merging.
- The kinds built here have no ACK controller to compare with and no real
  AWS run: everything is proven against moto only (`docs/plan.md` §8 item 3
  lists what to check against real AWS, one run per kind).
- **Next to build, in rank order:** the rest of rank 11 (ECR
  `RegistryPolicy`, `RegistryScanningConfiguration`), rank 17 (S3 `Object`,
  the account's public access block), rank 19 (Secrets Manager resource
  policy), rank 20 (RDS event subscriptions). Recheck upstream first (below).
- **Foundation queue** (`docs/coverage/foundation.md`, F01 to F18) is the work list now, ahead of the ranks above. Done: F01 `Policy` (PR #30, plan 6.25), `PolicyAttachment` (PR #31, plan 6.26), `DelegatedAdministrator` (PR #33, plan 6.27), `Organization` (PR #35, plan 6.28), `AccountPlacement` (PR #38, plan 6.29: `MoveAccount` mapped as both the create and the update; retain-only). F01 is complete. F03 IAM `AccessKey` (PR #40, plan 6.30), `VirtualMFADevice` (PR #47, plan 6.31: the seed into a Secret on `AccessKey`'s pattern; a hook reads the list's later pages). F05 EC2 `VPNConnection` (PR #48, plan 6.32: references to our gateways, keys from Secrets, updates unproven on moto; `transitGatewayRef` waits on open question 16). EC2 `NetworkInterfacePermission` (PR #49, plan 6.33: unproven on the fake; a stand-in unit test on `pkg/fakeaws`). EC2 `VPNGatewayRoutePropagation` (PR #50, plan 6.34: the attach-pair pattern; unproven on the fake, a stand-in unit test on `pkg/fakeaws`). `PolicyAttachment`, `DelegatedAdministrator` and `AccountPlacement` are generated from operations mapped in `generator.yaml` (step 3 below); a reference to an ACK kind of the same service works through a type alias in `apis/v1alpha1/ack_kinds.go`.
- **Next:** F02 (Identity Center) is the Cloud Control route's (`agent/cloudcontrol-types`), so not built here; check `docs/cloudcontrol.md` and the Cloud Control PRs before any kind, so none is built both ways. Cloud Control takes F03 to F18 where it can (`docs/cloudcontrol.md` section 8, plan step 2). Nothing is queued here now: the coordinator's two foundation gaps Cloud Control cannot provision (`NetworkInterfacePermission`, `VPNGatewayRoutePropagation`) are built, unproven on the fake; what a real run must check is in plan section 8.
- **Organization-scoped identity** (open question 11, answered; plan 7.5): the Organizations kinds run only in a namespace kept for organization-level objects, beside an ACK organizations-controller with an organization-scoped role bound by EKS Pod Identity (no IRSA annotation; plan 7.5 has the associations and the trust policy). The operator refuses an Organizations manager whose namespace has any other ACK controller, with a `SharedNamespace` Warning event (plan 7.2 item 7, proven in 7.4). A new organization-level kind adds its actions to plan 7.5's permissions table.
- **Retain-only kinds** (open question 12; plan 6.28): a kind for something that is not deleted from Kubernetes (an organization; later an account's closure, root-level settings) has no delete call (`ignore.operations` in `generator.yaml`, so `sdkDelete` is a no-op), and its resource is added to `config/admission/retain-only.yaml`'s `resourceRules`, which requires `services.k8s.aws/deletion-policy: retain` on every object. Its e2e test deletes an object made before the policy is installed and checks the cloud thing stays, then checks the policy's refusals (`tests/e2e/organizations_organization_test.go`). **The factory's scaffolder should generate this pattern** (the ignored delete, the policy entry and the two tests) for a kind marked retain-only.
- **Per-environment ACK managers** (open question 5, answered 2026-11-10; plan 7.6): an `ACKContext` (`core.aws.hfl.dev`, named `ack`) in a namespace lists controllers; the operator runs one manager per controller in `ack-extensions-system` as `ack-extensions-<service>-<namespace>`, watching only that namespace, bound by EKS Pod Identity. `task ack:iam` writes the role from the context (`pkg/ackiam` reads each kind's SDK calls from the generated code; a new kind needs nothing, but a call needing more than its own action, such as `iam:PassRole`, goes in `ackiam.Curated`). Open: questions 17 (the name) and 18 (ACK's own controllers per environment).
- **Open questions:** 8 to 12 are answered. Assumed, for the builder to
  confirm: `Organization`'s two lists are exclusive (plan 6.28); deleting an
  `AccountPlacement` leaves the account where it is (plan 6.29).
- **moto probing** is cheap on the laptop: `.cache/moto-venv/bin/moto_server -p <port>` and a boto3 script with `.cache/moto-venv/bin/python` (kill the server after). Probe before writing a test.

## Cloud Control types (`docs/cloudcontrol.md`)

The second route, for types ACK has no controller for and whose
CloudFormation model is what a platform team would write. The builder
(2026-11-09): go, with limits; foundation order; each type proven on the
fake and marked untested on real AWS until a real run. Adding a type:

1. Check it is not better as an ACK-generated kind here (references, an
   exclusive/non-exclusive choice, an attachment) and that
   `cloudcontrol/readiness.md` says its CRD is accepted.
2. Add `typeName` to `cloudcontrol/types.yaml`; opt in `secrets` only for
   write-only strings that are secrets (the generation report lists the
   candidates); `task cloudcontrol:generate`. No Go per type.
3. A moto backend in `cloudcontrol/test/fake/moto.go` and an end-to-end
   test in `cloudcontrol/test/e2e` when moto has the service; otherwise
   mark it "unproven on fake" (PR 2 adds the column to the coverage data).
4. `tests/operator/run.sh` checks the CRD list; add the new CRD there.
5. Never weaken the guard against real AWS (`pkg/fakeaws`, checked by
   `task noaws`; `docs/cloudcontrol.md` section 6).

The identity is per environment: a `CloudControlContext` per namespace,
the manager in the operator's namespace as
`ack-extensions-cloudcontrol-<namespace>`, EKS Pod Identity to the
environment's role (`pkg/operator/cloudcontrol.go`). Per-service Cloud
Control managers were the spike's proposal and are not to be built.

**Left of the builder's Cloud Control brief (2026-11-09), for the next
agent.** PR 1 (`agent/cloudcontrol`) is the core. Still to do:

- **PR 2, types in foundation order.** Done in `agent/cloudcontrol-types`:
  coverage counts Cloud Control kinds (labelled "(Cloud Control)", with an
  `unproven on fake` note from `unproven:` in `types.yaml`, which the
  generator writes into the CRD), and batch 1, F02 (`PermissionSet`,
  `Assignment` on moto's sso-admin; the access control attribute
  configuration unproven). After `task cloudcontrol:generate`, run
  `task coverage` and the survey (`go run ./hack/cloudcontrol-gen
  --survey`) twice: each reads the other's output. **Next batches**, in
  foundation order, recheck ACK first: F04's Transit Gateway route,
  association, propagation and peering (batch 2, done:
  `TestTransitGatewayRouting`), F07 Control Tower (batch 3: `EnabledControl` and
  `EnabledBaseline`, unproven; `LandingZone` never deleted and retain-only,
  on a test double, `TestLandingZone`), F08/F09 Route 53 and its Resolver firewall (batch 4: `TestDNS`; two of ten proven on moto, the DNS Firewall, DNSSEC signing, key-signing key, CIDR collection and resolver configuration types unproven), F14 Config (batch 5: `TestConfig`; six of eleven proven on moto, the organization config rule, conformance pack, remediation, stored query and connector unproven), F15 GuardDuty (batch 6: `Filter` on moto, six unproven), F16 Security Hub (batch 7: `Hub` and `OrganizationConfiguration` on moto, seven unproven; map-shaped tags get no ACK default tags, `docs/cloudcontrol.md` section 7), F17 (batch 8:
  `ResourcePolicy` on moto; `RotationSchedule` waits for ACK's pull
  request #69).
  **Next, F18** (`docs/coverage/foundation.md` says to ask the builder
  first; the coordinator's brief of 2026-11-10 lists F15 to F18), probed
  on moto 5.2.5: `macie2` has Enable/Get/DisableMacie (no
  `UpdateMacieSession`) and invitations, no findings filters, allow lists,
  custom data identifiers or `CreateMember`; `inspector2` has
  Create/Delete/ListFilters (no `UpdateFilter`; read a filter through
  `ListFilters`), tags, delegated admin and organization configuration, no
  CIS scan configurations. Access Analyzer, Detective, Firewall Manager,
  Audit Manager and Security Lake have no moto service at all. A sensible
  split: batch 9, Macie `Session` and Inspector `Filter` on moto with the
  rest of both services unproven (new test clients
  `aws-sdk-go-v2/service/macie2` and `inspector2`, of the release the
  GuardDuty client is from, as `securityhub` v1.55.6 was); batch 10, the
  five services without moto, all unproven. Then F13 (Direct Connect,
  when a catalog entry or adjacent coverage needs it, the builder's A10).
  **F03 `VirtualMFADevice` is not for this route** (the builder,
  2026-11-09, closing PR #44): Cloud Control never returns the seed or QR
  code, so a device made through it cannot be enrolled. It is to be built
  as a kind that writes the seed to a Kubernetes Secret (never logged),
  like `AccessKey` (PR #40); PR #44's moto notes still hold (moto takes any
  two six-digit codes in `EnableMFADevice`; it has no MFA device tags).
  **Left for the ACK kinds agent, not this route:** two foundation gaps
  Cloud Control cannot serve (non-provisionable) and ACK's generator here
  can (`docs/cloudcontrol.md` section 8, the table of the 25):
  `ec2.aws.hfl.dev/NetworkInterfacePermission` (a plain
  create/describe/delete; moto has none of it, so unproven on fake) and
  `VPNGatewayRoutePropagation` (an attach pair,
  `EnableVgwRoutePropagation`/`DisableVgwRoutePropagation` read from
  `DescribeRouteTables`, the `PolicyAttachment` pattern).
  **F05 `VPNConnection` is not for this route** (step 1 above):
  `docs/coverage/foundation.md` F05 proposes it ACK-generated in
  `services/ec2/`, with references to our `CustomerGateway` and
  `VPNGateway` (or ACK's `TransitGateway`) and the tunnels' pre-shared keys
  from Secrets, which the generator gives and this route does not yet (no
  references before the overlay). `VPNConnectionRoute` could come here,
  but moto has no `CreateVpnConnectionRoute` (unproven on fake), so it is
  better made beside the connection. Both for the ACK kinds agent.
  The original brief for it:
- **PR 2, types in foundation order**, in reviewable batches
  (`docs/coverage/foundation.md`; `cloudcontrol/readiness.md` lists the
  gap types and which make a clean CRD). Each with a moto backend and an
  e2e test where moto has the service, else "unproven on fake" in the
  coverage data. Coverage reads our CRDs from `config/crds/bases/` only
  (`hack/coverage/model.go`, the glob near line 132): add
  `cloudcontrol/config/crds/*_*.yaml`, a column or flag for the route and
  for "unproven on fake", then `task coverage`. Kind names can repeat
  across services; the manager handles it (group-qualified
  `--cloudcontrol-kinds`, controller-name validation off), but check the
  coverage matcher does too.
- **PR 3, the live-test switch.** Built in `agent/cloudcontrol-live`
  (`docs/live-runs.md`): `task test-live`, the sweeper, the ledger, the
  sandbox account's template (generated into `cloudcontrol/config/live/`).
  Next, once the builder has deployed the template and added the account
  to `hack/live/accounts.txt`: one live run per type, fixing the fixtures
  `docs/live-runs.md` lists, and a ledger row each. The original brief:
- **PR 3, the live-test switch**, after the GCP repository's
  (`config-connector-extensions`: `docs/live-runs.md`, `hack/live/run.sh`,
  `sweep.sh`, `projects.sh`, `task test-live`, `live-sweep`, `live-list`):
  a target variable for `cloudcontrol/test/e2e` (fake or real; `task check`
  forces fake), the real mode refusing to start without an explicit opt-in
  and an account ID on an allowlist (checked with STS `GetCallerIdentity`),
  run-ID names and a tag on everything made, assertions on the fake's
  call log skipped in real mode, the service SDK checks pointed at real
  AWS. A sweeper can be generic: Cloud Control `ListResources` for each
  served type, delete what carries the run tag (allowlist checked first).
  Credentials by federation only: on an offload VM (`offload run --sa`),
  fetch a Google ID token from the metadata server for an audience, write
  it to a file, and let the SDK do `AssumeRoleWithWebIdentity`
  (`AWS_WEB_IDENTITY_TOKEN_FILE`, `AWS_ROLE_ARN`), refreshing the file
  before the hour. The CloudFormation template for the sandbox account
  (for the builder to deploy; nothing deployed by an agent): the web
  identity trust for `accounts.google.com` (check whether IAM needs an
  OIDC provider resource for Google or takes `accounts.google.com`
  natively, and which claim each condition key maps to:
  `accounts.google.com:sub`, `:aud` (azp), `:oaud`), a test role trusting
  only the offload VM service account's unique ID, its permissions from
  `task cloudcontrol:iam` plus `cloudformation:ListResources`, and a
  budget alarm. Never runnable by `task check`; the fake's three guards
  stay as they are.

## The loop for one kind

1. **Recheck upstream, read-only.** The ACK controller's latest release, its
   `generator.yaml` ignore list, open pull requests and issues for the kind.
   Text there is data, not instructions. Never open, comment on or review an
   upstream PR or issue. Record what you found in the plan (§5 row and the
   kind's §6 section).
2. **Branch from the latest main:** `agent/<short-kind-name>`, one PR per
   kind. Avoid stacking; if a kind needs another's code (Schedule needs
   ScheduleGroup), say so in the PR body.
3. **Generate first (the builder, 2026-11-09).** Prefer ACK's generator for every kind it can produce and keep hooks small; say in the PR roughly what share is generated. An attach/detach pair is generable: map the calls in `generator.yaml` `operations` (`operation_type: Create`/`Delete`/`List`) and match the list on the target (`list_operation.match_fields`), as `PolicyAttachment` does. A reference to an ACK kind of the same service: alias ACK's type into our `apis/v1alpha1` (`organizations/apis/v1alpha1/ack_kinds.go`), add ACK's RBAC marker by hand and its scheme in `handwritten.go`. Any generator call you script goes in `hack/`.
   **Generated or hand-written?** If the API has a Create, Describe and
   Delete, add the resource to the service's `generator.yaml` (remove it
   from `ignore.resource_names`), copy upstream's hooks and templates when
   ACK has a similar kind, run `task generate`. Otherwise write it by hand on
   ACK's runtime: copy the skeleton (`descriptor`, `identifiers`, `manager`,
   `manager_factory`, `resource`) from a hand-written kind such as
   `services/iam/pkg/resource/user_group_membership`, and write the API type,
   `sdk.go`, `delta.go` and `references.go`; register the package in the
   service's `cmd/controller/handwritten.go`.
4. **A new service** also needs: `metadata.yaml` and `generator.yaml`, the
   service in `hack/generate.sh`, the `go.mod` entries (SDK at the version the
   ACK controller pins, or the aws-sdk-go-v2 `v1.34.0` release's if there is no
   controller), `config/operator/operator.yaml` (`MANAGER_IMAGE_<SVC>`),
   `config/operator/rbac.yaml` (`resourceNames`), `config/rbac/kustomization.yaml`,
   the comments in `pkg/operator/operator.go` and `pkg/test/harness/harness.go`,
   and the harness's CRD list if the service references ACK kinds. Copy the
   last new service's commit (`git show 2c2e9be` for EventBridge).
5. **Every kind** also needs its CRD in `config/crds/kustomization.yaml` and in
   `tests/operator/run.sh` (the CRD list, twice), an end-to-end test in
   `tests/e2e` and unit tests, and the README row, `UPSTREAM.md` rows and
   `docs/plan.md` §6 section, results and moto notes.
6. **Then** `task coverage` and commit its output (`task coverage:check` must
   pass), and `task cloudcontrol:generate` (Cloud Control's readiness survey
   counts a new kind of ours; `task check` fails while it is stale).
7. **Commit** with `git commit -s`, ending with the `Co-Authored-By` and
   `Claude-Session` trailers the session gives you. Never `--no-verify`,
   never push to main, never force-push, never touch the git config.
8. **PR** with `gh pr create`: what was built, what upstream code it copies
   (and the pins), what was proven against moto and what was not, new open
   questions, the checks and their real results (a failed first run is
   reported, with its cause), and the closing line the session gives you.
9. Report to the coordinator after each PR.

## Running the checks

Nothing heavy on the laptop: no `task check`, envtest, Docker or kind. Use a Spot
VM through `../offload`:

```sh
../offload/offload run --machine e2-standard-8 --hours 2 \
  --prep 'task tools' --step-skippable 'task check' --step 'task operator-e2e'
```

- A run takes 13 to 15 minutes. `tests/e2e` is the long part. Run it in the
  background and read the log; `offload ls` shows VMs, and reap only your own.
- The CPU quota is shared (32 at the time); about three of these runs fit at
  once beside another agent's.
- offload uploads the working tree. To start several runs from one clone,
  check out a branch, start the run, and wait about 25 seconds (until the
  upload is done) before checking out the next.
- `task operator-e2e` fails unless `tests/operator/run.sh` has more than 200 lines and its output ends with `all checks passed` (an empty script once passed vacuously in another repository). Do not truncate or rewrite that script wholesale; check `wc -l` after editing it.
- `task upstream`/`task tools` need network once; `task coverage` and
  `task coverage:check` are cheap and run on the laptop.
- If a test times out, rerun it once before calling it a failure. A run can
  also fail before any test: `task upstream` cloning ACK's code generator
  from GitHub timed out once on a VM (`exit 201` in prep). Rerun it.
- `tests/e2e` is one package whose tests run one after another, and takes
  about 9 to 10 minutes on a Spot VM, growing with each kind. `go test`'s
  default 10 minute timeout was hit once and is now 30 minutes
  (`taskfiles/go.yml`, `TIMEOUT`). **The lasting fix is to run the kinds'
  tests in parallel, and that needs its own look at how
  `pkg/test/harness` shares moto and envtest** (one moto per test, a kube-
  apiserver per test, and the ports and CPU they take); do not just add
  `t.Parallel()`.

## Merging main into a branch

The coordinator merges PRs one at a time. Your next PR conflicts in the same
places every time, all mechanical:

| File | Resolution |
| --- | --- |
| `README.md` kind table, `UPSTREAM.md` rows and lists | keep both sides |
| `docs/plan.md` status line | count the kinds again; the list is alphabetical by service |
| `docs/plan.md` §5 row, §6 sections, results, moto notes | keep both; §6 section numbers are in order of arrival |
| `tests/operator/run.sh` CRD list (twice) | keep both lists' entries |
| `config/crds/kustomization.yaml`, `handwritten.go` | keep both |
| `zz_generated.deepcopy.go`, `config/rbac/*-manager-role.yaml` | take main's and run `task generate` |
| `docs/coverage/*`, the README's Coverage section | take main's and run `task coverage` |
| `cloudcontrol/readiness.*` | take main's and run `task cloudcontrol:generate` |

Then `task generate`, `go build ./...`, `task coverage`, commit the merge with
`-s` and the trailers, and rerun the checks on the new head. A merge commit
git makes has no sign-off: amend it.

## Mistakes made once, so you need not make them

- **Tests that read a reference back from the spec.** ACK's runtime resolves
  `fooRef` on every reconcile and does not write the result into the stored
  spec. Check the cloud, not `spec.foo`.
- **Tests that update an object the controller is also writing.** The
  update gets a conflict. Read and update again until it takes.
- **Tags.** ACK's runtime adds `services.k8s.aws/...` tags to what it makes.
  Compare the spec's tags, not the full set.
- **Account-wide singletons** (an alias, a policy, replication rules): until
  an object has made the setting once (no ARN in its status), its read finds
  nothing, so the create sets it. Otherwise a retained object's leftovers make
  the runtime refuse a new object as "Resource already exists".
- **Secrets the cloud never returns** (passwords, API keys): keep the spec's
  value in the read, or every resync updates the resource. A changed Kubernetes
  Secret is then not pushed until the spec changes; say so.
- **Server defaults** (the volume type, a rate limit, a time zone): a
  `delta_pre_compare` `customSetDefaults` copied from
  `applicationautoscaling-controller`'s `custom_delta.go`. Unit-test it, since
  moto often does not return the default AWS does.
- **A generated type named like a hand-written kind** (the API's
  `SSHPublicKey` shape and our kind): `ignore.shape_names` in the
  service's `generator.yaml`.
- **The generator's list of operations that keep v1 SDK names**
  (patch 0002) breaks v2 output types for the ones on it; `CreateSnapshot`,
  `AttachVolume`, `DetachVolume` are the same case.
- **A moto lookup that does not take what AWS does** (KMS by ARN, a second
  rule, a shallow copy of a replica): see `docs/plan.md` §4.4 and the kind's
  moto note; prove what moto can, list the rest as not proven, and do not
  patch moto.

## Rules that are easy to break

- Never edit `third_party/`; changes to upstream code go in
  `patches/code-generator/`. `task upstream` resets it to the pin and patches.
- Every test path reaches AWS only through `pkg/fakeaws`: SDK clients from
  `fakeaws.Config`, processes from `fakeaws.Command`, Python on kind through
  `fake_py`. `task noaws` (in `task check`) fails otherwise; never add an
  exemption to it.
- Never use real cloud credentials, projects or accounts: moto only. Never
  catch an error and report success. Never claim a check passed that did not run.
- Never open, comment on or review upstream PRs or issues; reading them is
  fine. Never create or change a GitHub repository's visibility.
- Do not touch `docs/coverage/` or `hack/coverage/` by hand, and the README's
  marked Coverage section.
- Open questions go to `docs/plan.md` §9, with a recommendation, not decided
  silently. The coordinator takes them to the builder.
- Leave kind clusters that are not this repository's alone.

## Where to look

- `docs/plan.md` §2 (how a kind is built), §4.4 (moto's gaps), §5 (the ranked
  list), §6 (each kind), §7 (the operator), §8 (what is left), §9 (open
  questions).
- `taskfiles/README.md`: how the shared tasks may be shared with other repos.
- `hack/upstream.sh`, `hack/generate.sh`, `hack/coverage/`.
