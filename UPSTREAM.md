# Upstream

ack-extensions builds on AWS Controllers for Kubernetes (ACK). This file is the
record of exactly which ACK code it uses, and of every file copied from it.

`upstream.yaml` says the same as this file's tables in machine-readable
form (pins, pull requests, patches, copied files), for tools such as the
factory's daily watch. Change both together; `task upstream:yaml-check`
(part of `task check`) fails when they disagree.

## Pins

| Project | Version | Commit | How it is used |
| --- | --- | --- | --- |
| code-generator | `v0.67.0` | `5a7bcc2cc51b71344ec1f64d235e158731970bea` | fetched by `hack/upstream.sh` into `third_party/code-generator`, patched, built as `bin/ack-generate`; only `task generate` needs it |
| runtime | `v0.67.0` | `6655c7437e711aa149dbaf7956b2540ee93dc4b2` | Go module (`go.mod`): the reconciler, adoption, field exports, cross-account role mapping, credentials |
| rds-controller | `v1.14.2` | `ca4f0d529241be52349f6b38478be0cb4b9acb53` | Go module: the API types of ACK's `DBProxy`, `DBInstance` and `DBCluster`, which our kinds reference |
| applicationautoscaling-controller | `v1.9.0` | `009baef3a28e7d8a49a61c520eb6c537c674d52b` | not a module here: the Application Auto Scaling SDK client is at its version (`service/applicationautoscaling` `v1.34.10`) |
| autoscaling-controller | `v1.5.0` | `56316c75a5da8fdf8079917a16e540be4e84e986` | not a module here: its `AutoScalingGroup` generator entry, hook templates, `hooks.go` and `pkg/tags` copied for our fixed copy, `autoscaling.aws.hfl.dev` `AutoScalingGroup` (generated from `v1.41.5` models); the EC2 Auto Scaling SDK client is at its version (`service/autoscaling` `v1.64.3`) |
| kms-controller | `v1.7.0` | `57250fba83d58aeb84a3ec2770cf3e9b45708895` | Go module: the API types of ACK's `Key` (which `ReplicaKey` references), `KeyMetadata` and `Tag`; the KMS SDK client at its version (`service/kms` `v1.37.14`); its controller (`cmd/controller`), built from the module, runs ACK's side of `TestKeyExtension` (`docs/extensions.md`) |
| ec2-controller | `v1.23.1` | `9303654c2cffad05147e4260105439149f7c81bf` | Go module (since 2026-11-15, open question 16): the API types of ACK's `VPC`, `RouteTable`, `TransitGateway`, `Instance`, `SecurityGroup` and `Subnet`, which `VPNGateway`, `VPNGatewayRoutePropagation`, `VPNConnection`, `VolumeAttachment` and `LaunchTemplate` reference (and its CRDs, for the tests); its `LaunchTemplate` generator entry, hook templates and `hooks.go` copied for our fixed copy, `ec2.aws.hfl.dev` `LaunchTemplate` (generated from the `v1.34.0` models like the rest of `services/ec2`; ec2-controller was generated from `v1.41.2`); its `pkg/tags` is copied; the EC2 SDK client is at its version (`service/ec2` `v1.290.1`) |
| ecr-controller | `v1.11.1` | `72590cedeb2a4a92d086175762a97aa2ed2bc979` | not a module here: ACK's ECR kinds are left to it (the generator ignores them); the ECR SDK client is at its version (`service/ecr` `v1.55.0`) |
| ec2-controller PR #364 | head | `ad464975082935f6aedee21de2f0acd112e49994` | `LaunchTemplate`'s references under `spec.data` and `preserveDataReferenceFields` (closed unmerged, 2026-08-12) |
| ecr-controller PR #121 | head | `f6c7d57e0b169547ed2300cbba0c65b11691b0eb` | `ReplicationConfiguration`'s API, behaviour and conversions (open, conflicting, no review, 2026-11-09) |
| eventbridge-controller | `v1.9.2` | `fd084a0bb4d9025f294516d6319b88293be1de8d` | not a module here: the EventBridge SDK client is at its version (`service/eventbridge` `v1.36.7`) |
| secretsmanager-controller | `v1.8.2` | tag | not a module here: the Secrets Manager SDK client the end-to-end tests read a connection's secret with is at its version (`service/secretsmanager` `v1.34.14`) |
| wafv2-controller | `v1.8.1` | `d910e96f94d5b860cffc5dde648e87bafe94689c` | Go module: the API types of ACK's `WebACL`, which `WebACLAssociation` references; the WAFv2 SDK client at its version (`service/wafv2` `v1.55.11`) |
| elbv2-controller | `v1.9.0` | tag | Go module (since 2026-11-16): the API types of ACK's `LoadBalancer` and `TargetGroup`, which `WebACLAssociation`'s `resourceRef` and `Permission`'s `targetGroupRef` reference, and its CRDs, for the tests. `v1.9.0`, not the catalog's `v1.10.0`: `v1.10.0` needs runtime `v0.68.0`, past this repository's pin; the references read only `status.ackResourceMetadata.arn` and the conditions, which are the same in both; it moves to `v1.10.0` with the next runtime pin move. No SDK client of its own. Kept as a direct dependency by the builder's answer (2026-11-17, item ACKELBV2; `docs/plan.md` section 3) |
| lambda-controller | `v1.19.0` | `86905357482d01538cb00ab28d59c6af5c46e6f4` | Go module: the API types of ACK's `Function`, which `Permission` references; the Lambda SDK client at its version (`service/lambda` `v1.88.5`) |
| iam-controller | `v1.11.3` | `91ed7f353255e54e2c6372b3ce757bd2e7fae4ad` | Go module: the API types of ACK's `Role` and `Policy`, which our kinds reference; its `OpenIDConnectProvider` generator entry, hook template and hand-written code are copied into `services/iam` for our fixed copy, `iam.aws.hfl.dev` `OpenIDConnectProvider` (`docs/plan.md` section 6.42), generated from the aws-sdk-go-v2 `v1.34.0` models like the rest of `services/iam` (iam-controller was generated from `v1.32.6`; IAM's OpenID Connect provider calls are the same in both) |
| eks-controller | `v1.26.0` | `2549d1be1a9f1841aaf3a4303a0d5696ea941473` | not a module here: every release needs aws-sdk-go-v2 `v1.45.1` or later, past this repository's `v1.41.5`. Its `Cluster` CRD is copied into `hack/ack-crds` for the tests; `OpenIDConnectProvider.spec.urlRef` reads a `Cluster` through a mirror of the fields it needs (`services/iam/pkg/ekscluster`: conditions, `status.identity.oidc.issuer`). `v1.26.0` is the catalog's pin and the last release on runtime `v0.67.0` |
| organizations-controller | `v1.7.0` | `5824700fd7` | Go module: the API types of ACK's `Account` and `OrganizationalUnit`, which `AccountPlacement`, `DelegatedAdministrator` and `PolicyAttachment` reference (and its CRDs, for the tests); ACK's `Account` and `OrganizationalUnit` are left to it (the generator ignores them); the Organizations SDK client is at its version (`service/organizations` `v1.37.4`) |
| EventBridge Scheduler | none | | no ACK controller (`community` issue #2784); the Scheduler SDK client is `service/scheduler` `v1.12.13`, its version in aws-sdk-go-v2 `v1.34.0`, the release of the models below |
| s3control-controller | `v1.9.0` | `2ee9ccbfaaaa36ad2105143502a902e03906bfd8` | not a module here: ACK's `AccessPoint` is left to it (the generator ignores every S3 Control resource but our `PublicAccessBlock`); the S3 Control SDK client is at its version (`service/s3control` `v1.68.4`) |
| s3-controller | `v1.15.0` | `874152a6dbff23e60fcbaf8eda1a074121207ee0` | not a module here (a new upstream dependency is the builder's call): ACK's `Bucket` is left to it, extended by our s3 manager (`docs/extensions.md`), which reads it as unstructured; its `Bucket` CRD is copied into `hack/ack-crds` for the tests; its `generator.yaml`, hook templates and hand-written `Bucket` code are copied into `services/s3` for our fixed copy, `s3.aws.hfl.dev` `Bucket` (`docs/plan.md` section 6.38); the S3 SDK client is at its version (`service/s3` `v1.97.3`). `v1.15.0` is the catalog's pin and the last release on runtime `v0.67.0` |
| cloudtrail-controller | `v1.10.0` | `2cffdfa083fc390d5d9ad4ddb3ed468417ecd641` | not a module here: ACK's `Trail` and `EventDataStore` are left to it (the generator ignores every CloudTrail resource), extended by our cloudtrail manager (`docs/extensions.md`; `docs/plan.md` open question 26, answered 2026-11-23), which reads ACK's `Trail` as unstructured; its `Trail` CRD is copied into `hack/ack-crds` for the tests; the CloudTrail SDK client is at its version (`service/cloudtrail` `v1.47.2`, on aws-sdk-go-v2 `v1.41.5` as this repository is) |
| cloudwatchlogs-controller | `v1.11.0` | `7f4e9382b1affeb5280724bd7c01a5022fe54f72` | not a module here: its `LogGroup` generator entry, hook templates and hand-written code copied into `services/cloudwatchlogs` for our fixed copy, `cloudwatchlogs.aws.hfl.dev` `LogGroup` (with `spec.logGroupClass`) |
| efs-controller | `v1.9.0` | `4bea998f5732ab6d21d225824e09588c7248cb67` | not a module here: its `FileSystem` and `MountTarget` generator entries, hook templates and hand-written code copied into `services/efs` for our fixed copies, `efs.aws.hfl.dev` `FileSystem` and `MountTarget` (`FileSystemPolicy` is ours, generated from `PutFileSystemPolicy`) |
| elasticache-controller | `v1.13.0` | `2c1bbfbc231a32023bed41761760bae7ad088eba` | not a module here: its `CacheCluster` and `ReplicationGroup` generator entries, hook templates and hand-written code copied into `services/elasticache` for our fixed copies, `elasticache.aws.hfl.dev` `CacheCluster` and `ReplicationGroup` |
| aws-sdk-go-v2 models | `v1.34.0` | tag | the Smithy model the generator reads (`--aws-sdk-go-version`), the version rds-controller `v1.14.2` was generated from |
| aws-sdk-go-v2 models (s3) | `v1.41.5` | tag | the Smithy model the generator reads for s3 alone (`hack/generate.sh`, `SDK_VERSION_FOR`): the version s3-controller `v1.15.0` was generated from (its `ack-generate-metadata.yaml`), so our copy of its `Bucket` is generated the same |

The runtime, code generator and controllers move together: ACK releases
them in step, and a pin move takes all of them to one ACK release.

The twelve account-level controllers pinned above (not organizations or
secretsmanager) are also run, as ACK's own images, by the operator: one
per service per environment that enables it (`docs/plan.md` section 7.7,
open question 18, answered 2026-11-15). Their images are pinned by
digest in `config/operator/operator.yaml` (`ACK_IMAGE_<SERVICE>`), and
their ClusterRoles and IAM recommendations copied (below), all by
`hack/ack-controllers.sh` from these pins; rerun it after a pin move.

## Patches

`patches/code-generator/`, applied in order by `hack/upstream.sh`:

| Patch | What | Upstream? |
| --- | --- | --- |
| `0001-api-group-suffix-flag.patch` | adds `--api-group-suffix` to `ack-generate apis` and `controller`, so kinds generate into `<service>.aws.hfl.dev`; the generator already supports a suffix internally (its Crossplane path uses one) but its ACK commands always pass the default | offered as aws-controllers-k8s/code-generator#762 (2026-11-10), which also passes the flag to `release` |
| `0002-sdk-v2-createvolume-output-shape.patch` | takes EC2's `CreateVolume` off the generator's "shame list" of operations whose output keeps the v1 SDK's shape name: in aws-sdk-go-v2 the output is `CreateVolumeOutput`, and with the list entry the generator writes `*ec2.Volume`, which v2 does not have, so a generated `Volume` does not compile | offered as aws-controllers-k8s/code-generator#763 (2026-11-10), with an assertion in `TestEC2_Volume`; the other EC2 entries (`CreateSnapshot`, `AttachVolume`, `DetachVolume`) are probably the same case and were left alone |
| `0003-slot-guard.patch` | the version-slot guard (`docs/slot-moves.md`): the `main.go` template binds `--slot`, adds the slot tag to ACK's default tags and wraps the resource manager factories with `pkg/slot`; the `registry.go` template adds the RBAC marker for core events (the guard's `SlotMismatch` events) | no: ours alone (it imports `pkg/slot`, an ack-extensions package) |
| `0004-extend-ack-kinds.patch` | the `main.go` template calls `pkg/extend`'s `Setup` once ACK's service controller is bound to the manager, so a service's hand-written code can add controllers that extend ACK's own kinds (`docs/extensions.md`) | no: ours alone (it imports `pkg/extend`) |

## Pull requests we opened upstream

Our patches offered upstream, from the builder's fork (approved
2026-11-10). When one merges and a release ships it, drop the patch and
move the pin.

| PR | Head | Patch | State (2026-11-10) |
| --- | --- | --- | --- |
| aws-controllers-k8s/code-generator#762 | `1dd3ca96` | `0001-api-group-suffix-flag.patch` | open |
| aws-controllers-k8s/code-generator#763 | `8c009dd8` | `0002-sdk-v2-createvolume-output-shape.patch` | open |

## What is copied

Every copied file keeps its Amazon copyright and Apache-2.0 header and
carries a `Modified by High Five Labs for ack-extensions` note naming its source
and what changed.

| Our file | Source | Source commit | Change |
| --- | --- | --- | --- |
| `services/iam/apis/v1alpha1/role_policy_attachment.go` | iam-controller PR #184 `apis/v1alpha1/role_policy_attachment.go` | `78eb350ec61caa74b08e8242380b0545164b5576` (PR head) | none (package path only) |
| `services/iam/pkg/resource/role_policy_attachment/{delta,descriptor,identifiers,manager,manager_factory,resource,sdk}.go` | iam-controller PR #184 `pkg/resource/role_policy_attachment/` | `78eb350e…` | import path; API group `iam.aws.hfl.dev` |
| `services/iam/pkg/resource/role_policy_attachment/references.go` | iam-controller PR #184 | `78eb350e…` | import path; API group; `roleRef`/`policyRef` resolve to ACK's own `Role`/`Policy` (`iam.services.k8s.aws`, from ACK's module); RBAC markers to read them |
| `services/rds/generator.yaml` (the `OptionGroup` entry) | rds-controller PR #257 `generator.yaml` lines 720-796 | `32c3cb2394518c5e976eba841e070cc6053484b1` (PR head) | one hook added, `sdk_read_many_post_build_request` (`describeByNameOnly`, in our own `option_group/read.go`): the PR's read sends `OptionGroupName` with `EngineName` and `MajorEngineVersion`, which AWS refuses (`docs/plan.md` section 6.2); the rest of the file ignores every resource ACK has |
| `services/rds/templates/hooks/option_group/*.go.tpl` | rds-controller PR #257 `templates/hooks/option_group/` | `32c3cb23…` | none |
| `services/rds/pkg/resource/option_group/{hooks,hooks_test}.go` | rds-controller PR #257 `pkg/resource/option_group/` | `32c3cb23…` | import paths |
| `services/rds/pkg/util/tags.go` | rds-controller PR #257 `pkg/util/tags.go` | `32c3cb23…` | import path (rds-controller `main` has since moved to the runtime's tag helpers, #330) |
| `services/ec2/generator.yaml` (the `KeyPair` entry and its operations and ignores) | ec2-controller PR #396 `generator.yaml` | `91d99b7a5fd88d4eb98944c46e23c5a45c3d05bb` (PR head) | none; `KeyPair` taken off `ignore.resource_names` |
| `services/ec2/templates/hooks/key_pair/{sdk_create_post_build_request,sdk_read_many_post_build_request}.go.tpl` | ec2-controller PR #396 `templates/hooks/key_pair/` | `91d99b7a…` | none |
| `services/ec2/pkg/resource/key_pair/{hooks,hooks_test}.go` | ec2-controller PR #396 `pkg/resource/key_pair/` | `91d99b7a…` | import paths (our API types and `services/ec2/pkg/tags`); `hooks_test.go`'s stub client from `fakeaws.Config` (`task noaws`) |
| `services/ecr/apis/v1alpha1/replication_configuration.go` | ecr-controller PR #121 `apis/v1alpha1/replication_configuration.go` | `f6c7d57e…` (PR head) | import path; API group; header (the file is marked generated but was written by hand) |
| `services/ecr/pkg/resource/replication_configuration/sdk.go` (the behaviour and conversions) | ecr-controller PR #121 `pkg/resource/replication_configuration/manager.go` | `f6c7d57e…` (PR head) | split into our hand-written kinds' sdk functions; debug logging dropped; no rules reads as not found; `spec.registryID` must be the manager's registry; update returns the put configuration |
| `services/ecr/pkg/resource/replication_configuration/{descriptor,identifiers,manager,manager_factory,resource}.go` | iam-controller PR #184 `pkg/resource/role_policy_attachment/` (the hand-written skeleton) | `78eb350e…` | package, kind and API types |
| `services/rds/pkg/resource/db_proxy_target/{descriptor,identifiers,manager,manager_factory,resource}.go` | iam-controller PR #184 `pkg/resource/role_policy_attachment/` (the hand-written skeleton) | `78eb350e…` | package, kind, API group, RDS client, ARN format, `IsSynced` (synced when every target is AVAILABLE), adoption fields |
| `services/autoscaling/generator.yaml` (the `ScalingPolicy` entry's `operations` mapping and its shape) | applicationautoscaling-controller `v1.9.0` `generator.yaml` (`ScalingPolicy` from `PutScalingPolicy`) | `009baef3…` (`v1.9.0`) | EC2 Auto Scaling's operations (`PutScalingPolicy`, `DescribePolicies`, `DeletePolicy`), its fields, and late-initialized server defaults; the hook templates are written here |
| `services/applicationautoscaling/generator.yaml` (the `ScheduledAction` entry's `operations` mapping) | applicationautoscaling-controller `v1.9.0` `generator.yaml` (`ScalingPolicy` from `PutScalingPolicy`) | `009baef3…` | `PutScheduledAction`, `DescribeScheduledActions`, `DeleteScheduledAction`; the resources ACK has ignored |
| `services/applicationautoscaling/pkg/resource/scheduled_action/custom_delta.go` | applicationautoscaling-controller `v1.9.0` `pkg/resource/scalable_target/custom_delta.go` | `009baef3…` | package; the server's default time zone instead of `SuspendedState` |
| `services/eventbridge/pkg/resource/api_destination/custom_delta.go` | applicationautoscaling-controller `v1.9.0` `pkg/resource/scalable_target/custom_delta.go` | `009baef3…` | package; an API destination's rate limit instead of `SuspendedState` |
| `services/iam/pkg/resource/login_profile/custom_delta.go` | applicationautoscaling-controller `v1.9.0` `pkg/resource/scalable_target/custom_delta.go` | `009baef3…` | package; a login profile's `passwordResetRequired` instead of `SuspendedState` |
| `services/scheduler/pkg/resource/schedule/custom_delta.go` | applicationautoscaling-controller `v1.9.0` `pkg/resource/scalable_target/custom_delta.go` | `009baef3…` | package; a schedule's server defaults instead of `SuspendedState` |
| `services/s3/generator.yaml` (below its header) | s3-controller `v1.15.0` `generator.yaml` | `874152a6…` (`v1.15.0`) | `Object` added to `ignore.shape_names`, for our hand-written `Object` kind (`docs/plan.md` open question 27); the header says what the file is |
| `services/s3/templates/hooks/bucket/{new_resource_manager_client_options,sdk_create_post_build_request,sdk_create_post_set_output,sdk_read_many_post_set_output}.go.tpl` | s3-controller `v1.15.0` `templates/hooks/bucket/` | `874152a6…` | `sdk_create_post_set_output.go.tpl`: the requeue returned inside an `if` (it always is), so the generated `return` after the hook is not unreachable code, which `go vet` refuses |
| `services/s3/pkg/resource/bucket/{hook,acl_custom,sdk_file_end}.go` | s3-controller `v1.15.0` `pkg/resource/bucket/` | `874152a6…` | import path; `hook.go`: `customFindBucket` (by way of `addPutFieldsToSpec`) no longer dereferences `GetBucketEncryption`'s nil output when S3 answers `ServerSideEncryptionConfigurationNotFoundError` (`docs/plan.md` section 8, item 15; community#2990); `sdk_file_end.go` had no licence header and has one |
| `services/s3/pkg/resource/bucket/{hook_test,acl_custom_test,notification_eventbridge_test}.go` | s3-controller `v1.15.0` `pkg/resource/bucket/` | `874152a6…` | import path |
| `services/iam/generator.yaml` (the `OpenIDConnectProvider` entry) | iam-controller `v1.11.3` `generator.yaml` | `91ed7f35…` (`v1.11.3`) | `references` on `URL` (ACK's eks `Cluster`, `Status.Identity.OIDC.Issuer`) added |
| `services/iam/templates/hooks/open_id_connect_provider/sdk_read_one_post_set_output.go.tpl` | iam-controller `v1.11.3` `templates/hooks/open_id_connect_provider/` | `91ed7f35…` | none |
| `services/iam/pkg/resource/open_id_connect_provider/{hooks,tags}.go` | iam-controller `v1.11.3` `pkg/resource/open_id_connect_provider/` | `91ed7f35…` | import path. Its `hooks_tags_test.go` is not copied: it sets the SDK client's `HTTPClient` by hand, which `task noaws` refuses in test paths |
| `hack/ack-crds/eks.services.k8s.aws_clusters.yaml` | eks-controller `v1.26.0` `config/crd/bases/eks.services.k8s.aws_clusters.yaml` | `2549d1be…` (`v1.26.0`) | none: ACK's `Cluster` CRD, installed by the end-to-end tests' API server (`pkg/test/harness`) |
| `services/ec2/generator.yaml` (the `LaunchTemplate` entry and its operations and ignores) | ec2-controller `v1.23.1` `generator.yaml` | `9303654c…` (`v1.23.1`) | the references under `Data` and `Data`'s `set: Update ignore`, from ec2-controller pull request #364 (closed unmerged, head `ad464975`); `CreateLaunchTemplateVersionOutput.LaunchTemplateVersion.DefaultVersion` ignored, as ACK's newer models need |
| `services/ec2/templates/hooks/launch_template/{sdk_create_post_build_request,sdk_file_end,sdk_read_many_post_set_output,sdk_update_pre_build_request}.go.tpl` | ec2-controller `v1.23.1` `templates/hooks/launch_template/` | `9303654c…` | none |
| `services/ec2/pkg/resource/launch_template/hooks.go` | ec2-controller `v1.23.1` `pkg/resource/launch_template/hooks.go` | `9303654c…` | import path; `status.operator` not set (the aws-sdk-go-v2 `v1.34.0` models this service is generated from lack `LaunchTemplate.Operator`); `preserveDataReferenceFields` and its call, from pull request #364 |
| `services/autoscaling/generator.yaml` (the `AutoScalingGroup` entry and its operations and ignores) | autoscaling-controller `v1.5.0` `generator.yaml` | `56316c75…` (`v1.5.0`) | `launchTemplateRef` on all three `LaunchTemplateSpecification` paths (naming `ec2.aws.hfl.dev` `LaunchTemplate`), `spec.context` and `Tag.PropagateAtLaunch` un-ignored, `sdk_read_many_post_set_output` hook added |
| `services/autoscaling/templates/hooks/auto_scaling_group/{delta_pre_compare,sdk_create_post_set_output,sdk_update_pre_build_request}.go.tpl` | autoscaling-controller `v1.5.0` `templates/hooks/auto_scaling_group/` | `56316c75…` | `normalizeAutoScalingGroupForCompare` call added to `delta_pre_compare.go.tpl` |
| `services/autoscaling/pkg/resource/auto_scaling_group/hooks.go` | autoscaling-controller `v1.5.0` `pkg/resource/auto_scaling_group/hooks.go` | `56316c75…` | import path; `preserveLaunchTemplateReferenceFields` and `normalizeAutoScalingGroupForCompare` added |
| `services/autoscaling/pkg/tags/tags.go` | autoscaling-controller `v1.5.0` `pkg/tags/tags.go` | `56316c75…` | import path; per-tag `PropagateAtLaunch` instead of `PropagateAtLaunchDefault` |
| `hack/ack-crds/s3.services.k8s.aws_buckets.yaml` | s3-controller `v1.15.0` `config/crd/bases/s3.services.k8s.aws_buckets.yaml` | `874152a6…` (`v1.15.0`) | none: ACK's `Bucket` CRD, installed by the end-to-end tests' API server (`pkg/test/harness`) |
| `hack/ack-crds/cloudtrail.services.k8s.aws_trails.yaml` | cloudtrail-controller `v1.10.0` `config/crd/bases/cloudtrail.services.k8s.aws_trails.yaml` | `2cffdfa0…` (`v1.10.0`) | none: ACK's `Trail` CRD, installed by the end-to-end tests' API server (`pkg/test/harness`) |
| `services/s3control/pkg/resource/public_access_block/custom_delta.go` | applicationautoscaling-controller `v1.9.0` `pkg/resource/scalable_target/custom_delta.go` | `009baef3…` | package; the four settings of an account's public access block, and only S3's default (false) taken from the latest state |
| `services/autoscaling/pkg/resource/scheduled_action/custom_delta.go` | applicationautoscaling-controller `v1.9.0` `pkg/resource/scalable_target/custom_delta.go` | `009baef3…` | package; `ScheduledAction`'s server-defaulted fields (start time, time zone) |
| `services/autoscaling/pkg/resource/scaling_policy/custom_delta.go` | applicationautoscaling-controller `v1.9.0` `pkg/resource/scalable_target/custom_delta.go` | `009baef3…` | package; `ScalingPolicy`'s server-defaulted fields instead of `SuspendedState` |
| `services/kms/apis/v1alpha1/replica_key.go` | kms-controller PR #112 `apis/v1alpha1/replica_key.go` | `eb4057e4…` (PR head) | written by hand here, so the generator's header is replaced; documentation shortened; ACK's `Tag` and `KeyMetadata`; `keyID`/`keyRef` exactly one, `keyID` immutable |
| `services/kms/pkg/resource/replica_key/sdk.go`, `keymetadata.go` | kms-controller PR #112 `pkg/resource/replica_key/{hook,policy_hooks,tag_hooks}.go` | `eb4057e4…` | every call on the replica sent to its Region by ARN (the PR sends them to the primary key's Region); the replica found through the primary key; create and delete written here |
| `services/kms/pkg/resource/replica_key/{descriptor,identifiers,manager,manager_factory,resource}.go` | iam-controller PR #184 `pkg/resource/role_policy_attachment/` (the hand-written skeleton, by way of our `db_role_association` copy) | `78eb350e…` | package, kind, API group, KMS client, ARN, `IsSynced` (synced when `Enabled`), adoption fields |
| `services/ec2/pkg/tags/{sync,sync_test}.go` | ec2-controller `v1.23.1` `pkg/tags/` | `9303654c…` | our `ec2.aws.hfl.dev` API types |
| `services/ec2/pkg/resource/customer_gateway/hooks.go` | ec2-controller `v1.23.1` `pkg/resource/internet_gateway/hooks.go` (`customUpdateInternetGateway`, `updateTagSpecificationsInCreateRequest`) | `9303654c…` | a customer gateway: tags only, no VPC attachment or route tables |
| `services/ec2/pkg/resource/vpn_connection/hooks.go` (`customUpdateVpnConnection`, `updateTagSpecificationsInCreateRequest`), `services/ec2/templates/hooks/vpn_connection/sdk_create_post_build_request.go.tpl` | ec2-controller `v1.23.1` `pkg/resource/internet_gateway/hooks.go`, through `vpn_gateway/hooks.go` | `9303654c…` | a VPN connection: tags, static routes, the connection's CIDRs and each tunnel's options; the options' read, the static routes and the other templates written here |
| `services/ec2/pkg/resource/vpn_gateway/hooks.go`, `services/ec2/templates/hooks/vpn_gateway/*.go.tpl` | ec2-controller `v1.23.1` `pkg/resource/internet_gateway/hooks.go` and `templates/hooks/internet_gateway/` | `9303654c…` | a VPN gateway (`AttachVpnGateway`, `DetachVpnGateway`, `VpcAttachments`); no route tables; a deleted gateway reads as gone |
| `services/ec2/pkg/resource/volume/hooks.go` | ec2-controller `v1.23.1` `pkg/resource/internet_gateway/hooks.go` | `9303654c…` | an EBS volume: `ModifyVolume` and tags; no attachment or route tables |
| `services/ec2/pkg/resource/volume/custom_delta.go` | applicationautoscaling-controller `v1.9.0` `pkg/resource/scalable_target/custom_delta.go` | `009baef3…` | an EBS volume's server defaults |
| `services/organizations/pkg/resource/policy/hooks.go` (`getTags`, `syncTags` and the tag conversions) | eventbridge-controller `v1.9.2` `pkg/resource/event_bus/hooks.go` | `fd084a0b…` | package; Organizations' client and its `ResourceId` fields, `ListTagsForResource` paged; the policy summary hooks are written here |
| `services/scheduler/pkg/resource/schedule_group/hooks.go` | eventbridge-controller `v1.9.2` `pkg/resource/event_bus/hooks.go` | `fd084a0b…` | package; the Scheduler client and its `ResourceArn` fields; the resource policy dropped |
| `services/lambda/pkg/resource/permission/sdk.go` (the calls and the reading of a policy statement) | lambda-controller `v1.19.0` `pkg/resource/alias/hooks.go` (`addPermission`, `removePermission`, `setPermissions`) | `86905357…` | one statement instead of an alias's set; `encoding/json` instead of `micahhausler/aws-iam-policy`; not-found handling |
| `services/lambda/pkg/resource/permission/{descriptor,identifiers,manager,manager_factory,resource}.go` | iam-controller PR #184 `pkg/resource/role_policy_attachment/` (the hand-written skeleton, by way of our `db_role_association` copy) | `78eb350e…` | package, kind, API group, Lambda client, ARN format, `IsSynced` (synced once read back), adoption fields |
| `services/ec2/pkg/resource/volume_attachment/{descriptor,identifiers,manager,manager_factory,resource}.go` | iam-controller PR #184 `pkg/resource/role_policy_attachment/` (the hand-written skeleton) | `78eb350e…` | package, kind, API group, service client, ARN form, adoption fields and the `IsSynced` hook (synced when attached) |
| `services/ec2/pkg/resource/default_security_group/{descriptor,identifiers,manager,manager_factory,resource}.go` | iam-controller PR #184 `pkg/resource/role_policy_attachment/` (the hand-written skeleton, by way of our `resource_tag` copy) | `78eb350e…` | package, kind, API group, service client, ARN form, adoption field (`vpcID`) and the `IsSynced` hook (synced once read) |
| `services/ec2/pkg/resource/default_security_group/rules.go` | ec2-controller `v1.23.1` `pkg/resource/security_group/hooks.go` (rule canonicalisation, `syncSGRules`, Authorize/Revoke, `containsRule`) and `sdk.go` (`compareIPPermission`, `newIPPermission`, `setResourceIPPermission`) | `9303654c…` | package; the rule types are ACK's own (`ackec2`) |
| `services/ec2/pkg/resource/{default_network_acl,default_route_table,default_vpc}/{descriptor,identifiers,manager,manager_factory,resource}.go` | iam-controller PR #184 `pkg/resource/role_policy_attachment/` (the hand-written skeleton, by way of our `default_security_group` copy) | `78eb350e…` | package, kind, API group, service client, ARN form, adoption field (`vpcID`; none for `DefaultVPC`) and the `IsSynced` hook (synced once read) |
| `services/ec2/pkg/resource/default_network_acl/entries.go` | ec2-controller `v1.23.1` `pkg/resource/network_acl/hooks.go` (`syncEntries`, `createEntry`, `updateEntry`, `deleteEntry`, the 32767 filter) and `sdk.go` (reading entries) | `9303654c…` | package; entries compared by a canonical key instead of the generated `compareNetworkACLEntry`; one builder for the create and replace inputs |
| `services/ec2/pkg/resource/default_route_table/routes.go` | ec2-controller `v1.23.1` `pkg/resource/route_table/sdk.go` (`compareCreateRouteInput`, `newCreateRouteInput`, `newDeleteRouteInput`, reading a route, the `vpce-` correction) and `hooks.go` (`getRoutesDifference`) | `9303654c…` | package; ACK's own route type (`ackec2`); only `CreateRoute`-origin routes read, gateway-endpoint routes left out |
| `services/ec2/pkg/resource/resource_tag/{descriptor,identifiers,manager,manager_factory,resource}.go` | iam-controller PR #184 `pkg/resource/role_policy_attachment/` (the hand-written skeleton, by way of our `volume_attachment` copy) | `78eb350e…` | package, kind, API group, service client, ARN form, adoption fields (`resourceID`, `key`) and the `IsSynced` hook (synced once read) |
| `services/s3/pkg/resource/object/{descriptor,identifiers,manager,manager_factory,resource}.go` | iam-controller PR #184 `pkg/resource/role_policy_attachment/` (the hand-written skeleton, by way of our `resource_tag` copy) | `78eb350e…` | package, kind, API group, service client (S3, path-style as ACK's flag says), ARN form (S3's), adoption fields (`bucketName`, `key`) and the `IsSynced` hook (synced once read) |
| `services/iam/pkg/resource/user_group_membership/{descriptor,identifiers,manager,manager_factory,resource}.go` | iam-controller PR #184 `pkg/resource/role_policy_attachment/` (the hand-written skeleton) | `78eb350e…` | package, kind, ARN form, adoption fields and the `IsSynced` hook (synced once read) |
| `services/iam/pkg/resource/ssh_public_key/{descriptor,identifiers,manager,manager_factory,resource}.go` | iam-controller PR #184 `pkg/resource/role_policy_attachment/` (the hand-written skeleton) | `78eb350e…` | package, kind, ARN form, adoption by key ID and the `IsSynced` hook (synced once read) |
| `services/iam/pkg/resource/account_alias/{descriptor,identifiers,manager,manager_factory,resource}.go` | iam-controller PR #184 `pkg/resource/role_policy_attachment/` (the hand-written skeleton) | `78eb350e…` | package, kind, ARN form, adoption field and the `IsSynced` hook (synced once read) |
| `services/iam/pkg/resource/account_password_policy/{descriptor,identifiers,manager,manager_factory,resource}.go` | iam-controller PR #184 `pkg/resource/role_policy_attachment/` (the hand-written skeleton) | `78eb350e…` | package, kind, ARN form, no adoption fields and the `IsSynced` hook (synced once read) |
| `services/rds/pkg/resource/db_role_association/{descriptor,identifiers,manager,manager_factory,resource}.go` | iam-controller PR #184 `pkg/resource/role_policy_attachment/` (the hand-written skeleton, by way of our `db_proxy_target` copy) | `78eb350e…` | package, kind, API group, RDS client, ARN format, `IsSynced` (synced when the association is ACTIVE), adoption fields (`roleARN`, `featureName`, the cluster or instance) |
| `config/ack-controllers/applicationautoscaling-controller-role.yaml`, `pkg/ackiam/recommended/applicationautoscaling/recommended-policy-arn` | applicationautoscaling-controller `v1.9.0` `config/rbac/cluster-role-controller.yaml`, `config/iam/recommended-policy-arn` | `009baef3…` (`v1.9.0`) | the ClusterRole renamed `ack-extensions-ack-applicationautoscaling-controller`; the IAM files as they are (`hack/ack-controllers.sh`; ACK's controller per environment, `docs/plan.md` section 7.7) |
| `config/ack-controllers/autoscaling-controller-role.yaml`, `pkg/ackiam/recommended/autoscaling/recommended-policy-arn` | autoscaling-controller `v1.5.0` `config/rbac/cluster-role-controller.yaml`, `config/iam/recommended-policy-arn` | `56316c75…` (`v1.5.0`) | the ClusterRole renamed `ack-extensions-ack-autoscaling-controller`; the IAM files as they are (`hack/ack-controllers.sh`; ACK's controller per environment, `docs/plan.md` section 7.7) |
| `config/ack-controllers/ec2-controller-role.yaml`, `pkg/ackiam/recommended/ec2/recommended-policy-arn` | ec2-controller `v1.23.1` `config/rbac/cluster-role-controller.yaml`, `config/iam/recommended-policy-arn` | `9303654c…` (`v1.23.1`) | the ClusterRole renamed `ack-extensions-ack-ec2-controller`; the IAM files as they are (`hack/ack-controllers.sh`; ACK's controller per environment, `docs/plan.md` section 7.7) |
| `config/ack-controllers/ecr-controller-role.yaml`, `pkg/ackiam/recommended/ecr/recommended-policy-arn` | ecr-controller `v1.11.1` `config/rbac/cluster-role-controller.yaml`, `config/iam/recommended-policy-arn` | `72590ced…` (`v1.11.1`) | the ClusterRole renamed `ack-extensions-ack-ecr-controller`; the IAM files as they are (`hack/ack-controllers.sh`; ACK's controller per environment, `docs/plan.md` section 7.7) |
| `config/ack-controllers/eventbridge-controller-role.yaml`, `pkg/ackiam/recommended/eventbridge/recommended-policy-arn` | eventbridge-controller `v1.9.2` `config/rbac/cluster-role-controller.yaml`, `config/iam/recommended-policy-arn` | `fd084a0b…` (`v1.9.2`) | the ClusterRole renamed `ack-extensions-ack-eventbridge-controller`; the IAM files as they are (`hack/ack-controllers.sh`; ACK's controller per environment, `docs/plan.md` section 7.7) |
| `config/ack-controllers/iam-controller-role.yaml`, `pkg/ackiam/recommended/iam/recommended-inline-policy` | iam-controller `v1.11.3` `config/rbac/cluster-role-controller.yaml`, `config/iam/recommended-inline-policy` | `91ed7f35…` (`v1.11.3`) | the ClusterRole renamed `ack-extensions-ack-iam-controller`; the IAM files as they are (`hack/ack-controllers.sh`; ACK's controller per environment, `docs/plan.md` section 7.7) |
| `config/ack-controllers/kms-controller-role.yaml`, `pkg/ackiam/recommended/kms/recommended-inline-policy` | kms-controller `v1.7.0` `config/rbac/cluster-role-controller.yaml`, `config/iam/recommended-inline-policy` | `57250fba…` (`v1.7.0`) | the ClusterRole renamed `ack-extensions-ack-kms-controller`; the IAM files as they are (`hack/ack-controllers.sh`; ACK's controller per environment, `docs/plan.md` section 7.7) |
| `config/ack-controllers/lambda-controller-role.yaml`, `pkg/ackiam/recommended/lambda/recommended-inline-policy` | lambda-controller `v1.19.0` `config/rbac/cluster-role-controller.yaml`, `config/iam/recommended-inline-policy` | `86905357…` (`v1.19.0`) | the ClusterRole renamed `ack-extensions-ack-lambda-controller`; the IAM files as they are (`hack/ack-controllers.sh`; ACK's controller per environment, `docs/plan.md` section 7.7) |
| `config/ack-controllers/rds-controller-role.yaml`, `pkg/ackiam/recommended/rds/recommended-policy-arn` | rds-controller `v1.14.2` `config/rbac/cluster-role-controller.yaml`, `config/iam/recommended-policy-arn` | `ca4f0d52…` (`v1.14.2`) | the ClusterRole renamed `ack-extensions-ack-rds-controller`; the IAM files as they are (`hack/ack-controllers.sh`; ACK's controller per environment, `docs/plan.md` section 7.7) |
| `config/ack-controllers/s3-controller-role.yaml`, `pkg/ackiam/recommended/s3/recommended-inline-policy`, `pkg/ackiam/recommended/s3/recommended-policy-arn` | s3-controller `v1.15.0` `config/rbac/cluster-role-controller.yaml`, `config/iam/recommended-inline-policy`, `config/iam/recommended-policy-arn` | `874152a6…` (`v1.15.0`) | the ClusterRole renamed `ack-extensions-ack-s3-controller`; the IAM files as they are (`hack/ack-controllers.sh`; ACK's controller per environment, `docs/plan.md` section 7.7) |
| `config/ack-controllers/s3control-controller-role.yaml`, `pkg/ackiam/recommended/s3control/recommended-policy-arn` | s3control-controller `v1.9.0` `config/rbac/cluster-role-controller.yaml`, `config/iam/recommended-policy-arn` | `2ee9ccbf…` (`v1.9.0`) | the ClusterRole renamed `ack-extensions-ack-s3control-controller`; the IAM files as they are (`hack/ack-controllers.sh`; ACK's controller per environment, `docs/plan.md` section 7.7) |
| `config/ack-controllers/wafv2-controller-role.yaml`, `pkg/ackiam/recommended/wafv2/recommended-policy-arn` | wafv2-controller `v1.8.1` `config/rbac/cluster-role-controller.yaml`, `config/iam/recommended-policy-arn` | `d910e96f…` (`v1.8.1`) | the ClusterRole renamed `ack-extensions-ack-wafv2-controller`; the IAM files as they are (`hack/ack-controllers.sh`; ACK's controller per environment, `docs/plan.md` section 7.7) |

Not copied, generated here: every file carrying `Code generated by
ack-generate. DO NOT EDIT.` (`hack/generate.sh`), from ACK's templates at
the code-generator pin.

Written here: `services/rds/apis/v1alpha1/db_proxy_target.go`,
`services/rds/pkg/resource/db_proxy_target/{sdk,delta,references,sdk_test}.go`,
`services/rds/apis/v1alpha1/db_role_association.go`,
`services/lambda/apis/v1alpha1/permission.go`,
`services/ec2/templates/hooks/customer_gateway/*.go.tpl`,
`services/ec2/pkg/resource/vpn_connection/hooks_test.go`,
`services/ec2/templates/hooks/network_interface_permission/*.go.tpl`,
`services/ec2/pkg/resource/network_interface_permission/sdk_test.go`,
`services/ec2/templates/hooks/vpn_gateway_route_propagation/*.go.tpl`,
`services/ec2/pkg/resource/vpn_gateway_route_propagation/{hooks,sdk_test,references_test}.go`,
`services/ec2/apis/v1alpha1/ack_kinds.go`,
`services/ecr/pkg/resource/replication_configuration/{delta,references,sdk_test}.go`,
`services/eventbridge/pkg/resource/connection/{hooks,hooks_test}.go`,
`services/eventbridge/pkg/resource/api_destination/custom_delta_test.go`,
`services/eventbridge/templates/hooks/connection/*.go.tpl`,
`services/scheduler/templates/hooks/schedule_group/*.go.tpl`,
`services/scheduler/pkg/resource/schedule/custom_delta_test.go`,
`services/organizations/templates/hooks/policy/*.go.tpl`,
`services/organizations/pkg/resource/policy/{custom_delta,custom_delta_test}.go`,
`services/organizations/templates/hooks/account_placement/*.go.tpl`,
`services/organizations/pkg/resource/account_placement/hooks.go`,
`services/organizations/templates/hooks/delegated_administrator/*.go.tpl`,
`services/organizations/pkg/resource/delegated_administrator/{hooks,hooks_test}.go`,
`services/organizations/templates/hooks/policy_attachment/*.go.tpl`,
`services/organizations/pkg/resource/policy_attachment/{hooks,hooks_test}.go`,
`services/organizations/templates/hooks/organization/*.go.tpl`,
`services/organizations/pkg/resource/organization/{hooks,hooks_test}.go`,
`services/iam/apis/v1alpha1/ack_kinds.go`,
`services/iam/templates/hooks/access_key/*.go.tpl`,
`services/iam/pkg/resource/access_key/{hooks,hooks_test}.go`,
`config/admission/*.yaml`,
`services/organizations/apis/v1alpha1/ack_kinds.go`, `services/organizations/cmd/controller/handwritten.go`,
`services/kms/pkg/resource/replica_key/{delta,references,sdk_test}.go`,
`services/autoscaling/templates/hooks/scaling_policy/*.go.tpl`,
`services/autoscaling/templates/hooks/scheduled_action/*.go.tpl`,
`services/autoscaling/pkg/resource/scheduled_action/{hooks,hooks_test}.go`,
`services/lambda/pkg/resource/permission/{delta,references,sdk_test}.go`,
`services/rds/pkg/resource/db_role_association/{sdk,delta,references,sdk_test}.go`,
`services/iam/apis/v1alpha1/user_group_membership.go`,
`services/iam/pkg/resource/user_group_membership/{sdk,delta,references,sdk_test}.go`,
`services/iam/pkg/resource/login_profile/custom_delta_test.go`,
`services/iam/apis/v1alpha1/ssh_public_key.go`,
`services/iam/pkg/resource/ssh_public_key/{sdk,delta,references,sdk_test}.go`,
`services/iam/apis/v1alpha1/account_alias.go`,
`services/iam/pkg/resource/account_alias/{sdk,delta,references,sdk_test}.go`,
`services/iam/apis/v1alpha1/account_password_policy.go`,
`services/iam/pkg/resource/account_password_policy/{sdk,delta,references,sdk_test}.go`,
`services/iam/templates/hooks/virtual_mfa_device/*.go.tpl`,
`services/iam/pkg/resource/virtual_mfa_device/{hooks,hooks_test}.go`,
`services/ec2/apis/v1alpha1/volume_attachment.go`,
`services/ec2/pkg/resource/volume_attachment/{sdk,delta,references,sdk_test}.go`,
`services/ec2/apis/v1alpha1/resource_tag.go`,
`services/ec2/apis/v1alpha1/default_security_group.go`,
`services/ec2/pkg/resource/default_security_group/{sdk,delta,references}.go`,
`services/ec2/apis/v1alpha1/{default_network_acl,default_route_table,default_vpc}.go`,
`services/ec2/pkg/resource/{default_network_acl,default_route_table,default_vpc}/{sdk,delta,references,conditions}.go`,
`services/ec2/pkg/resource/resource_tag/{sdk,delta,references,sdk_test}.go`,
`services/wafv2/apis/v1alpha1/ack_kinds.go`,
`services/wafv2/pkg/resource/web_acl_association/{hooks,hooks_test}.go`,
`services/wafv2/templates/hooks/web_acl_association/*.go.tpl`,
`services/s3control/templates/hooks/public_access_block/*.go.tpl`,
`services/s3control/pkg/resource/public_access_block/{hooks,custom_delta_test}.go`,
`services/s3control/pkg/endpoint/`,
`services/s3/` (but the copies above, from s3-controller; with
`services/s3/pkg/resource/bucket/encryption_not_found_test.go`),
`services/kms/pkg/extend/`, `services/ec2/pkg/extend/`, `services/cloudtrail/`,
`services/*/cmd/controller/handwritten.go`, `pkg/`, `cmd/operator`,
`tests/`, `hack/`, `config/operator`, `config/fake-aws`.

## Watched pull requests

| PR | Head copied | State (2026-11-08) |
| --- | --- | --- |
| aws-controllers-k8s/iam-controller#184 | `78eb350e` | open, conflicts with base, last updated 2026-07-15; head unchanged |
| aws-controllers-k8s/kms-controller#112 | `eb4057e4` | open, conflicts with base, not approved, last updated 2026-10-18; a maintainer's review reports its calls act on the primary key |
| aws-controllers-k8s/rds-controller#257 | `32c3cb23` | open, conflicts with base, last updated 2026-06-19; head unchanged |

`DBRoleAssociation` copies no pull request: rds-controller has no code,
issue or pull request for `AddRoleToDBCluster` or `AddRoleToDBInstance`
(checked 2026-11-08, `docs/plan.md` section 5).

When a head moves, re-copy and update this table. When a PR merges and
ships, retire our kind (`docs/plan.md`, section 3).
