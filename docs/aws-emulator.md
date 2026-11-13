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

**`EIPAssociation`, 2026-11-12, release 1.0.338.** `TestEIPAssociation`,
proven on moto the same day, fails on the emulator for the same cause
(offload run `off-ack-extensions-1113024313-28788`): the manager resolves
`allocationRef` and `instanceRef` and sends `CreateResource` with moto's
shape of IDs (`{"AllocationId":"eipalloc-...","InstanceId":"i-..."}`),
the emulator answers `SUCCESS` with a UUID identifier, `GetResource`
gives no `Id`, and `DescribeAddresses` shows the address on no instance.
`SecurityGroupVpcAssociation` and `VPCBlockPublicAccessExclusion` have no
moto backend (moto 5.2.5 lacks the operations); their reference tests
run on the schema fake only, and the survey above (`works`, the
association's identifier a UUID where AWS gives `GroupId|VpcId`) is all
the emulator says of them.

## The ACK-generated kinds

The 32 tests in `tests/e2e`, unchanged, with the emulator in moto's place
(`pkg/test/harness` serves both): 32 of 32 pass on moto in 804 s; **9 of
32** on the emulator in 2,686 s (failures wait out their timeouts). Here
the emulator talks to the services' own APIs, its strength, so the
failures say more:

| Kind of failure | Tests | What |
| --- | --- | --- |
| **A bug of ours the emulator caught** | `TestOptionGroup` | Our `rds.aws.hfl.dev` `OptionGroup` reads with `DescribeOptionGroups` naming `OptionGroupName` together with `EngineName` and `MajorEngineVersion` (`services/rds/pkg/resource/option_group/sdk.go`, `newListRequestPayload`). AWS's API reference: OptionGroupName "can't be supplied together with EngineName or MajorEngineVersion". Moto answers; the emulator refuses (`ValidationException`), as AWS should. **Fixed** (2026-11-11, `docs/plan.md` section 6.2): the read names the group only; `TestOptionGroup` fails on the emulator before the fix (offload run `off-ack-extensions-1112023209-20205`) and passes after (`off-ack-extensions-1112023210-13362`, 47 s). Not yet checked on a live run |
| Fixtures moto lets through and AWS would not | `TestApplicationScheduledAction` (no scalable target registered), `TestScalingPolicy`, `TestScheduledAction` (launch configurations, which AWS no longer offers new accounts), `TestSSHPublicKey` (a key body that is not a key), `TestSchedule` (a role Scheduler cannot assume), `TestVolumeAttachment` (moto's own AMI id) | The emulator is closer to AWS than moto here; the tests would need real-looking fixtures to run on it |
| The emulator's account, not moto's | `TestReplicationConfiguration`, `TestIAMVirtualMFADevice` | The tests expect `123456789012` |
| The emulator's account starts in an organization | `TestOrganizationsOrganization`, `...Policy`, `...PolicyAttachment`, `...DelegatedAdministrator`, `...AccountPlacement` | `CreateOrganization` refuses (moto starts with none); and `ListAWSServiceAccessForOrganization` is not implemented (501) |
| Not implemented, or different, in the emulator | `TestVPNConnection` (`CreateVpnConnection` 501), `TestAccountPasswordPolicy` (`GetAccountPasswordPolicy` unknown), `TestDBProxyTarget` (targets never read back), `TestConnection` (no connection secret in Secrets Manager), `TestReplicaKey` (one Region per emulator), `TestVpnGateway` (stays `deleting`), `TestScheduleGroup` (still there after the delete), `TestDBRoleAssociation` (the removal ends at once), `TestAccountAlias` (a second `CreateAccountAlias` refused; which side matches AWS is unchecked) | Gaps, or timing the emulator does not model |

### Second pass, 2026-11-12

Every failure above was read against the AWS API reference and sorted
three ways: our request breaks the documented contract (fix the kind,
with a test that fails first), the test asks for what AWS would not do
(fix the test), or the emulator differs from AWS (written down here).
After it: **18 of 32** on the emulator (17 in the full run, 2,800 s;
`TestVolumeAttachment`'s wait, added after it, passes on its own run),
still **32 of 32** on moto (837 s). Offload runs: before
`off-ack-extensions-1112085953-22656`, after
`off-ack-extensions-1112094927-21489` and
`off-ack-extensions-1112105532-21257` (`TestVolumeAttachment` on both
backends, and the survey below).

**A bug of ours.**

- **`iam.aws.hfl.dev` `AccountAlias`: a new object for the alias the
  account already has went terminal.** The read finds nothing for an
  object that has not set its alias yet, so the create calls
  `CreateAccountAlias` with the account's own alias; the emulator
  answers `EntityAlreadyExists` ("The account alias acme-prod already
  exists"), an error the
  [API reference](https://docs.aws.amazon.com/IAM/latest/APIReference/API_CreateAccountAlias.html)
  lists for the call, and the object stopped there (`TestAccountAlias`,
  "delete deletes the alias", after "deletion-policy retain leaves the
  alias" left it). Moto accepts the call. **Fixed** (`docs/plan.md`
  section 6.21): on `EntityAlreadyExists` the create reads the account's
  alias and takes it over when it is the one asked for; another
  account's alias stays terminal. `TestCreateAdoptsTheAccountsOwnAlias`
  (`services/iam/pkg/resource/account_alias/create_test.go`) failed
  before the fix with the emulator's error and passes after;
  `TestAccountAlias` passes on the emulator. What real AWS answers for
  the account's own alias is not yet checked live; the fix is right
  either way. A new alias replacing the old one (the drift step) works
  on both, as
  [AWS documents](https://docs.aws.amazon.com/powershell/v5/userguide/powershell_iam_code_examples.html)
  ("changes the account alias").

**Tests that asked for what AWS would not do** (fixed; each still passes
on moto):

| Test | What AWS does | The fix |
| --- | --- | --- |
| `TestApplicationScheduledAction` | `PutScheduledAction` needs a registered scalable target (`ObjectNotFoundException` on the emulator) | the fixture makes the ECS cluster, task definition and service, and registers the service's desired count (adds `aws-sdk-go-v2/service/ecs` at v1.78.0, the release on the pinned SDK core, for the test only) |
| `TestScalingPolicy`, `TestScheduledAction` | new accounts cannot make launch configurations (`UnsupportedOperation`) | the group's fixture uses a launch template |
| `TestSSHPublicKey` | `UploadSSHPublicKey` parses the key (`InvalidPublicKey`) | two real RSA public keys, their private halves thrown away |
| `TestSchedule` | the execution role must trust `scheduler.amazonaws.com` (`ValidationException`) | the test makes the role with that trust policy |
| `TestVolumeAttachment` | `ami-12c6146b` is moto's image (the emulator: `InvalidAMIID.Malformed`) | the image is one `DescribeImages` returns |
| `TestReplicationConfiguration`, `TestIAMVirtualMFADevice` | the account is the caller's | `harness.Env.Account`: moto's `123456789012` or the emulator's `000000000000` |
| `TestVpnGateway`, `TestVolumeAttachment`, `TestConnection`, `TestScheduleGroup` | the delete is asynchronous: [`DeleteVpnGateway`](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DeleteVpnGateway.html) and [`DetachVolume`](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DetachVolume.html) pass through `deleting` and `detaching`, [`DeleteConnection`](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_DeleteConnection.html) through `DELETING`, and a [schedule group](https://docs.aws.amazon.com/scheduler/latest/APIReference/API_DeleteScheduleGroup.html) "remains in a `DELETING` state" and the call "is eventually consistent" | the test waits up to a minute for the end state instead of reading it once; moto gets there at once |
| `TestDBRoleAssociation` | `RemoveRoleFromDBCluster` removes the role | on the emulator, which implements the call, the test checks the role leaves the cluster and the object goes; on moto (no such call, section 4.4 of `docs/plan.md`) it still checks the object is kept while the call fails |

**The emulator differs from AWS** (left failing; nothing of ours to fix):

| Test | What the emulator does | AWS |
| --- | --- | --- |
| `TestDBProxyTarget` | `DescribeDBProxyTargets` gives the database's resource id (`db-...`) in `RdsResourceId` (`services/service-com-amazonaws-rds-amazonrdsv19/src/state/db_proxies.rs`, `target_to_wire`), so our read never finds the target it registered and every retry meets `DBProxyTargetAlreadyRegisteredFault` | `RdsResourceId` is "the instance identifier for an RDS DB instance, or the cluster identifier for an Aurora DB cluster" ([API reference](https://docs.aws.amazon.com/AmazonRDS/latest/APIReference/API_DBProxyTarget.html); the [CLI example](https://docs.aws.amazon.com/cli/latest/reference/rds/describe-db-proxy-targets.html) shows `database1-instance-1`), which is what our read matches |
| `TestScalingPolicy`, `TestScheduledAction` | `DescribePolicies` and `DescribeScheduledActions` answer 501 | implemented |
| `TestSSHPublicKey` | the upload works now; `GetSSHPublicKey` and `UpdateSSHPublicKey` answer 501 | implemented |
| `TestIAMVirtualMFADevice` | `DeleteVirtualMFADevice` answers 501 (the rest passes) | implemented |
| `TestAccountPasswordPolicy` | `GetAccountPasswordPolicy` is an unknown action | implemented |
| `TestVPNConnection` | `CreateVpnConnection` answers 501 | implemented |
| `TestConnection` | `CreateConnection` makes no Secrets Manager secret for the credentials (the delete step passes now) | EventBridge stores them in a secret it manages ([`CreateConnection`](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_CreateConnection.html), `SecretArn`) |
| `TestReplicaKey` | one Region per emulator: the replica in `eu-west-1` is never there | a replica key lives in another Region |
| `TestOrganizationsOrganization` | `ListAWSServiceAccessForOrganization` answers 501, so the adopted organization is never read whole; and the account starts in an organization, so the subtest that makes one from none cannot run | implemented; a new account is in none |
| `TestOrganizationsPolicy`, `...PolicyAttachment` | `CreatePolicy` answers 501 ("scaffolded but not implemented") | implemented |
| `TestOrganizationsDelegatedAdministrator`, `...AccountPlacement` | `CreateAccount` answers 501, so the fixture's member accounts cannot be made | implemented |

**Third pass, 2026-11-12: the organization fixture.** The four
Organizations tests that need an organization made one with
`CreateOrganization` and stopped at `AlreadyInOrganizationException` on
the emulator, whose account starts in one. A sandbox account that is
already a management account answers the same, so the fixture
(`managementOrganization`, `tests/e2e/organizations_test.go`) now takes
the account's own organization when it is in one with all features, and
makes one otherwise (moto). On the emulator the tests then reach the
operations it lacks, named in the table above: `CreatePolicy` and
`CreateAccount` (501), as `Organization` meets
`ListAWSServiceAccessForOrganization`. Still 18 of 32; the five fail on
the emulator's gaps, not on the fixture (offload run
`off-ack-extensions-1112152719-29035`).

The Cloud Control survey on the same release is unchanged:
`TestEveryKindOnAWSEmulator` with `ACK_EMULATOR_EXPECT=docs/aws-emulator-1.0.322.csv`
gives 257 works, 16 partial, 19 unsupported.

**`s3control.aws.hfl.dev` `PublicAccessBlock`, 2026-11-12.** A new kind
written after the builder's direction that day (adopt the emulator as
the primary fake, moto as the fallback), so its test was run on both
from the start: `TestPublicAccessBlock` passes on the emulator, 37 s
(offload run `off-ack-extensions-1112234526-327`), as on moto, 39 s
(`off-ack-extensions-1112233804-10864`), unchanged (the account is
`harness.Env.Account`). Both needed the same thing: S3 Control's
endpoint rules put the account ID in front of a custom endpoint's host
(`000000000000.127.0.0.1`), so the manager and the test keep the fake's
own host (`services/s3control/pkg/endpoint`; `docs/plan.md` section 4.4)
and the account travels in `x-amz-account-id`. The emulator (behind our
re-signing proxy, signing name `s3`) routes `/v20180820/` to S3 Control
and answers put, get (all four settings), delete and
`NoSuchPublicAccessBlockConfiguration` as moto does. Its own issue list
says account-level settings are not enforced on S3 calls
(`20261102T104655Z-s3control-account-level-publicaccessblock-operations-never-deny-the`,
`20261024T210022Z-s3control-account-level-blockpublicpolicy-not-enforced-on-putbucketpolicy`);
the kind never relies on that. Run on 1.0.322 only: with it, that release
passes 19 of 33 ACK-kind tests.

**Fourth pass, 2026-11-12: release 1.0.338.** The builder's direction
the same day: adopt AWS Emulator as the primary fake, moto the fallback
where the emulator lacks an operation. The newest release (1.0.338,
published 2026-11-12; the Debian package as for 1.0.322,
`AWS_EMULATOR_VERSION=1.0.338 hack/aws-emulator.sh`) on one Spot VM
(offload run `off-ack-extensions-1112224936-7200`):

- **The Cloud Control survey**, now 1,343 kinds (the types added since
  1.0.322's run): 899 work, 77 partial, 367 unsupported
  ([`aws-emulator-1.0.338.csv`](aws-emulator-1.0.338.csv)). On the 292
  kinds of 1.0.322's run nothing moved: 257, 16, 19, the same rows.
- **The ACK-generated kinds**, 34 tests now (`TestResourceTag` and
  `TestSlotMove` added since): **20 pass, 14 fail.** `TestDBProxyTarget`
  passes: the emulator now gives the database's identifier in
  `RdsResourceId`, as AWS does. `TestResourceTag` is new and fails
  (`DescribeTags`). Every other failure is one of 1.0.322's, and every one
  is the emulator's: nothing of ours to fix.

### The 14 failures: candidate upstream contributions

For the builder's approval: this repository files nothing upstream
(CLAUDE.md, "Never"). AWS Emulator (CC0) tracks its gaps as files in its
repository, `issues/things-that-do-not-work/` (read on `main`,
2026-11-12); where one names our gap, it is given, so a contribution
would be the implementation, not a new report.

| Test | What the emulator lacks (1.0.338) | Its issue file | Contribution |
| --- | --- | --- | --- |
| `TestScalingPolicy` | Auto Scaling `DeletePolicy` (a scaffold): the object is never deleted. `DescribePolicies` now works | `20261111T072954Z-autoscaling-deletepolicy-stub`, `20261111T093414Z-autoscaling-cloudwatch-ec2-deletepolicy-answers-http-501-notimplemented` | implement `DeletePolicy` |
| `TestScheduledAction` | Auto Scaling `DescribeScheduledActions` (501) | none found | report and implement `DescribeScheduledActions` |
| `TestResourceTag` | EC2 `DescribeTags` (501) | `20261022T235108Z-ec2-describetags-stub` | implement `DescribeTags` (filters by resource ID and key) |
| `TestVPNConnection` | EC2 `CreateVpnConnection` (a stub) | `20261023T102725Z-ec2-createvpnconnection-stub` | implement `CreateVpnConnection`, `DescribeVpnConnections` with its options and tunnels |
| `TestConnection` | EventBridge `CreateConnection` makes no Secrets Manager secret for the credentials (`GetSecretValue` on its `SecretArn`: `ResourceNotFoundException`) | related, not the same: `20261029T235600Z-events-connection-lifecycle-never-authorizes-connectionstate-stays-creating` | report and implement the managed secret |
| `TestAccountPasswordPolicy` | IAM `GetAccountPasswordPolicy` still answers a policy after `DeleteAccountPasswordPolicy`: the object is never deleted | `20261111T010549Z-iam-getaccountpasswordpolicy-answers-200-with-a-policy-after` | implement the delete |
| `TestSSHPublicKey` | IAM `UpdateSSHPublicKey` (a scaffold); `GetSSHPublicKey` now works | `20261112T141524Z-iam-updatesshpublickey-stub` | implement `UpdateSSHPublicKey` |
| `TestIAMVirtualMFADevice` | IAM `DeleteVirtualMFADevice` (a scaffold) | `20261105T054223Z-iam-deletevirtualmfadevice-stub` | implement it |
| `TestReplicaKey` | the replica is made now (`ReplicateKey` works), but its key policy in `eu-west-1` never reads back as the one put (`kms_test.go:165`); not isolated further | none found | isolate (a policy put through the replica's Region), then report |
| `TestOrganizationsOrganization` | `ListAWSServiceAccessForOrganization` (a scaffold); `EnablePolicyType` and `DisablePolicyType` refuse valid input ("1 validation error"); the account starts in an organization | `20261104T063936Z-account-organizations-describeorganization-reports-an-organization-on-a-freshly` (the starting organization) | implement `ListAWSServiceAccessForOrganization`, fix the two validations |
| `TestOrganizationsPolicy`, `TestOrganizationsPolicyAttachment` | Organizations `CreatePolicy` gives no policy (the object's policy ID stays empty; 501 on 1.0.322) | none found | implement `CreatePolicy`, `UpdatePolicy`, `AttachPolicy`, `DetachPolicy` |
| `TestOrganizationsDelegatedAdministrator`, `TestOrganizationsAccountPlacement` | Organizations `CreateAccount` (a scaffold) | none found | implement `CreateAccount` and `DescribeCreateAccountStatus` |

Six of them are one operation each (`DeletePolicy`,
`DescribeScheduledActions`, `DescribeTags`, `UpdateSSHPublicKey`,
`DeleteVirtualMFADevice`, the password policy's delete); Organizations is
the largest gap (five tests). Until they land, moto stays the test of
record for these 14 (`task check`), and the emulator run is the one to
read for the 20 it passes.

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

Where the emulator is stricter than moto and closer to AWS (the fixture
failures, `OptionGroup` and `AccountAlias` above), it found things moto
hides. It also differs from AWS in places (`DBProxyTarget`'s
`RdsResourceId`, the organization it starts in), so a failure on it is
read against the API reference before anything changes.

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
  not gate on it. (Superseded 2026-11-12: CI gates on it, with the
  expected failures listed; "In CI" below.)
- **Look again** when upstream wires Cloud Control to the services and
  validates against the registry schema (issues
  `20261102T095426Z-cloudcontrol-9-net-new-causes-desiredstate-patchdocument-are`,
  `20261103T024056Z-cloudcontrolapi-resource-identifiers-and-deleteresource-acceptance-diverge-from`,
  `20261104T172234Z-cloudcontrol-desiredstate-tags-are-never-written-to-the`).
  `TestEveryKindOnAWSEmulator` measures that in a minute: run it on the
  new release and compare with `ACK_EMULATOR_EXPECT=docs/aws-emulator-1.0.322.csv`.

Follow-ups: check the `OptionGroup` read and the `AccountAlias` takeover
on a live run (both fixes are in). The moto-shaped fixtures were made
real on 2026-11-12 ("Second pass" above).

## In CI

Adopted 2026-11-12 (the builder: the emulator is the primary fake, moto
the fallback). The ACK-kind tests run on the emulator **beside** moto, not
instead of it: `task check` still runs every test on moto, and nothing
there changed.

| | |
| --- | --- |
| Run | `task test:aws-emulator:ack` (`hack/ci/emulator-ack.sh`): every test in `tests/e2e` with `ACK_TEST_BACKEND=aws-emulator`, `go test -json` |
| Pin | **1.0.338**, `hack/aws-emulator.sh`'s default (the Debian package, signature checked) |
| Expected failures | [`tests/e2e/aws-emulator-expected-failures.yaml`](../tests/e2e/aws-emulator-expected-failures.yaml): the 14 tests of "The 14 failures" above, each naming the operation the emulator lacks and linking here, on the emulator only |
| Judged by | `hack/emulator-expect` (its own unit tests in `task check`). The run fails when a test not on the list fails, skips or does not finish (a panic, the binary's timeout); when a test on the list **passes** (the emulator gained the operation: take it off the list, so the list stays honest); when a listed test does not exist; or when the installed emulator is not the list's release. go test's exit status is not the result: the expected failures make it fail |
| When | the plan's `emulator=true` (`hack/ci/select.sh`): mode full, or a group with ACK kinds or their tests (a service, `e2e:F`); not docs only, not `cloudcontrol` or `coverage` alone |
| Where | the merge gate (`task ci`, `hack/ci/gate.sh`: a skippable step after `operator-e2e`, so a preempted VM resumes past it) and `ci.yml`'s `emulator` job, which the required `ci` status needs |
| Telemetry | off (`EMULATOR_TELEMETRY=off`, `--telemetry off`, the dead proxy) |

First run (offload run `off-ack-extensions-1113023721-5738`, c3d-standard-8):
on 1.0.338 **21 of 35 pass** (`TestPublicAccessBlock` among them), the
14 listed fail, and the judgement passes.

Moving the pin: install the new release, run `task test:aws-emulator:ack`,
and change the list to what that release does; a test that now passes
comes off it, a new failure is read against the API reference first (fix
ours, fix the test, or add it here with its missing operation).

The Cloud Control kinds are not in this run: the emulator's Cloud Control
keeps them in its own store (0 of 13 moto-proven tests pass, above), so
the schema fake and moto stay their tests. `task test:aws-emulator` (the
survey, `ACK_EMULATOR_EXPECT=docs/aws-emulator-1.0.338.csv`) remains by
hand.
