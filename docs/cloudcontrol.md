# Cloud Control: one generic controller for the AWS long tail

Status, 2026-11-09: **go, with limits** (the builder). The spike of PR #34
is productionized on branch `agent/cloudcontrol`: one generic reconciler of
our own over the AWS Cloud Control API, with CRDs generated from the
CloudFormation registry schemas, as Terraform's `awscc` and Pulumi's
`aws-native` do, run per environment (a namespace) as that environment's
own AWS identity. Everything is proven on a fake: no AWS account has been
used, and every kind is **untested on real AWS** until it has a real run
(the live-test switch and the sandbox account's template are a separate
pull request; the account itself is the builder's).

**In one paragraph.** One generic resource manager (about 1,200 lines)
implements ACK's runtime interfaces over unstructured objects, so ACK's own
runtime drives it: the same finalizer, deletion policy, adoption
annotations, conditions (`ACK.ResourceSynced`, `Ready`, `ACK.Terminal`,
`ACK.Recoverable`, `ACK.Adopted`) and flags as every other kind here. A
generator turns a type's registry schema into a CRD the API server
accepts, an embedded schema, and a least-privilege IAM policy. A
`CloudControlContext` in a namespace (Config Connector's
`ConfigConnectorContext`, for AWS) makes the operator run a manager for
that namespace, watching only it, as a ServiceAccount of its own bound by
EKS Pod Identity to the environment's IAM role. Three foundation types ACK
lacks (`AWS::IAM::SAMLProvider`, `AWS::EC2::TransitGatewayRouteTable`,
`AWS::GuardDuty::Detector`) run end to end on envtest against a thin Cloud
Control fake over moto, with **no per-type Go**. Run on every type in the
pinned schema zip, the generator makes an accepted CRD for **all 1,766
provisionable types**, including **251 of the 276 foundation-gap types**.

## The builder's decisions (2026-11-09)

1. **Go, with limits.** "Productionize the generic manager and roll types
   out in foundation order; each type proven on the fake and marked
   untested on real AWS until it has a real run. Keep ACK-generated kinds
   where ACK's generator gives better behaviour; Upjet remains the
   fallback for types this route can't handle."
2. **API group:** `<service>.cloudcontrol.aws.hfl.dev`, separate from our
   ACK-generated `<service>.aws.hfl.dev`, matching a separate Bundt
   Provider named `hfl.aws-cloudcontrol`.
3. **Identity**, in the builder's words: "I think it's closer to the one
   shared cloud control manager, but not exactly. I think the scoping
   should be at the environment level, so each environment gets an
   identity attached, and not through Workload Identity but instead
   through Pod Identity. Important distinction there, Pod Identity is
   newer. I don't think a per-service manager is the correct thing.
   Loosely, this follows the KCC model of a GSA and KSA workload identity
   bound that is enabled within a namespace to run KCC, and really that
   namespace has a scope that it works within." Built as section 10 says;
   the spike's per-service managers are gone.
4. **Write-only fields:** "Secret references only for fields marked as
   secrets, opt-in per field (many write-only fields aren't secrets)."
   Built: `secrets` per type in `cloudcontrol/types.yaml` (section 2).
5. **A sandbox AWS account is coming** for one real run per type; the
   builder will create it. To prepare for it: a live-test switch like the
   GCP repository's (`task test-live`, a sweeper, a ledger), never run by
   `task check` and refusing to run without explicit opt-in and an
   allowlisted account ID; credentials by federation from the offload
   VMs' Google service account (`AssumeRoleWithWebIdentity` with a Google
   ID token), never stored keys; and the CloudFormation template the
   builder deploys in the account. Nothing deployed by an agent.

## Contents

1. What was built
2. The mapping: schema to CRD
3. The reconciler
4. Generated share
5. IAM from the schemas
6. The fake, and the guard against real AWS
7. What was proven
8. Readiness: how much of the gap Cloud Control covers
9. Compared with our ACK-generated `organizations/Policy`
10. Identity and placement: one manager per environment
11. What did not work, or is not known
12. Decisions taken, and the plan

## 1. What was built

| Path | What | Written or generated |
| --- | --- | --- |
| `cloudcontrol/types.yaml` | the types to serve: one line each (`typeName`), and the write-only properties that are secrets (`secrets`) | written, the only per-type input |
| `cloudcontrol/apis/v1alpha1` | `CloudControlContext`, which enables Cloud Control in a namespace | written; deepcopy and CRD (`cloudcontrol/config/context/`) by controller-gen |
| `pkg/operator/cloudcontrol.go` | the operator's part: a manager per context | written |
| `hack/cloudcontrol-gen/` | the generator; `--survey` runs it on every type in the zip | written once |
| `cloudcontrol/pkg/cfn/` | reads registry schemas: behaviour lists, pointers, names, conversion, comparison | written once |
| `cloudcontrol/pkg/crdgen/`, `crdcheck/` | schema to CRD; validation as the API server does it (structural schema, CEL compile and cost) | written once |
| `cloudcontrol/pkg/iamgen/` | handler permissions to an IAM policy; an environment's role (merged, split to IAM's size limit) and its Pod Identity trust policy | written once |
| `cloudcontrol/pkg/resource/` | the generic resource manager on ACK's runtime | written once |
| `cloudcontrol/cmd/manager/` | the manager binary (ACK's generated `main.go`, shape for shape) | written once |
| `cloudcontrol/config/crds/`, `config/iam/`, `config/rbac/`, `pkg/registry/zz_generated.registry.go`, `pkg/registry/schemas/` | per type: CRD, IAM policy, the registry entry, the embedded schema; for all, the manager's ClusterRole (namespaced rights, bound per environment) | **generated** |
| `cloudcontrol/zz_generated.report.md` | what the generator could not map exactly, per type | generated |
| `cloudcontrol/readiness.csv`, `readiness.md` | every type in the zip: provisioning type, CRD accepted or not, lossy nodes, secrets, sensitive IAM, ACK coverage | generated (`--survey`) |
| `cloudcontrol/test/fake/` | the Cloud Control fake: a generic server plus one moto backend per type, passing everything else to moto | test code |
| `pkg/fakeaws/`, `hack/noaws/` | the guard against real AWS, for every test path in the repository, and its check in `task check` (section 6) | test code |
| `cloudcontrol/test/e2e/` | end to end: envtest, the manager binary, the fake over moto | test code |

Tasks: `task cloudcontrol:generate`, `task cloudcontrol:check` (generated
files current, vet, SPDX, build, unit and end-to-end tests),
`task cloudcontrol:iam` (one environment's role, section 5). `task check`
also runs them (fmt, the generated-files check, and `go test ./...`), and
`task operator-e2e` runs a context on kind.

The kinds live in `<service>.cloudcontrol.aws.hfl.dev`
(`SAMLProvider.iam.cloudcontrol.aws.hfl.dev`), the builder's decision 2:
the engine is visible in the group, a kind cannot clash with one built
here with ACK's generator (`<service>.aws.hfl.dev`), and Bundt serves them
through its own Provider, `hfl.aws-cloudcontrol`. `CloudControlContext` is
`cloudcontrol.aws.hfl.dev/v1alpha1`.

## 2. The mapping: schema to CRD

| Registry schema | Kubernetes | Notes |
| --- | --- | --- |
| `properties` | `spec`, lower camel case (`SamlMetadataDocument` to `samlMetadataDocument`, `S3Logs` to `s3Logs`) | a name clash fails generation; none in the 1,766 types |
| `readOnlyProperties` (top level) | `status.<field>` | nested read-only inside a writable object: dropped from spec, reported |
| `createOnlyProperties` | CEL on the field: `self == oldSelf`; on its parent: `has(self.f) == has(oldSelf.f)` | the API server refuses the edit; paths inside arrays have no rule (a transition rule there needs a keyed list) and are refused by the reconciler instead |
| `writeOnlyProperties` a type opts in (`secrets` in `types.yaml`: write-only strings only) | a Secret reference (`{name, namespace, key}`, ACK's `SecretKeyReference`) | resolved by ACK's runtime, which refuses a Secret in another namespace unless `--enable-cross-namespace` (the operator never sets it, and the manager's rights end at its namespace anyway); a digest in `status.cloudControl.writeOnlyDigest` notices a changed Secret, since Cloud Control never returns the value |
| every other `writeOnlyProperties` | a plain field, never compared (the digest notices a change) | opt-in per field, the builder's decision 4: many are not secrets (an IPAM pool's netmask, a GuardDuty member's email, a SAML provider's key ID to remove). The generator refuses an opted-in pointer that is not a write-only string reachable through objects, and the report lists the remaining candidates |
| `primaryIdentifier` | `status.cloudControl.identifier` (values joined with `\|`), and the read-only parts in status | adoption takes it from ACK's `adoption-fields` annotation |
| `required` | `required`, minus read-only | |
| `tagging.tagProperty` | the tag list; ACK's default tags (`services.k8s.aws/*`) added on create, filtered out of spec | |
| `insertionOrder: false` | compared as a multiset | the drift rule, not the CRD |
| `enum`, lengths, ranges, `minItems`/`maxItems` | kept | |
| `pattern`, `format` | dropped, counted | ECMAScript expressions are not RE2; Cloud Control validates them anyway |
| `$ref` | inlined; a definition that contains itself is cut to `x-kubernetes-preserve-unknown-fields` | |
| free-form object (a policy document) | `type: object` with `x-kubernetes-preserve-unknown-fields` | compared whole |
| a list of types | `x-kubernetes-int-or-string` for integer or string; otherwise preserve-unknown | |
| `handlers` | provisioning type (FULLY_MUTABLE, IMMUTABLE, NON_PROVISIONABLE) and the IAM policy | |

Status also has ACK's `ackResourceMetadata` (ARN when the type has an
`Arn`, account, region) and `conditions`, and `status.cloudControl.request`:
the last Cloud Control request's token, operation, status, and the handler's
error code and message when it failed.

`cloudcontrol/zz_generated.report.md` lists what did not map exactly for
each served type; for the three, nothing was lossy.

## 3. The reconciler

`cloudcontrol/pkg/resource` implements ACK's `AWSResource`,
`AWSResourceDescriptor`, `AWSResourceManager` and its factory once, over
`unstructured.Unstructured`, and `Factories()` makes one factory per
registered type. `cmd/manager` hands them to ACK's
`NewServiceController(...).WithResourceManagerFactories(...)`, so ACK's
reconciler does the rest. Nothing in it names a type.

- **Create.** `CreateResource` with the spec as a CloudFormation model
  (Secret references resolved). The reconciler never blocks a worker on a
  request: it records the request token in `status.cloudControl.request`
  and requeues (`PollDelay`, 3 seconds); the next pass asks
  `GetResourceRequestStatus`, and takes the identifier from the request's
  end, where the fake gives it (the hardest case).
- **Read.** `GetResource` by identifier, on every pass and every resync.
  Writable properties go to the latest spec, read-only ones to status.
- **Drift.** `Delta` compares the fields the spec sets: a field the spec
  leaves out is the server's (a default), not drift. This is narrower than
  awscc's "every optional attribute is also computed" (which loses drift on
  omitted values *and* on values it once set) and does not fight server
  defaults. Unordered arrays are multisets; scalars a handler returns as
  strings ("5", "true") equal the number. A drift found on a resync is put
  back with an update.
- **Update.** An RFC 6902 patch of the top-level properties the spec sets
  that differ: never a read-only property; a create-only difference is an
  error (`ACK.Terminal`) rather than an op; an object is replaced with the
  desired keys merged over AWS's value, so a nested server default stays;
  an array is replaced whole, never patched by position (the awscc bugs
  #3259, #3302, #3234); a write-only property is sent when its digest
  changed, unless it is also create-only.
- **Delete.** `DeleteResource`; in progress, the token is recorded and the
  next pass finishes it; `NotFound` is done. `deletion-policy: retain` is
  ACK's runtime's, unchanged.
- **Never delete.** A type marked `neverDelete: true` in
  `cloudcontrol/types.yaml` (default false) is never deleted: deleting
  its object removes the object and keeps the resource, as
  `deletion-policy: retain` does, but for every object of the type,
  whatever its annotations, including one made before any admission
  policy. It is for a resource whose delete cannot be undone (a landing
  zone, plan section 9 question 15). The generator carries it three ways:
  `Delete` makes no call (`TestNeverDelete`); the type's IAM policy has
  neither `cloudformation:DeleteResource` nor its delete handler's
  actions, and an environment's merged policy drops `DeleteResource`
  when none of its types may be deleted (`TestNeverDeletePolicy`); the
  CRD carries `cloudcontrol.aws.hfl.dev/never-delete: "true"`. It is the
  Cloud Control route's counterpart of a retain-only ACK-generated kind
  (no delete call, `docs/handoff.md`); a type can add the retain-only
  admission policy (`config/admission/retain-only.yaml`) too, so a
  delete without `deletion-policy: retain` is refused rather than
  quietly keeping the resource. No type served sets it yet.
- **Adoption.** ACK's `adoption-policy: adopt` (or `adopt-or-create`) with
  `adoption-fields` naming the primary identifier by field
  (`{"transitGatewayRouteTableId": "tgw-rtb-..."}`) or `{"identifier": ...}`;
  adoption fills spec from AWS. A type whose primary identifier is a name
  the user writes is read by that name, as ACK's generated kinds do.
- **Client tokens.** Every request carries a token derived from the object,
  the operation and the input: a request retried because its status could
  not be written is not made twice. A failed request's token is mixed in,
  so a retry after a failure is a new request. ACK's generated code sets a
  client token only where a kind's hooks do.
- **Errors.** A handler's `InvalidRequest`, `NotUpdatable`, `AlreadyExists`
  and the API's validation-type exceptions are terminal; everything else
  (`AccessDenied`, `Throttling`, `NotStabilized`, `ServiceInternalError`, a
  concurrent operation) is recoverable and retried. The handler's code and
  message are in the condition and in `status.cloudControl.request`.

## 4. Generated share

Measured on this branch, in lines, tests excluded.

| | Per kind, written | Per kind, generated | Shared, written once |
| --- | ---: | ---: | ---: |
| ACK generator, `organizations/Policy` (6.25) | about 270: `hooks.go` 187, `custom_delta.go` 38, three hook templates 16, its part of `generator.yaml` about 30 | about 1,980: Go 1,750 (`sdk.go` 518, `manager.go` 421, ...) and the CRD 234 | ACK's runtime and generator (upstream) |
| Cloud Control, each of the three types | **1** (`typeName` in `types.yaml`) | 157 to 250 lines of CRD, an IAM policy, a registry line, the embedded schema | 3,233: runtime path 2,058 (`resource` 1,198, `cfn` 680, registry and `main`), generator path 1,175 |

So per kind, `Policy` is about 88% generated; a Cloud Control kind is 100%
generated, and costs nothing more in Go. The shared code is the price: at
three kinds the generated lines (799) are a fifth of the total; at the 251
provisionable foundation-gap types (about 200 lines each) the generated
share is about 94%, which is where awscc (96.5% of bytes) and aws-native sit.
The other per-type cost is in the tests: each moto backend in the fake is
70 to 150 lines (section 6).

## 5. IAM from the schemas

Cloud Control runs a type's handlers with the caller's credentials (the
reconciler passes no `RoleArn`), so the environment's role needs the Cloud
Control actions it calls and the permissions of the handlers those actions
run. `iamgen` writes, per type (`cloudcontrol/config/iam/`):

- one statement with the Cloud Control actions, which IAM names in the
  `cloudformation:` namespace: `CreateResource`, `GetResource`,
  `UpdateResource`, `DeleteResource`, `GetResourceRequestStatus`;
  `ListResources` is left out, as the reconciler reads by identifier;
- one statement per handler (create, read, update, delete) with that
  handler's permissions, so a reviewer sees why each action is there; the
  list handler's permissions are left out unless another handler needs
  them;
- actions that reach beyond the resource (`iam:PassRole`,
  `iam:CreateServiceLinkedRole`, `iam:Put*`, `iam:Attach*`,
  `kms:CreateGrant`, `kms:Decrypt`, `sts:AssumeRole`) listed as sensitive in
  the report.

**An environment's role** carries the policies of the kinds its context
enables, nothing more (the builder: "the role's permissions are scoped to
what that environment manages"):

```sh
task cloudcontrol:iam NAMESPACE=team-a-prod ACCOUNT=111122223333 \
  CLUSTER_ARN=arn:aws:eks:us-west-2:111122223333:cluster/platform \
  KINDS=SAMLProvider.iam,Detector.guardduty OUT=iam/team-a-prod
```

writes `permissions-<n>.json`, the merged statements split into as few
documents as IAM's 6,144-character limit on a managed policy allows (one
for the three types here; a role takes ten managed policies by default),
and `trust.json`, the role's trust policy for EKS Pod Identity (section
10). The spike's single `manager-policy.json` for every type is gone: no
role should hold every type's permissions.

For the three types:

| Type | Actions | Left out (list only) | Sensitive |
| --- | --- | --- | --- |
| `AWS::IAM::SAMLProvider` | `iam:CreateSAMLProvider`, `iam:GetSAMLProvider`, `iam:TagSAMLProvider`, `iam:UpdateSAMLProvider`, `iam:ListSAMLProviderTags`, `iam:UntagSAMLProvider`, `iam:DeleteSAMLProvider` | `iam:ListSAMLProviders` | none |
| `AWS::EC2::TransitGatewayRouteTable` | `ec2:CreateTransitGatewayRouteTable`, `ec2:DescribeTransitGatewayRouteTables`, `ec2:CreateTags`, `ec2:DeleteTags`, `ec2:DeleteTransitGatewayRouteTable`, `ec2:GetTransitGatewayRouteTableAssociations`, `ec2:DisassociateTransitGatewayRouteTable` | none | none |
| `AWS::GuardDuty::Detector` | `guardduty:CreateDetector`, `guardduty:GetDetector`, `guardduty:UpdateDetector`, `guardduty:DeleteDetector`, `guardduty:ListDetectors`, `guardduty:TagResource`, `guardduty:UntagResource`, `guardduty:ListTagsForResource`, `iam:CreateServiceLinkedRole`, `iam:GetRole` | none | `iam:CreateServiceLinkedRole` |

plus the five `cloudformation:` actions each. Across the foundation-gap
types, 60 have at least one sensitive action.

**Limits.** Resources are `*`: the schemas name actions, not ARNs, so
scoping by ARN (`arn:aws:iam::<account>:saml-provider/*`) and conditions
(`iam:AWSServiceName` for the service-linked role) is a reviewed step after
generation, which the overlay could carry. Handler permission lists are the
type owners' and are known to be incomplete for some types; whether they
suffice can only be checked on real AWS (moto does not enforce IAM).
Whether Cloud Control's own actions accept resource-level scoping or a
`cloudformation:TypeName` condition was not checked. Attribute-based
scoping by the session tags (a condition such as
`aws:ResourceTag/services.k8s.aws/namespace` against
`aws:PrincipalTag/...`) is possible for the services whose actions take
resource tags, but whether the tags reach the handlers' calls through
Cloud Control is not known without real AWS; not generated.

## 6. The fake, and the guard against real AWS

**moto has no Cloud Control.** moto 5.2.5 (our pin) and its `master`
list `cloudcontrol` among the services with nothing implemented
(`IMPLEMENTATION_COVERAGE.md`); there is no issue or pull request for it.

The options:

| Option | Verdict |
| --- | --- |
| A thin Cloud Control fake here, over moto's per-service APIs | **Chosen.** The protocol and Cloud Control's own rules are generic and schema-driven; only the work (create a SAML provider) is per type, and moto already does it with state. Tests check both sides: the Kubernetes object and moto through each service's real SDK client, as the rest of the repository does, and drift is made through the service's own API. |
| LocalStack | Rejected for the reasons in plan §4.1 (the community repository is archived; the image needs an account under a non-commercial EULA). Its Cloud Control support was not checked. |
| Recorded responses (go-vcr) | Later: recording needs a real account once, and a recording does not answer a read after a write the way a stateful fake does. The right tool for the real-AWS checks of section 11. |

`cloudcontrol/test/fake/server.go`, the generic part (about 490 lines):
the AWS JSON 1.0 protocol and its exceptions; asynchronous requests
(`IN_PROGRESS` for `Polls` answers, then `SUCCESS` or `FAILED`, the
identifier only at the end); client-token replay; `InvalidRequest` for an
unknown, read-only or missing required property; `NotUpdatable` for a patch
of a read-only or create-only property; write-only values never returned
(kept so a test can see a Secret arrived); `ConcurrentOperationException`
while a request on the resource is in progress.

`cloudcontrol/test/fake/moto.go`, the per-type part, stands in for each
type's CloudFormation handler using moto through the SDK:

- `SAMLProvider` on moto's IAM. moto has no tags, assertion encryption mode
  or private keys for SAML providers, so the backend keeps those itself.
- `TransitGatewayRouteTable` on moto's EC2, all of it. moto keeps a deleted
  table with state `deleted`, which the read treats as gone.
- `Detector` on moto's GuardDuty: create, get, update, delete; tags at
  create only (moto has no `TagResource` for GuardDuty).

**The guard against real AWS** (`pkg/fakeaws`, shared by every test path
in the repository: the Cloud Control tests, the ACK kinds' harness, the
operator on kind). The Upjet spike in `highfivelabs/catalog` sent signed
requests to the real Organizations endpoint because an endpoint block was
mistyped and nothing complained.

**A mistake made once, 2026-11-09.** The first kind run gave the Cloud
Control manager only `--aws-endpoint-url`. ACK's runtime asks STS who it is
at start, at `--aws-identity-endpoint-url` or `AWS_ENDPOINT_URL`, else at
the real regional STS endpoint; so that manager almost certainly sent
`GetCallerIdentity`, signed with the dummy key `testing`, to real AWS STS,
which refused it (the manager never became ready; no AWS account or
credential was involved). The Go tests had their locks then; the kind test
set its endpoints flag by flag, and one was missing.

**What changed: one setting, and a check that refuses anything else.**
Setting endpoints one flag at a time was the cause, so nothing in a test
path sets one any more:

1. **One setting.** A fake is one URL, a `fakeaws.Endpoint`. From it come,
   together, every endpoint a process has: ACK's runtime's service and STS
   flags (`--aws-endpoint-url`, `--aws-identity-endpoint-url`) and
   `AWS_ENDPOINT_URL`, which every client the SDK's default configuration
   makes reads (STS for an assumed role, any service a handler or hook
   calls); beside them the fake credentials, the metadata service off, and
   `HTTP(S)_PROXY` (both cases) at `127.0.0.1:9`, a closed port, for every
   host but the fake's and the Kubernetes API's. The Cloud Control fake now
   passes every request that is not a Cloud Control call to moto
   (`Server.Upstream`), so in the Go tests its URL is the manager's only
   endpoint, STS included.
2. **Refused at start.** `fakeaws.Verify` takes a process's whole command
   line and environment (a container's, with `$(VAR)` expanded as
   Kubernetes does) and refuses it unless both runtime endpoints and
   `AWS_ENDPOINT_URL` are set; every endpoint flag or `*ENDPOINT_URL*`
   variable is the fake's (loopback in Go tests, a `.svc` name or private
   address on kind) and none is under an AWS partition's domain (all 14,
   from the SDK's `partitions.json`); `AWS_IGNORE_CONFIGURED_ENDPOINT_URLS`
   is unset; the proxy is the dead one and `NO_PROXY` names only the fake
   and local addresses; and no credential source but the fake keys is
   configured (no role, web identity, Pod Identity or session token).
   - Go tests start every AWS client process with `fakeaws.Command`, which
     builds it from the endpoint and calls `Verify`; their own SDK clients
     come from `fakeaws.Config`, whose HTTP client dials loopback addresses
     only (checked on the literal host before any name is resolved).
   - The operator has one test-only flag, `--fake-aws-endpoint`, which
     replaces `--cloudcontrol-manager-arg`/`-env`. It refuses to start if
     the URL is an AWS domain or outside the cluster; in that mode every
     manager it makes, the Cloud Control ones and those beside ACK's
     controllers, gets the whole setting in place of whatever endpoint,
     proxy or profile it copied, and is verified before it is applied (a
     container with `envFrom`, or AWS variables from `valueFrom`, which
     cannot be seen, is refused).
   - On kind, `tests/operator/run.sh` sets one variable, `FAKE_AWS`; ACK's
     controller gets the same environment (`hack/noaws env`), and after
     each manager comes up `hack/noaws verify-deployments` runs `Verify`
     on every AWS client in the cluster and fails if one could reach AWS
     (or if it found none). Python (boto3) runs only through `fake_py`:
     an environment built from nothing, the endpoint at the port-forward,
     the dead proxy.
3. **Checked in `task check`.** `task noaws` (`hack/noaws`, also in
   `task cloudcontrol:check`) reads every test path (every `_test.go` file;
   `pkg/test`, `tests`, `cloudcontrol/test`, `config/fake-aws`) and fails
   on: an `aws.Config` made by hand, `config.LoadDefaultConfig`, or a
   change to a configuration's `HTTPClient` or `BaseEndpoint` (G1); a
   process started other than `go` whose environment is not set from
   `pkg/fakeaws` (G2); `net/http`'s default client (G3); an AWS endpoint
   written out, a URL or a regional host name, other than an XML namespace
   (G4: tests that show a real endpoint is refused use
   `fakeaws.RealEndpoint`); Python run other than through `fake_py`, or the
   AWS CLI, in `tests/` scripts (S1). It has no exemption marker; only
   `pkg/fakeaws` itself is not read. Its own tests prove each rule fires
   (`hack/noaws/testdata`). Its first run found two unit tests
   (`services/kms` and `services/rds`) with hand-made SDK configurations
   and the kind script's direct Python calls; all now go through
   `pkg/fakeaws`.

`TestVerify` (`pkg/fakeaws`) replays the incident (only the service
endpoint set: refused, naming `--aws-identity-endpoint-url`) and each other
way out; `TestLocks` proves the dial and the proxy locks on their own;
`TestFakeBesideManager` (`pkg/operator`) shows ACK's chart's container
with no identity endpoint refused as copied and fixed in fake mode.

## 7. What was proven

`task cloudcontrol:check` on a Spot VM (e2-standard-8, Go 1.27.3,
envtest Kubernetes 1.35, moto 5.2.5), 2026-11-09: generated files current,
vet, SPDX, build, and every test passed (`cloudcontrol/test/e2e` 162 s for
the three tests; each starts its own moto, fake, kube-apiserver and
manager). The fake answers `IN_PROGRESS` twice before each outcome, so every
create, update and delete below went through the asynchronous path.

**`TestSAMLProvider`**: a SAML provider with its metadata, a private key
from a Kubernetes Secret (write-only, create-only) and a tag. The create is
followed through three `GetResourceRequestStatus` calls to `SUCCESS`; the
ARN, the primary identifier, lands in `status.arn`,
`status.cloudControl.identifier` and `status.ackResourceMetadata.arn`; moto
has the provider with the metadata; the Secret's value reached Cloud
Control, `GetResource` never returns it, and the spec keeps the reference.
Changing the metadata sends exactly one op, `replace /SamlMetadataDocument`,
and moto has the new document. Changing `spec.name` or removing
`spec.addPrivateKey` is refused by the API server with the create-only
message. Delete removes it from moto.

**`TestTransitGatewayRouteTable`**: a route table on a gateway made in
moto, with a tag: `status.transitGatewayRouteTableId` is set, moto has the
tag and ACK's `services.k8s.aws/namespace` tag, and ACK's tags are not
written into spec. A tag change and a new tag are one `replace /Tags`
(nothing else is ever patched). Reordering the tags, an unordered list, is
no update across three resyncs. Changing the gateway is refused by CEL.
`deletion-policy: retain` leaves the table in moto; a new object adopts it
by `{"transitGatewayRouteTableId": ...}`, is marked adopted, and gets its
spec tags from AWS without ACK's; deleting that object deletes the table. A
route table on a gateway that does not exist ends `ACK.Terminal`, as the
user sees it: "Cloud Control CREATE failed: InvalidRequest:
InvalidTransitGatewayID.NotFound: Transit Gateway tgw-00000000000000000 was
deleted or does not exist", with `status.cloudControl.request.errorCode:
InvalidRequest`.

**`TestDetector`**: a detector with a nested data source and a list of
features. Changing its finding frequency through GuardDuty's own API
(drift) is put back within a resync, by exactly one
`replace /FindingPublishingFrequency`; moto's extra data sources (the
server's defaults) cause no update. Reordering the features is no update
across three resyncs. Turning off S3 logs in the nested object is one
replace, merged over AWS's value, and moto shows it off. Delete removes it.

**Per environment** (the productionized route, `task check` and
`task operator-e2e` on a Spot VM, 2026-11-09, commit d5bf9e7; three
e2e tests in 156 s). The end-to-end tests above now run the manager with
exactly the arguments the operator gives it for a `CloudControlContext`
(`--watch-namespace=team-a`, `--cloudcontrol-kinds` of all three), as
envtest user `system:serviceaccount:ack-extensions-system:ack-extensions-cloudcontrol-team-a`
holding only the operator's RoleBinding in `team-a` to the generated
ClusterRole: so the generated rights suffice. `TestDetector` also makes a
detector in `team-b`; after every step and many resyncs it has no
finalizer, no status and no Cloud Control request. `TestCloudControlContext`
(`pkg/operator`, envtest): a context not named `cloudcontrol` and a
malformed role ARN are refused; an unknown kind sets `Ready=False`
(`UnknownKinds`) and makes nothing; the ServiceAccount (no
`eks.amazonaws.com/*` annotation) and the Deployment are in the operator's
namespace with the expected arguments, the RoleBinding in the environment,
owned by the context; `status.podIdentityAssociation`; by
SubjectAccessReview the manager may manage the kinds, read Secrets and take
leases in its environment and nothing in another namespace, in the
operator's namespace or cluster-wide; the ACK side's clean-up leaves it
alone; deleting the context removes the manager and its ServiceAccount
(finalizer). On kind (`tests/operator/run.sh`), with the operator from
`config/default`: the context becomes `Ready` when its manager rolls out,
the manager runs in `ack-extensions-system` as its environment's
ServiceAccount, the rights hold as above, `team-a`'s Detector is taken up
(finalizer) while `team-b`'s is never touched, and deleting the context
removes the manager, its ServiceAccount and the RoleBinding.

**Unit tests** (`cloudcontrol/pkg/...`, `cloudcontrol/test/fake`): names,
pointer sets, conversion round trips, the drift rules, the three CRDs
(validated as the API server would), patches never touching read-only or
create-only properties, object merges keeping nested defaults, client
tokens, the IAM policies, the fake's own rules, and the guard.

**Found by the end-to-end test, fixed:** ACK's runtime patches the object
`ClearResolvedReferences` returns and writes the API server's answer into
it; returning the resource itself (not a copy, as generated code does) let
the adoption's metadata patch overwrite the observed spec, so an adopted
object's spec was not filled. The first run of the route-table test failed
on that; the second passed.

**Types in foundation order, batch 1: F02, IAM Identity Center**
(`TestIdentityCenter`). `AWS::SSO::PermissionSet` and `AWS::SSO::Assignment`
on moto's `sso-admin`; their backend keeps tags and the permissions
boundary, which moto lacks (`cloudcontrol/test/fake/moto_sso.go`). A
permission set with a managed policy, an inline policy (a free-form
document) and a tag is made in moto's default instance; its identifier is
`InstanceArn|PermissionSetArn`; resyncs of the unchanged set make no
update; a second managed policy is one `replace /ManagedPolicies` and
moto has both; a new name is refused by the API server (create-only). An
assignment of it to a group in an account gets the six-part identifier and
is in moto's `ListAccountAssignments`; a new account is refused
(create-only; the type has no update handler); deleting each removes it
from moto. `AWS::SSO::InstanceAccessControlAttributeConfiguration` is
**unproven on the fake**: moto 5.2.5 has none of its API. It is marked
`unproven` in `types.yaml`, which the generator writes into the CRD
(`cloudcontrol.aws.hfl.dev/unproven-on-fake`) and the coverage data
reports; the e2e harness refuses a type with neither a backend nor that
mark. Results: the pull request.

**Batch 2: F04, Transit Gateway routing** (`TestTransitGatewayRouting`).
`AWS::EC2::TransitGatewayRouteTableAssociation`,
`AWS::EC2::TransitGatewayRouteTablePropagation`,
`AWS::EC2::TransitGatewayRoute` and
`AWS::EC2::TransitGatewayPeeringAttachment` on moto's EC2
(`cloudcontrol/test/fake/moto_tgw.go`); ACK's ec2-controller v1.23.1 has
`TransitGateway` and `TransitGatewayVPCAttachment` and none of these, and
no open pull request for them. The fixture (two gateways, a VPC attached to
the first, a route table) is made with the EC2 SDK. The attachment is
associated with the table and propagates to it, each with the identifier
`<table>|<attachment>` and in EC2's `GetTransitGatewayRouteTable*`; a
static route to the attachment and a blackhole route are in EC2's
`SearchTransitGatewayRoutes` (`<table>|<cidr>`); a peering request to the
second gateway gets its attachment ID and `pendingAcceptance` in status and
the spec's tags plus ACK's, and a tag change is one `replace /Tags`;
resyncs of the five make no update; a create-only change to each is
refused; each delete removes it from EC2. The three links and the route are
`IMMUTABLE` (no update handler). moto differs from AWS in four ways the
backend makes up for: it associates twice and makes a static route twice
where AWS refuses the second (the backend answers `AlreadyExists`); it
drops a static route's attachment from `SearchTransitGatewayRoutes` (the
backend keeps it); it fails `DeleteTransitGatewayRoute` of a route that is
gone with an internal error (the backends read first); and it keeps a
deleted peering attachment in state `deleted` (read as gone). **What the
kinds do not do:** the peering kind is the requester's side only; the peer
accepts it (`AcceptTransitGatewayPeeringAttachment`, which no
CloudFormation type has), so a peering made here stays
`pendingAcceptance` until someone does. The references
(`transitGatewayRouteTableRef` to our `TransitGatewayRouteTable`,
`transitGatewayRef` and the attachment to ACK's kinds) wait for the
overlay (section 12, item 4); today the IDs are written out.

**Batch 3: F07, Control Tower controls and baselines.**
`AWS::ControlTower::EnabledControl` (a control on an OU, its identifier
`<target>|<control>`) and `AWS::ControlTower::EnabledBaseline` (a baseline
on an OU or account, identified by its own ARN) are **unproven on the
fake**: moto 5.2.5 has no Control Tower and no other emulator does, so both
are generated, accepted by envtest's API server with the other CRDs, and
marked `unproven` in `types.yaml`; there is no e2e test. Each parameter's
`Value` is free-form (`oneOf` without a type), so it is
`x-kubernetes-preserve-unknown-fields` and compared as JSON. The create
handlers' permissions reach into Organizations, Service Catalog and IAM
(`config/iam/`), and `EnabledControl`'s create may take up to 720 minutes;
the reconciler polls the request without holding a worker.
`AWS::ControlTower::LandingZone` is held back: deleting one decommissions
the landing zone (plan section 9, question 15). ACK has no Control Tower
controller, issue or pull request.

**Batch 4: F08 and F09, DNS** (`TestDNS`). Ten types; ACK's
route53-controller v1.8.0 (`HealthCheck`, `HostedZone`, `RecordSet`) and
route53resolver-controller v1.11.0 (endpoints, rules, query log
configurations and their associations) have none of them, and no open
pull request for them (rechecked 2026-11-09). Two are proven on moto
(`cloudcontrol/test/fake/moto_dns.go`): `AWS::Route53::QueryLoggingConfig`
(a public hosted zone's query logs to a log group) and
`AWS::Route53Resolver::ResolverDNSSECConfig` (a VPC's DNSSEC
validation). The fixture (a hosted zone, a log group in us-east-1, where Route 53
requires it and moto checks it, and a VPC) is made with the SDKs. The query logging configuration gets its ARN as identifier and
its ID in status and is in Route 53's `GetQueryLoggingConfig`; the DNSSEC
configuration gets its `rdsc-` ID and `ENABLED`; resyncs of both make no
update; a create-only change to each is refused (both are `IMMUTABLE`);
deleting the first removes it, deleting the second turns validation off
(`DISABLED`), which reads as gone. moto differs from AWS in two ways the
backends make up for: Route 53 has no ARN for a query logging
configuration (CloudFormation's handler makes
`arn:aws:route53:::queryloggingconfig/<id>`; so does the backend), and
moto enables validation for a VPC that does not exist (the backend looks
the VPC up first, as the handler's `ec2:DescribeVpcs` permission
suggests). **Unproven on the fake** (moto 5.2.5 has none of their API):
`AWS::Route53::DNSSEC` (deleting it disables signing: remove the zone's
DS record at the parent first, or the zone stops resolving for
validating resolvers), `AWS::Route53::KeySigningKey`,
`AWS::Route53::CidrCollection`, `AWS::Route53Resolver::ResolverConfig`,
and the four DNS Firewall types (`FirewallDomainList`,
`FirewallRuleGroup` with its rules as a field, `FirewallRuleGroupAssociation`,
`FirewallConfig`). Left out: `TrafficPolicy` and `TrafficPolicyInstance`
(traffic flow, low priority in `docs/coverage/foundation.md`) and a hosted
zone's second VPC (no CloudFormation type: a field of
`AWS::Route53::HostedZone`, ACK's). References (`hostedZoneRef`,
`vpcRef`, a KMS key's ARN for the key-signing key) wait for the overlay.

**Not shown by the fake** (section 11): anything about real AWS.

`task check` (the whole repository) is reported in the pull request.

## 8. Readiness: how much of the gap Cloud Control covers

`go run ./hack/cloudcontrol-gen --survey` runs the generator over every
`AWS::` type in the pinned zip (1,875, the coverage dashboard's
denominator) and validates each CRD as the API server would. The rows are
`cloudcontrol/readiness.csv`; `cloudcontrol/readiness.md` has the
foundation-gap list.

| Category | Types | Provisionable | FULLY_MUTABLE | IMMUTABLE | CRD accepted | Clean | Gap types | Gap, provisionable | Gap, clean |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| platform-foundation | 241 | 234 | 186 | 48 | 234 | 216 | 179 | 172 | 165 |
| security-adjacent | 112 | 94 | 87 | 7 | 94 | 89 | 97 | 79 | 76 |
| application | 262 | 250 | 232 | 18 | 250 | 230 | 147 | 136 | 128 |
| other | 1,260 | 1,188 | 1,084 | 104 | 1,188 | 1,137 | 1,169 | 1,101 | 1,059 |
| all | 1,875 | 1,766 | 1,589 | 177 | 1,766 | 1,672 | 1,592 | 1,488 | 1,428 |

*Provisionable* is derived from the schema's handlers (create, read and
delete present), which is how CloudFormation derives `ProvisioningType`;
it was not checked against Cloud Control's live `ListTypes`. *Clean* means
no preserve-unknown node and a CEL rule for every create-only path. *Gap*
means neither ACK nor this repository has a kind (`docs/coverage/resources.csv`).

**Foundation gaps: 251 of 276 are provisionable, and all 251 make an
accepted CRD; 241 are clean.** That covers, among others, F02 (Identity
Center `PermissionSet`, `Assignment`), F04 (Transit Gateway route tables,
routes, associations, propagations, peering), F05 (`VPNConnection`), F07
(Control Tower `LandingZone`, `EnabledControl`, `EnabledBaseline`: no
emulator anywhere, so the generic route is the only cheap one), F08, F09
(Resolver DNS Firewall), F12 (Identity Store), F13 (Network Manager, Direct
Connect), F14 to F18 (Config, GuardDuty, Security Hub, Access Analyzer,
Macie, Inspector, Detective, Firewall Manager, Audit Manager, Security
Lake).

The 25 that are not provisionable: WAF Classic and WAF Regional (18, a
deprecated service), `AWS::IAM::Policy`, `AWS::IAM::AccessKey`,
`AWS::IAM::UserToGroupAddition`, `AWS::EC2::NetworkAclEntry`,
`AWS::EC2::NetworkInterfacePermission`, `AWS::EC2::VPNGatewayRoutePropagation`,
`AWS::Route53::RecordSetGroup`. Several are already covered inline by ACK
(inline policies, NACL entries, record sets) or by kinds here; the rest
are the fallback's (ACK's generator first, Upjet only if that fails).

**The 25 this route cannot serve, and what could** (2026-11-09, for the
builder's question whether any fallback, such as community Crossplane
providers as a third-party Provider, is needed):

| Type | Why not Cloud Control | What fills it | Fallback needed? |
| --- | --- | --- | --- |
| 7 `AWS::WAF::*` (ByteMatchSet, IPSet, Rule, SizeConstraintSet, SqlInjectionMatchSet, WebACL, XssMatchSet) | non-provisionable (no handlers): WAF Classic, a deprecated service | nothing: WAF v2 replaces it, served by ACK's `wafv2-controller` (and our `WebACLAssociation`) | no |
| 11 `AWS::WAFRegional::*` (the seven above for regional, plus GeoMatchSet, RateBasedRule, RegexPatternSet, WebACLAssociation) | the same: WAF Classic regional | the same: WAF v2 | no |
| `AWS::IAM::Policy` (an inline policy) | non-provisionable | ACK inline: `Role`, `User` and `Group` `.spec.inlinePolicies` | no |
| `AWS::EC2::NetworkAclEntry` | non-provisionable | ACK inline: `NetworkACL.spec.entries` | no |
| `AWS::Route53::RecordSetGroup` | non-provisionable (a CloudFormation batching construct) | ACK's `RecordSet`, one object per record | no |
| `AWS::IAM::UserToGroupAddition` | non-provisionable | ours: `iam.aws.hfl.dev/UserGroupMembership` (non-exclusive, user side; the coverage mapping counts it under the group-membership resource) | no |
| `AWS::IAM::AccessKey` | non-provisionable (the secret is returned once) | hand-written on ACK's runtime (`LoginProfile` pattern plus a Secret write-back), if the builder wants long-lived keys at all (plan §9 question 10 recommends not) | no |
| `AWS::EC2::NetworkInterfacePermission` | non-provisionable | ACK's generator here: `CreateNetworkInterfacePermission`, `DescribeNetworkInterfacePermissions`, `DeleteNetworkInterfacePermission` are a plain create/read/delete; moto 5.2.5 has none of them, so unproven on fake | no |
| `AWS::EC2::VPNGatewayRoutePropagation` | non-provisionable | ACK's generator here as an attach pair (`EnableVgwRoutePropagation`/`DisableVgwRoutePropagation`, read from `DescribeRouteTables`), the `PolicyAttachment` pattern, or a field on a route table; moto has neither call | no |

So every one of the 25 is deprecated, already covered inline by ACK or by
a kind here, or a small kind ACK's generator (or one hand-written kind) can
make in this repository. **No third-party Provider is needed to cover the
foundation.** What is not known is whether the 251 provisionable types
behave on real AWS (section 11); a type whose handler misbehaves there is
the case for a per-type fallback (ACK's generator first, Upjet after), and
only a live run can say which, if any.

The survey now counts Cloud Control kinds as ours, so its gap columns
shrink as types are served; the 276 and 251 above are from before any
was counted.

The ten not clean have one free-form node each (`PermissionSet`'s inline
policy, Control Tower's `Manifest` and `Parameters`, resource policies as
objects or strings) or a create-only path inside an array
(`VirtualMFADevice`). They work; their free-form values are compared as
whole JSON documents.

## 9. Compared with our ACK-generated `organizations/Policy`

| | ACK generator (`Policy`) | Cloud Control (generic) |
| --- | --- | --- |
| Per-kind work | about 270 written lines: hooks for the summary wrapper, tags through `ListTagsForResource`/`TagResource`, a JSON comparison | one line; the CloudFormation handler already hides the API's shape (wrappers, separate tag calls, stabilisation) |
| Drift | every spec field, against the SDK's read | the fields the spec sets, against the handler's read model; unordered lists as sets for every type, from the schema |
| What drift can see | what the Describe call returns | what the read handler returns: some handlers return less than the API (awscc has types frozen for this), and the model is CloudFormation's, which can differ from the API's |
| Error detail | the API's own exception (`MalformedPolicyDocumentException`) | a handler error code from a list of 16 (`InvalidRequest`, `GeneralServiceException`, ...) plus the handler's message, which usually carries the service's text; terminal or retryable is decided on the coarse code |
| Speed | one or two direct calls per pass | asynchronous: a request, then one or more status polls (3 seconds apart here); handlers include stabilisation waits, so a create can take longer, but "ready" means ready. Every resync is a `GetResource`, which runs the read handler (several service calls for some types); Cloud Control's own rate limits were not measured |
| Eventual consistency | handled by hooks and requeues, per kind | the handlers retry and stabilise (IAM propagation, "pending" states); the reconciler only polls |
| Types with odd APIs | Put-shaped singletons, attach/detach pairs and wrapped responses each need a hand-written kind or hooks (plan §2) | uniform, if CloudFormation models the type; but CloudFormation's model decides the semantics: `AWS::Organizations::Policy.TargetIds` is the exclusive attachment foundation.md says not to build, and a non-provisionable type (`AWS::IAM::Policy`) cannot be served at all |
| References | generated `fooRef` to ACK's kinds | none yet; only 27 of the 1,875 schemas carry `relationshipRef`, so references would come mostly from the overlay |
| Identity | runs beside ACK's controller with its identity | its own (section 10) |
| Retirement into ACK | a group move with adoption | the same: adopt by identifier in ACK's group, delete ours with `retain` |
| Generated share | about 88% per kind | 100% per kind; about 94% overall at the foundation-gap scale |

## 10. Identity and placement: one manager per environment

The builder's decision 3 (above, verbatim): identity at the environment
level, through EKS Pod Identity, loosely Config Connector's namespaced
mode. Config Connector, read for this: a `ConfigConnectorContext` (named
`configconnectorcontext.core.cnrm.cloud.google.com`, one per namespace)
names a Google service account; Config Connector's operator then runs a
controller manager for that namespace in `cnrm-system`, as a Kubernetes
ServiceAccount of its own (`cnrm-controller-manager-<namespace>`) bound to
the Google service account by Workload Identity, watching only that
namespace. Ours is the same shape on AWS:

```yaml
apiVersion: cloudcontrol.aws.hfl.dev/v1alpha1
kind: CloudControlContext
metadata:
  name: cloudcontrol            # one per namespace; CEL refuses any other name
  namespace: team-a-prod        # the environment
spec:
  roleARN: arn:aws:iam::111122223333:role/team-a-prod-cloudcontrol
  region: us-west-2
  kinds: [SAMLProvider.iam, Detector.guardduty]   # optional; empty: every kind
  deletionPolicy: delete        # optional; or retain
```

**What the operator makes** (`pkg/operator/cloudcontrol.go`), when its
`MANAGER_IMAGE_CLOUDCONTROL` is set:

| Where | What | Why |
| --- | --- | --- |
| the operator's namespace (`ack-extensions-system`) | ServiceAccount `ack-extensions-cloudcontrol-<environment>`, no IRSA annotation | the identity the Pod Identity association names; in the operator's namespace, as Config Connector's are in `cnrm-system`, so nobody who may only create pods in the environment's namespace can run as the role and step around admission policy on the kinds |
| the operator's namespace | Deployment `ack-extensions-cloudcontrol-<environment>`: the manager, `--watch-namespace=<environment>`, `--aws-region`, `--cloudcontrol-kinds` (group-qualified, since two services can have kinds of the same name), `--deletion-policy`, leader election in the environment | one manager per environment, serving every Cloud Control kind it enables |
| the environment's namespace, owned by the context | RoleBinding `ack-extensions-cloudcontrol-manager` of that ServiceAccount to the generated ClusterRole | Kubernetes rights in that namespace only: its kinds, its Secrets (references), its leases and events; nothing cluster-wide, nothing in the operator's namespace |
| the context | a finalizer, and `status`: `podIdentityAssociation` (namespace, ServiceAccount, role), the kinds served, `Ready` when the manager is available | an owner reference cannot cross namespaces; the status says exactly which association to make |

With one watch namespace ACK's runtime runs no namespace or CARM cache, so
the manager needs no cluster-wide read; CARM is off (`--enable-carm=false`):
the environment's role is the identity. An unknown kind in `spec.kinds`
leaves whatever runs alone and sets `Ready=False`, `UnknownKinds`. The
operator holds no AWS credentials and may bind only the manager's
ClusterRole by name. A fake AWS's endpoint and dummy credentials reach the
managers only through the operator's own test-only flag
(`--fake-aws-endpoint`, section 6), never through a context: an
environment cannot point its manager at another endpoint.

**The EKS Pod Identity association** the environment needs (the operator
cannot make it: it is an EKS API call, made with AWS credentials):

```sh
aws eks create-pod-identity-association --cluster-name platform \
  --namespace ack-extensions-system \
  --service-account ack-extensions-cloudcontrol-team-a-prod \
  --role-arn arn:aws:iam::111122223333:role/team-a-prod-cloudcontrol
```

or ACK's own kind, where ACK's eks-controller runs with rights to make it:

```yaml
apiVersion: eks.services.k8s.aws/v1alpha1
kind: PodIdentityAssociation
metadata: {name: team-a-prod-cloudcontrol, namespace: ack-extensions-system}
spec:
  clusterName: platform
  namespace: ack-extensions-system
  serviceAccount: ack-extensions-cloudcontrol-team-a-prod
  roleARN: arn:aws:iam::111122223333:role/team-a-prod-cloudcontrol
```

The cluster needs the EKS Pod Identity Agent add-on. The role's trust
policy (`task cloudcontrol:iam` writes it) lets `pods.eks.amazonaws.com`
`sts:AssumeRole` and `sts:TagSession`, with conditions on Pod Identity's
**session tags**: `aws:RequestTag/kubernetes-namespace` is the operator's
namespace and `aws:RequestTag/kubernetes-service-account` is this
environment's ServiceAccount, plus `aws:SourceAccount` and `aws:SourceArn`
(the cluster). So an association made by mistake for another
environment's ServiceAccount, for a pod in the environment's own
namespace, or in another cluster cannot use the role; and every call the
manager makes is in CloudTrail with those tags. Pod Identity, not IRSA, as
the builder decided: no OIDC provider per cluster, no role-ARN annotation
for anyone with ServiceAccount rights to copy, and a role's trust policy
that names no cluster's OIDC issuer.

**A cluster that is not EKS** (documented, not built). The Pod Identity
Agent runs only on EKS (and EKS Hybrid Nodes), so elsewhere the manager
needs another source of the environment's credentials; the managers use
the AWS SDK's default chain, so any of these works without code:

- *IRSA-style web identity, self-hosted*: publish the cluster's service
  account issuer as an IAM OIDC provider, run
  `amazon-eks-pod-identity-webhook`, and give the role a trust policy on
  `sub: system:serviceaccount:ack-extensions-system:ack-extensions-cloudcontrol-<environment>`.
  It would need the ServiceAccount annotation `eks.amazonaws.com/role-arn`,
  which the operator does not set today (a context field would).
- *IAM Roles Anywhere*: an X.509 certificate per environment and the
  credential helper as a sidecar or `credential_process`; the operator
  would mount the certificate's Secret.
- *A credentials Secret* (ACK's `aws.credentials.secretName`): a static key
  per environment. Long-lived keys are what this repository avoids
  (plan 9, question 10); last resort.

**What kind cannot show** (`task operator-e2e`): the Pod Identity Agent
exchanging the pod's token for the role's credentials, and AWS honouring
the trust policy's conditions. Everything on our side of that is proven
(section 7).

## 11. What did not work, or is not known

- **Write-only to Secret was not right by default** (the spike made every
  write-only string a Secret reference). Of the 35 foundation-gap types
  with a write-only string, many are not secrets
  (`AWS::EC2::VPCCidrBlock`'s IPAM parameters,
  `AWS::SecretsManager::RotationSchedule`'s eleven, `GuardDuty::Member`'s
  email). Now opt-in per field (`secrets` in `types.yaml`), the builder's
  decision 4.
- **Names.** Lower camel case from PascalCase is mechanical and mostly
  right; `IPv6CidrBlock` becomes `iPv6CidrBlock`. An overlay rename is needed
  for a handful; ACK's own names (`ipv6CIDRBlock`) come from its own rules.
- **No references.** A route table names its gateway by ID; ACK's kinds
  would take `transitGatewayRef`. Only 27 of the 1,875 schemas carry
  `relationshipRef` (none of the three here), so references to ACK's kinds
  and to other Cloud Control kinds would be overlay data; not built.
- **Removing a field** from spec leaves AWS's value: the reconciler only
  manages what the spec sets. Tracking what it last applied would allow a
  `remove`; not built.
- **Per-type test cost.** The fake's generic part is shared, but each type
  needs a moto backend (70 to 150 lines) and moto must implement the
  service: of the foundation items, moto lacks Control Tower, the Resolver
  firewall, Inspector and others (`docs/coverage/foundation.md`). For those
  a generic in-memory backend (store the model, apply the schema's rules)
  proves the reconciler but not the service; recordings from a real account
  prove the service.
- **Not known without real AWS:** each handler's real error codes and
  messages; whether `GetResource` returns values that differ from what was
  sent (normalised JSON, defaults inside objects, upper-cased enums) and so
  cause a perpetual update (awscc's suppression list is made of such cases);
  whether an update touching a create-only path fails synchronously or as
  an event; Cloud Control's request timings and rate limits under a resync
  of many objects; whether the handler permission lists suffice; how long
  request tokens live; whether a type the schema says is provisionable is
  served by Cloud Control in every region.

## 12. Decisions taken, and the plan

The builder's decisions are at the top. The limits that come with "go,
with limits":

1. it is proven on a fake only; every kind is marked untested on real AWS
   until one run on a real account has checked the read model against
   what was sent (section 11);
2. use it for types whose CloudFormation model is what a platform team
   would write and that need no reference or exclusive/non-exclusive
   choice; types that do (attachments, memberships, anything ACK's kinds
   reference), or where ACK's generator gives better behaviour, stay on
   ACK's generator here, as now;
3. Upjet stays the fallback for a type Cloud Control serves badly on real
   AWS or cannot serve.

**Plan.**

1. Done (`agent/cloudcontrol`): the generic manager, the CRD and IAM
   generator, the fake over moto and its three guards, the per-environment
   identity and `CloudControlContext`, Secret references opt-in, the
   operator's part, the spike's three types.
2. Types in foundation order, in reviewable batches, each with a
   fake-backed test where moto has the service, else marked "unproven on
   fake" in the coverage data; `task coverage` counts them as ours. Done:
   the coverage change, batch 1 (F02, three Identity Center types) and
   batch 2 (F04, four Transit Gateway routing types), batch 3 (F07, two
   Control Tower types, unproven on the fake) and batch 4 (F08, F09: ten
   DNS types, two proven on moto).
   Next: the rest of F03 to F18 by `docs/coverage/foundation.md`.
3. The live-test switch (`task test-live`, a sweeper, the ledger
   `docs/live-runs.md`) and the CloudFormation template for the sandbox
   account (credentials by federation from the offload VMs' Google service
   account, never stored keys). Built (`docs/live-runs.md`; nothing
   deployed, no live run: the account does not exist); next, one real run
   per type once the account exists, recording responses; quirks found go into `types.yaml` as data
   (a secret path, a server-normalised field, a frozen schema), never as
   per-type Go.
4. The overlay: renames, references (from `relationshipRef` where a schema
   has it, otherwise overlay data; ACK's kinds first: `transitGatewayRef`
   to ACK's `TransitGateway`), removal of fields the spec drops.
5. Selective CRD installation (only the kinds some context enables), once
   the kind count makes installing them all costly.
6. Currency: the factory's watcher diffs the schemas' behaviour keys; a
   type whose new schema breaks generation is frozen at its last good
   schema, as awscc's bigdiffer does.
