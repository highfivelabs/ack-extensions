# AWS Emulator as a test backend

A spike, written 2026-11-11: can AWS Emulator
([github.com/aws/aws-emulator](https://github.com/aws/aws-emulator), CC0,
Rust; 438 service crates generated from Smithy models, Cloud Control among
them) improve how this repository tests its kinds? Today there are two
fakes (`docs/testing.md`): moto, for the kinds proven on it, and our schema
fake, for the Cloud Control types moto lacks (`TestEveryKindOnSchemaFake`).

Measured: release **v1.0.322** (commit `7753983`), the Debian package,
telemetry off, on an offload Spot VM (c3d-standard-16). No AWS credentials
and no request to AWS: the emulator and everything in front of it dial
loopback only.

## How it plugs in

Opt-in, beside the two fakes; nothing in `task check` changes.

| | |
| --- | --- |
| Switch | `ACK_TEST_BACKEND=aws-emulator` (default `moto`). Read by both harnesses: `cloudcontrol/test/e2e` (the Cloud Control kinds) and `pkg/test/harness` (the ACK-generated kinds) |
| Install | `hack/aws-emulator.sh`: the pinned Debian package (`AWS_EMULATOR_VERSION`, default 1.0.322), its signature checked against the publisher's key fingerprint first |
| Run | `task test:aws-emulator [RUN=...] [PKGS=...] [REPORT=...]`; the default runs `TestEveryKindOnAWSEmulator` and writes the per-type report |
| Wiring | `pkg/test/awsemulator` starts `aws-emulator --port 0 --telemetry off` with `EMULATOR_TELEMETRY=off`, the dead proxy and a home of its own, and puts a loopback proxy in front: the manager and the tests keep the one plain endpoint and the fake keys `pkg/fakeaws` gives them; the proxy records the Cloud Control calls (as the fake and the live recorder do), signs each request again with the run's emulator keys and forwards it over HTTPS, trusting only the emulator's certificate. `hack/noaws` passes unchanged |
| The survey | `TestEveryKindOnAWSEmulator`: every Cloud Control kind (all 292, the moto-proven ones too), the schema fake test's example object, then create, read, list, update and delete through the manager, plus probes of refusals AWS makes. A survey, not a gate: a step's failure is a row in the CSV (`ACK_EMULATOR_REPORT`); `ACK_EMULATOR_EXPECT=<csv>` makes any change of outcome a failure, for a pinned release |

The account is `000000000000` on the emulator, `123456789012` on moto.

## Cloud Control: every type

`TestEveryKindOnAWSEmulator`, 292 types, the whole report in
[`aws-emulator-1.0.322.csv`](aws-emulator-1.0.322.csv).

| Outcome | Types | What |
| --- | ---: | --- |
| works | 257 | create, read (the spec's values back), list, update (one mutable field, seen in `GetResource`) and delete all pass |
| partial | 16 | every one fails the update only, with `NotUpdatable` on a field AWS lets you change (below) |
| unsupported | 19 | 18 `TypeNotFoundException`: the emulator's registry has no SecurityLake, DirectConnect, IdentityStore, Route53Profiles, RolesAnywhere, APS or S3Files; 1 refused by the emulator's native Logs provider (`AWS::Logs::Destination`: "Tags is not a property", though the type's registry schema has `Tags`) |

Per service:

| Service | Types | Works | Partial | Unsupported | Identifier as on AWS | Empty desired state accepted | Read-only values filled |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| EC2 | 71 | 71 | 0 | 0 | 16 | 61 | 23 of 236 |
| SecurityHub | 17 | 16 | 1 | 0 | 1 | 15 | 0 of 56 |
| CloudWatch | 16 | 11 | 5 | 0 | 0 | 15 | 0 of 76 |
| CloudFront | 12 | 12 | 0 | 0 | 1 | 12 | 0 of 35 |
| Config | 11 | 11 | 0 | 0 | 3 | 10 | 1 of 13 |
| GuardDuty | 11 | 11 | 0 | 0 | 1 | 11 | 1 of 26 |
| Logs | 10 | 8 | 1 | 1 | 1 | 8 | 0 of 14 |
| ECS | 7 | 7 | 0 | 0 | 0 | 5 | 0 of 22 |
| Lambda | 7 | 7 | 0 | 0 | 0 | 7 | 0 of 34 |
| RDS | 7 | 7 | 0 | 0 | 1 | 7 | 0 of 34 |
| Route53 | 7 | 7 | 0 | 0 | 0 | 6 | 0 of 16 |
| S3 | 7 | 7 | 0 | 0 | 3 | 6 | 3 of 11 |
| ApiGatewayV2 | 6 | 5 | 1 | 0 | 0 | 6 | 4 of 9 |
| Route53Resolver | 6 | 6 | 0 | 0 | 0 | 3 | 0 of 36 |
| SSM | 6 | 5 | 1 | 0 | 0 | 6 | 2 of 20 |
| DMS | 5 | 5 | 0 | 0 | 0 | 3 | 0 of 11 |
| FMS | 5 | 3 | 2 | 0 | 0 | 5 | 0 of 11 |
| InspectorV2 | 5 | 4 | 1 | 0 | 0 | 3 | 0 of 15 |
| Macie | 5 | 3 | 2 | 0 | 0 | 4 | 0 of 14 |
| SSO | 5 | 4 | 1 | 0 | 0 | 5 | 0 of 3 |
| ECR | 4 | 4 | 0 | 0 | 0 | 4 | 0 of 3 |
| EventSchemas | 4 | 4 | 0 | 0 | 0 | 3 | 1 of 9 |
| NetworkFirewall | 4 | 4 | 0 | 0 | 0 | 4 | 1 of 8 |
| RAM | 4 | 4 | 0 | 0 | 0 | 4 | 0 of 20 |
| SecurityLake | 4 | 0 | 0 | 4 | 0 | 0 | 0 of 0 |
| CodeDeploy | 3 | 3 | 0 | 0 | 0 | 1 | 0 of 0 |
| ControlTower | 3 | 3 | 0 | 0 | 0 | 3 | 0 of 6 |
| Detective | 3 | 3 | 0 | 0 | 0 | 2 | 0 of 2 |
| IdentityStore | 3 | 0 | 0 | 3 | 0 | 0 | 0 of 0 |
| MSK | 3 | 3 | 0 | 0 | 0 | 3 | 0 of 2 |
| RolesAnywhere | 3 | 0 | 0 | 3 | 0 | 0 | 0 of 0 |
| Route53Profiles | 3 | 0 | 0 | 3 | 0 | 0 | 0 of 0 |
| APS | 2 | 0 | 0 | 2 | 0 | 0 | 0 of 0 |
| AccessAnalyzer | 2 | 2 | 0 | 0 | 0 | 2 | 0 of 4 |
| AuditManager | 2 | 1 | 1 | 0 | 1 | 1 | 1 of 10 |
| AutoScaling | 2 | 2 | 0 | 0 | 0 | 2 | 0 of 0 |
| CloudTrail | 2 | 2 | 0 | 0 | 0 | 0 | 0 of 6 |
| DirectConnect | 2 | 0 | 0 | 2 | 0 | 0 | 0 of 0 |
| ElasticLoadBalancingV2 | 2 | 2 | 0 | 0 | 0 | 0 | 0 of 5 |
| IAM | 2 | 2 | 0 | 0 | 0 | 0 | 0 of 3 |
| SecretsManager | 2 | 2 | 0 | 0 | 0 | 2 | 0 of 2 |
| WAFv2 | 2 | 2 | 0 | 0 | 0 | 2 | 0 of 3 |
| ACMPCA | 1 | 1 | 0 | 0 | 0 | 1 | 0 of 0 |
| CertificateManager | 1 | 1 | 0 | 0 | 1 | 1 | 1 of 1 |
| EKS | 1 | 1 | 0 | 0 | 0 | 1 | 0 of 11 |
| S3Files | 1 | 0 | 0 | 1 | 0 | 0 | 0 of 0 |
| StepFunctions | 1 | 1 | 0 | 0 | 0 | 1 | 0 of 1 |
| **all** | **292** | **257** | **16** | **19** | **29** | **235** | **38 of 788** |

"Identifier as on AWS": the identifier equals the type's
`primaryIdentifier` values joined by `|`, as Cloud Control gives it.
"Empty desired state accepted": `CreateResource` with `{}` for a type that
has required properties reached `SUCCESS`. "Read-only values filled": of the
type's top-level read-only properties (ARNs, server-assigned ids, states),
how many `GetResource` returned.

### Why "works" means less than it says

257 "works" says less than it seems. The emulator's Cloud Control
(`services/service-com-amazonaws-cloudcontrol-cloudapiservice/src/state.rs`)
is a generic store, like our schema fake but without the registry schema:

- **Three types are wired to their service.** `AWS::Logs::LogGroup`,
  `AWS::Logs::Destination` and `AWS::SQS::Queue` create real resources in the
  emulator's Logs and SQS. Every other type is kept in Cloud Control's own
  store: a `GuardDuty::Detector` made through Cloud Control is not a detector
  GuardDuty's API can see. Of our 292 types, one (`Logs::Destination`) is
  wired, and it fails.
- **No schema.** A desired state is not validated: 235 of 253 types with
  required properties accepted `{}` (the 18 others were the unregistered
  types and `Logs::Destination`). Upstream's own issue
  `20261102T095426Z-cloudcontrol-9-net-new-causes-desiredstate-patchdocument-are`
  lists enum, pattern, type, range, unknown-property and create-only checks
  as missing.
- **Identifiers are guessed.** The identifier is the first property named
  `<Resource>Name`, `<Resource>Id`, `<Resource>Arn`, `Name`, `Id` or `Arn`,
  else a random UUID recorded under `<Resource>Id`. Only 29 of 273 created
  types got AWS's identifier. Composite identifiers (`SSO::Assignment`'s six
  parts, `GuardDuty::Filter`'s `DetectorId|Name`, the transit gateway route
  and association kinds) come back as a UUID or as one part; server-assigned
  ids land under a property the schema does not have (`DetectorId`, not `Id`).
- **Create-only is guessed too.** A patch to a create-only property was
  accepted for 88 of 103 types tried; the 15 refusals were all the property
  the emulator had taken as the identifier. The other way round, 16 types
  refuse an update of `Name` that AWS allows, because the emulator took
  `Name` for the identifier (the 16 "partial" rows).
- **No read-only values.** 38 of 788 read-only properties came back, each
  the UUID the emulator minted. No ARN, state or timestamp.

The schema fake does each of these from the registry schema: required,
read-only, create-only and write-only properties, the primary identifier
(composite ones included), asynchronous requests. So for the 265 kinds on
the schema fake, the emulator would test less, not more.

Upstream agrees. The release's fidelity scores (`scores.json.gz`, checks
passed of checks run) give Cloud Control 75 of 190 (39%), and
`issues/things-that-do-not-work/` has 26 open Cloud Control issues
(identifiers, validation, tags never written to the owning service,
`CancelResourceRequest` and `ListResourceRequests` stubbed). For the
services behind our kinds, the scores of their own APIs (what the ACK
kinds meet):

| Service API | Passed | | Service API | Passed |
| --- | ---: | --- | --- | ---: |
| EC2 | 39% (958 of 2,468) | | IAM | 58% |
| CloudWatch | 60% | | RDS | 41% |
| CloudWatch Logs | 58% | | Lambda | 52% |
| Config | 67% | | KMS | 72% |
| GuardDuty | 76% | | ECR | 61% |
| Security Hub | 70% | | EventBridge | 72% |
| SSO Admin | 85% | | Scheduler | 51% |
| Identity Store | 88% | | Application Auto Scaling | 77% |
| Secrets Manager | 68% | | Auto Scaling | 20% |
| WAFv2 | 73% | | Organizations | not scored |

The ACK-kind results below follow these: the failures are in EC2, RDS,
IAM, Organizations, Scheduler and Auto Scaling.

## The moto-proven kinds

The 13 tests of the Cloud Control kinds proven on moto, unchanged, with
`ACK_TEST_BACKEND=aws-emulator` (the test reads the service's own API where
it read moto's; Control Tower's `TestLandingZone` skips, its double is ours):

| | On moto (fake Cloud Control over moto) | On the emulator |
| --- | --- | --- |
| Pass | 13 of 13 | **0 of 13** |
| Time | 829 s | 151 s (each fails within 8 to 19 s) |

Every failure is the same cause: the emulator's Cloud Control does not
reach the service. The object syncs, but the status has no ARN or id
(`TestSAMLProvider`, `TestSecurityHub`, `TestSecretResourcePolicy`,
`TestDNS`, `TestDetector`, `TestGuardDutyFilter`, `TestECRRegistry`,
`TestIdentityCenter`, `TestNetworkInterface`), the identifier is a UUID
where AWS gives `tgw-rtb-...|tgw-attach-...` (`TestTransitGatewayRouting`,
`TestTransitGatewayRouteTable`), the Config recorder never records because
nothing reached Config (`TestConfig`), and `AWS::IdentityStore::Group` is
an unknown type (`TestIdentityStore`). Our moto backends translate each
type into the service's API; that translation is exactly what the emulator
does not have.

## The ACK-generated kinds

The 32 tests in `tests/e2e`, unchanged, with the emulator in moto's place
(`pkg/test/harness` serves both): 32 of 32 pass on moto in 804 s; **9 of
32** on the emulator in 2,686 s (failures wait out their timeouts). Here
the emulator talks to the services' own APIs, its strength, so the
failures say more:

| Kind of failure | Tests | What |
| --- | --- | --- |
| **A bug of ours the emulator caught** | `TestOptionGroup` | Our `rds.aws.hfl.dev` `OptionGroup` reads with `DescribeOptionGroups` naming `OptionGroupName` together with `EngineName` and `MajorEngineVersion` (`services/rds/pkg/resource/option_group/sdk.go`, `newListRequestPayload`). AWS's API reference: OptionGroupName "can't be supplied together with EngineName or MajorEngineVersion". Moto answers; the emulator refuses, as AWS should. Worth a live check and a fix in its own pull request |
| Fixtures moto lets through and AWS would not | `TestApplicationScheduledAction` (no scalable target registered), `TestScalingPolicy`, `TestScheduledAction` (launch configurations, which AWS no longer offers new accounts), `TestSSHPublicKey` (a key body that is not a key), `TestSchedule` (a role Scheduler cannot assume), `TestVolumeAttachment` (moto's own AMI id) | The emulator is closer to AWS than moto here; the tests would need real-looking fixtures to run on it |
| The emulator's account, not moto's | `TestReplicationConfiguration`, `TestIAMVirtualMFADevice` | The tests expect `123456789012` |
| The emulator's account starts in an organization | `TestOrganizationsOrganization`, `...Policy`, `...PolicyAttachment`, `...DelegatedAdministrator`, `...AccountPlacement` | `CreateOrganization` refuses (moto starts with none); and `ListAWSServiceAccessForOrganization` is not implemented (501) |
| Not implemented, or different, in the emulator | `TestVPNConnection` (`CreateVpnConnection` 501), `TestAccountPasswordPolicy` (`GetAccountPasswordPolicy` unknown), `TestDBProxyTarget` (targets never read back), `TestConnection` (no connection secret in Secrets Manager), `TestReplicaKey` (one Region per emulator), `TestVpnGateway` (stays `deleting`), `TestScheduleGroup` (still there after the delete), `TestDBRoleAssociation` (the removal ends at once), `TestAccountAlias` (a second `CreateAccountAlias` refused; which side matches AWS is unchecked) | Gaps, or timing the emulator does not model |

## The operator end to end

Not run. `task operator-e2e` runs moto as a pinned image in the kind
cluster (`config/fake-aws/moto.yaml`). The emulator publishes no image
(its docs build one from a Dockerfile), serves only HTTPS with a
certificate made at start, and wants requests signed with keys made at
start: in the cluster that means an image of our own, the certificate in
every manager's trust store and the re-signing proxy as a Service. Not
cheap, and the survey above already shows what the managers would meet.

## Cost and speed

All on one c3d-standard-16 Spot VM.

| | AWS Emulator 1.0.322 | moto (pinned) | schema fake |
| --- | --- | --- | --- |
| Install | a 171 MB Debian package, signature checked, a few seconds; Linux only (a container elsewhere) | a Python venv (`hack/moto.sh`) | none (Go, in the test binary) |
| Start to healthy | 145 ms (three runs: 145 to 151) | 295 to 313 ms | none |
| Memory | 285 MiB idle; 321 MiB peak over the 292-kind survey | 51 MiB idle | in the test process |
| Every Cloud Control kind once | `TestEveryKindOnAWSEmulator`: 292 kinds, five steps and two probes each, 49 s | | `TestEveryKindOnSchemaFake`: 265 kinds, 42 s |
| The 13 moto-proven Cloud Control tests | 151 s, 0 pass | 829 s, 13 pass | |
| The 32 ACK-kind tests | 2,686 s, 9 pass | 804 s, 32 pass | |

Speed is not the question: both fakes are fast enough, and the end-to-end
time is envtest, the manager and the tests' own waits.

## Gaps that would hide a real bug

What a test on the emulator would pass that AWS fails:

1. **Cloud Control without the service.** A Cloud Control kind can sync
   and be ready while nothing exists in the service's own API. A broken
   mapping to the service would pass. (Our moto backends exist to catch
   exactly that.)
2. **No schema validation.** A missing required property, an unknown
   property, a value outside its enum, pattern or range: all accepted (235
   of 253 types took `{}`). A CRD that lost a required marker would pass.
3. **Guessed identifiers.** A kind that builds or parses its composite
   identifier wrongly would pass, since the emulator's identifier is a UUID
   or one part; a status that should carry the ARN or id is left empty and
   a test that does not check it would pass.
4. **Create-only not enforced** (88 of 103 accepted), so a kind that
   updates in place what AWS can only replace would pass; and the
   opposite, 16 false `NotUpdatable` refusals, would fail a correct kind.
5. **One Region per emulator**, so cross-Region kinds (KMS replica keys,
   ECR replication) need two emulators and a routing proxy.

Where the emulator is stricter than moto and closer to AWS (the six fixture
failures and `OptionGroup` above), it found things moto hides.

There is nothing live to compare with yet: the one row in
`docs/live-runs.md`'s ledger stopped at the sweep, before any test.

## Recommendation

**Not yet for Cloud Control; yes, opt-in, for the ACK-generated kinds.**

- **Do not replace the schema fake.** For Cloud Control the emulator is a
  weaker version of it: a generic store with no registry schema, guessed
  identifiers and no read-only values. The schema fake checks more of
  what our side does.
- **Do not replace moto for the Cloud Control kinds.** 0 of 13 pass: the
  emulator's Cloud Control reaches its services for three types, none of
  them ours (`Logs::Destination` is wired and fails). The moto backends are
  the part that proves the service; the emulator has nothing in their
  place.
- **Keep it as an opt-in third backend** (this pull request), for the
  ACK-generated kinds. Against the services' own APIs it is stricter than
  moto in places AWS is strict, and it found a bug moto hides
  (`OptionGroup`). Run it by hand, or nightly, and read the failures; do
  not gate on it.
- **Look again** when upstream wires Cloud Control to the services and
  validates against the registry schema (issues
  `20261102T095426Z-cloudcontrol-9-net-new-causes-desiredstate-patchdocument-are`,
  `20261103T024056Z-cloudcontrolapi-resource-identifiers-and-deleteresource-acceptance-diverge-from`,
  `20261104T172234Z-cloudcontrol-desiredstate-tags-are-never-written-to-the`).
  `TestEveryKindOnAWSEmulator` measures that in a minute: run it on the
  new release and compare with `ACK_EMULATOR_EXPECT=docs/aws-emulator-1.0.322.csv`.

Follow-ups, each its own pull request: fix the `OptionGroup` read (and
check it on a live run); make the six moto-shaped fixtures real (a
scalable target, a launch template, a real key, a trusted role).

## In `task ci`, if adopted

Nothing changes in `task check` or the merge gate. If the builder wants
the emulator in CI, the cheapest shape:

- The nightly run (`nightly.yml`'s job, run on an offload VM while Actions
  is off), and `task ci` only when asked: after `task check`, run `task test:aws-emulator` (the survey; about a minute) and
  `task test:aws-emulator PKGS=./tests/e2e/ RUN=.` (the ACK kinds; up to
  45 minutes while tests fail on timeouts). The VM image would carry the
  pinned package, installed by `hack/aws-emulator.sh`.
- Gate only on change: the survey with `ACK_EMULATOR_EXPECT=` the committed
  CSV fails when an outcome moves, so a new release that wires a service or
  validates a schema shows up as a diff to review, and the pin moves with
  it. The ACK-kind tests would need an expected-failures list in the same
  spirit before any of them can gate.
- Telemetry stays off (`EMULATOR_TELEMETRY=off`, `--telemetry off`, and the
  dead proxy in its environment).
