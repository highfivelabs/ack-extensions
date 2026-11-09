# Cloud Control: one generic controller for the AWS long tail (spike)

Status, 2026-11-09: a spike on branch `agent/cloudcontrol-spike`. The
builder chose the route for AWS types ACK lacks: "Cloud Control first,
Upjet as fallback", one generic reconciler of our own over the AWS Cloud
Control API, with CRDs generated from the CloudFormation registry schemas,
as Terraform's `awscc` and Pulumi's `aws-native` do. This document is the
design, what was proven and how, what was not, and the decision the builder
has to make. Everything was run against a fake: no AWS account was used.

**In one paragraph.** It works on the fake. One generic resource manager
(about 1,200 lines) implements ACK's runtime interfaces over unstructured
objects, so ACK's own runtime drives it: the same finalizer, deletion
policy, adoption annotations, CARM, conditions (`ACK.ResourceSynced`,
`Ready`, `ACK.Terminal`, `ACK.Recoverable`, `ACK.Adopted`) and flags as
every other kind here. A generator turns a type's registry schema into a
CRD the API server accepts, an embedded schema, and a least-privilege IAM
policy. Three foundation types ACK lacks
(`AWS::IAM::SAMLProvider`, `AWS::EC2::TransitGatewayRouteTable`,
`AWS::GuardDuty::Detector`) were generated with **no per-type Go** and run
end to end on envtest against a thin Cloud Control fake over moto. Run on
every type in the pinned schema zip, the generator makes an accepted CRD for
**all 1,766 provisionable types**, including **251 of the 276 foundation-gap
types** (the other 25 are not provisionable through Cloud Control at all,
18 of them WAF Classic). The recommendation is **go, with limits** (the
end of this document).

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
10. Identity and placement (plan §9, question 12)
11. What did not work, or is not known
12. Recommendation, phased plan, and the builder's decisions

## 1. What was built

| Path | What | Written or generated |
| --- | --- | --- |
| `cloudcontrol/types.yaml` | the types to serve: one line each (`typeName`) | written, the only per-type input |
| `hack/cloudcontrol-gen/` | the generator; `--survey` runs it on every type in the zip | written once |
| `cloudcontrol/pkg/cfn/` | reads registry schemas: behaviour lists, pointers, names, conversion, comparison | written once |
| `cloudcontrol/pkg/crdgen/`, `crdcheck/` | schema to CRD; validation as the API server does it (structural schema, CEL compile and cost) | written once |
| `cloudcontrol/pkg/iamgen/` | handler permissions to an IAM policy | written once |
| `cloudcontrol/pkg/resource/` | the generic resource manager on ACK's runtime | written once |
| `cloudcontrol/cmd/manager/` | the manager binary (ACK's generated `main.go`, shape for shape) | written once |
| `cloudcontrol/config/crds/`, `config/iam/`, `config/rbac/`, `pkg/registry/zz_generated.registry.go`, `pkg/registry/schemas/` | per type: CRD, IAM policy, the manager's ClusterRole, the registry entry, the embedded schema | **generated** |
| `cloudcontrol/zz_generated.report.md` | what the generator could not map exactly, per type | generated |
| `cloudcontrol/readiness.csv`, `readiness.md` | every type in the zip: provisioning type, CRD accepted or not, lossy nodes, secrets, sensitive IAM, ACK coverage | generated (`--survey`) |
| `cloudcontrol/test/fake/` | the Cloud Control fake: a generic server plus one moto backend per type; the guard | test code |
| `cloudcontrol/test/e2e/` | end to end: envtest, the manager binary, the fake over moto | test code |

Tasks: `task cloudcontrol:generate`, `task cloudcontrol:check` (generated
files current, vet, SPDX, build, unit and end-to-end tests). `task check`
also runs them (fmt, the generated-files check, and `go test ./...`).

The kinds live in `<service>.cloudcontrol.aws.hfl.dev`
(`SAMLProvider.iam.cloudcontrol.aws.hfl.dev`): a separate suffix so the
spike cannot clash with a kind built here with ACK's generator (F03 proposes
an ACK-generated `iam.aws.hfl.dev/SAMLProvider`). The group is a builder
decision (section 12).

## 2. The mapping: schema to CRD

| Registry schema | Kubernetes | Notes |
| --- | --- | --- |
| `properties` | `spec`, lower camel case (`SamlMetadataDocument` to `samlMetadataDocument`, `S3Logs` to `s3Logs`) | a name clash fails generation; none in the 1,766 types |
| `readOnlyProperties` (top level) | `status.<field>` | nested read-only inside a writable object: dropped from spec, reported |
| `createOnlyProperties` | CEL on the field: `self == oldSelf`; on its parent: `has(self.f) == has(oldSelf.f)` | the API server refuses the edit; paths inside arrays have no rule (a transition rule there needs a keyed list) and are refused by the reconciler instead |
| `writeOnlyProperties`, strings | a Secret reference (`{name, namespace, key}`, ACK's `SecretKeyReference`) | resolved by ACK's runtime; a digest in `status.cloudControl.writeOnlyDigest` notices a changed Secret, since Cloud Control never returns the value |
| `writeOnlyProperties`, other | a plain field, never compared | |
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
reconciler passes no `RoleArn`), so the manager's role needs the Cloud
Control actions it calls and the permissions of the handlers those actions
run. `iamgen` writes, per type (`cloudcontrol/config/iam/`), and merged for
the manager (`manager-policy.json`):

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
suffice can only be checked on real AWS (moto does not enforce IAM). Whether
Cloud Control's own actions accept resource-level scoping or a
`cloudformation:TypeName` condition was not checked.

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

**The guard** (`cloudcontrol/test/fake/guard.go`). The Upjet spike in
`highfivelabs/catalog` sent signed requests to the real Organizations
endpoint because an endpoint block was mistyped and nothing complained. Here
three independent locks, each enough on its own:

1. `LocalOnly`: the harness refuses to start unless moto's and the fake's
   URLs are loopback addresses;
2. `HTTPClient`: every SDK client the tests and the fake make dials loopback
   addresses only, checked on the literal host before any name is resolved,
   so a missing endpoint override fails at the dial;
3. `ChildEnv`: the manager and moto run with an environment built from
   nothing: the credentials `testing`/`testing`, `/dev/null` shared config,
   and `HTTP(S)_PROXY` at `127.0.0.1:9`, a closed port; Go never proxies
   loopback, so every other request the manager makes dies at the proxy.

`TestGuard` proves each: the real endpoint is refused by `LocalOnly`; an SDK
client with no endpoint override fails with "refusing to dial"; the child
environment sends the real endpoint to the dead proxy and loopback direct.

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

## 10. Identity and placement (plan §9, question 12)

There is no ACK controller to run beside for these types, so the operator's
model (copy an ACK controller's ServiceAccount annotations, flags,
environment and CARM; `Beside` for EventBridge Scheduler) has nothing to
copy from. Because the manager is built on ACK's runtime, it takes ACK's
flags and does ACK's per-resource identity (CARM, team CARM,
`IAMRoleSelector`) unchanged; what it lacks is a base identity.

| Option | For | Against |
| --- | --- | --- |
| A. Beside an ACK controller, with its identity (as Scheduler is beside EventBridge) | no new identity; the operator's model as is | one process serves many services: which controller? That controller's role then needs every Cloud Control type's permissions |
| B. **Its own manager per service, installed by the operator from configuration**: ServiceAccount `ack-extensions-cloudcontrol-<service>`, role from configuration (IRSA annotation, or an EKS Pod Identity association made outside), region and endpoint flags, in ACK's namespace so CARM's ConfigMaps are shared, serving only that service's kinds (`--reconcile-resources`) | least privilege per service (the generated policy, merged per service); matches ACK's one controller and role per service and question 5; CARM per namespace works as for ACK, since target roles trust the base role | one IAM role per service to create, as for ACK; the operator needs a small configuration of its own |
| C. One manager for every Cloud Control type, one role | simplest install | one role with every type's permissions (60 foundation types carry sensitive actions); one failure stops every type |
| D. A static bundle (kustomize or Helm) outside the operator | no operator change | repeats region, CARM and flags by hand; the drift the operator exists to avoid (plan §7) |

**Recommendation: B.** The operator reads a configuration (a ConfigMap
`ack-extensions-cloudcontrol` in the operator's namespace, or a field on
an operator configuration kind later) listing, per service: the role ARN
(or `podIdentity: true`), the region, and the kinds to serve; it installs
those CRDs only, a Deployment of the one image with
`--reconcile-resources` for those kinds, its ServiceAccount, and a
ClusterRoleBinding to the generated ClusterRole. When ACK's controllers are
installed in a namespace, it uses that namespace and copies the endpoint,
CARM and feature-gate flags from any of them, so a cluster configured for
ACK needs only the role. The generated `manager-policy.json` is merged per
service for each role. If ACK later ships a controller for the service, the
kinds retire into it (section 9) and the manager goes.

## 11. What did not work, or is not known

- **Write-only to Secret is not right by default.** Of the 35 foundation-gap
  types with a write-only string, many are not secrets
  (`AWS::EC2::VPCCidrBlock`'s IPAM parameters,
  `AWS::SecretsManager::RotationSchedule`'s eleven, `GuardDuty::Member`'s
  email). Make it opt-in per path in `types.yaml`, with the default a plain
  write-only field.
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

## 12. Recommendation, phased plan, and the builder's decisions

**Recommendation: go, with limits.** The generic route works for every
mechanism the spike set out to prove, with no per-type Go, on ACK's
runtime, and reaches 251 of the 276 foundation gaps on paper. The limits:

1. it is proven on a fake only; nothing is shipped to users until one run
   per type on a real account has checked the read model against what was
   sent (section 11);
2. use it for types whose CloudFormation model is what a platform team
   would write and that need no reference or exclusive/non-exclusive choice;
   types that do (attachments, memberships, anything ACK's kinds reference)
   stay on ACK's generator here, as now;
3. Upjet is not needed for any foundation type: the 25 Cloud Control
   cannot serve are WAF Classic and types ACK covers inline or that are
   built here. Keep it as the fallback the builder named, for a type Cloud
   Control serves badly on real AWS.

**Phased plan, if go.**

1. Merge this spike as experimental: not installed by the operator, not in
   the coverage numbers.
2. One real-AWS run per served type (needs a sandbox account from the
   builder), recording responses with go-vcr; quirks found go into
   `types.yaml` as data (a secret path, a server-normalised field, a frozen
   schema), never as per-type Go.
3. The overlay: secrets opt-in, renames, references (from `relationshipRef`
   where a schema has it, otherwise overlay data; ACK's kinds first:
   `transitGatewayRef` to ACK's `TransitGateway`), removal of fields the
   spec drops.
4. Install: option B of section 10 in `pkg/operator`, per service, with
   selective CRD installation; the coverage dashboard counts Cloud Control
   kinds as ours.
5. The foundation queue in batches by service (Identity Center, GuardDuty,
   Security Hub, Config, Network Manager, Control Tower), each with e2e on
   moto where moto has the service, and the in-memory backend plus
   recordings where it does not.
6. Currency: the factory's watcher diffs the schemas' behaviour keys; a
   type whose new schema breaks generation is frozen at its last good
   schema, as awscc's bigdiffer does.

**Decisions for the builder.**

1. Go, go with limits (recommended), or fall back to Upjet.
2. The API group: `<service>.cloudcontrol.aws.hfl.dev` (the spike's: the
   engine is visible and cannot clash with an ACK-generated kind here), or
   `<service>.aws.hfl.dev` (one group per service whatever the engine, with
   a rule that a type is served by one engine only).
3. Identity and install: option B of section 10 (plan §9, question 12).
4. Write-only properties: plain by default with Secret references opted in
   per path (recommended), or Secret references for every write-only string.
5. A sandbox AWS account for the real-AWS runs of phase 2, or the route
   stays experimental.
