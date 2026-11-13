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
| autoscaling-controller | `v1.5.0` | `56316c75a5da8fdf8079917a16e540be4e84e986` | not a module here: the EC2 Auto Scaling SDK client is at its version (`service/autoscaling` `v1.64.3`) |
| kms-controller | `v1.7.0` | `57250fba83d58aeb84a3ec2770cf3e9b45708895` | Go module: the API types of ACK's `Key` (which `ReplicaKey` references), `KeyMetadata` and `Tag`; the KMS SDK client at its version (`service/kms` `v1.37.14`) |
| ec2-controller | `v1.23.1` | `9303654c2cffad05147e4260105439149f7c81bf` | not a module here (its `pkg/tags` is copied); the EC2 SDK client is at its version (`service/ec2` `v1.290.1`) |
| ecr-controller | `v1.11.1` | `72590cedeb2a4a92d086175762a97aa2ed2bc979` | not a module here: ACK's ECR kinds are left to it (the generator ignores them); the ECR SDK client is at its version (`service/ecr` `v1.55.0`) |
| ecr-controller PR #121 | head | `f6c7d57e0b169547ed2300cbba0c65b11691b0eb` | `ReplicationConfiguration`'s API, behaviour and conversions (open, conflicting, no review, 2026-11-09) |
| eventbridge-controller | `v1.9.2` | `fd084a0bb4d9025f294516d6319b88293be1de8d` | not a module here: the EventBridge SDK client is at its version (`service/eventbridge` `v1.36.7`) |
| secretsmanager-controller | `v1.8.2` | tag | not a module here: the Secrets Manager SDK client the end-to-end tests read a connection's secret with is at its version (`service/secretsmanager` `v1.34.14`) |
| wafv2-controller | `v1.8.1` | `d910e96f94d5b860cffc5dde648e87bafe94689c` | Go module: the API types of ACK's `WebACL`, which `WebACLAssociation` references; the WAFv2 SDK client at its version (`service/wafv2` `v1.55.11`) |
| lambda-controller | `v1.19.0` | `86905357482d01538cb00ab28d59c6af5c46e6f4` | Go module: the API types of ACK's `Function`, which `Permission` references; the Lambda SDK client at its version (`service/lambda` `v1.88.5`) |
| iam-controller | `v1.11.3` | `91ed7f353255e54e2c6372b3ce757bd2e7fae4ad` | Go module: the API types of ACK's `Role` and `Policy`, which our kinds reference |
| organizations-controller | `v1.7.0` | `5824700fd7` | Go module: the API types of ACK's `Account` and `OrganizationalUnit`, which `AccountPlacement`, `DelegatedAdministrator` and `PolicyAttachment` reference (and its CRDs, for the tests); ACK's `Account` and `OrganizationalUnit` are left to it (the generator ignores them); the Organizations SDK client is at its version (`service/organizations` `v1.37.4`) |
| EventBridge Scheduler | none | | no ACK controller (`community` issue #2784); the Scheduler SDK client is `service/scheduler` `v1.12.13`, its version in aws-sdk-go-v2 `v1.34.0`, the release of the models below |
| s3control-controller | `v1.9.0` | `2ee9ccbfaaaa36ad2105143502a902e03906bfd8` | not a module here: ACK's `AccessPoint` is left to it (the generator ignores every S3 Control resource but our `PublicAccessBlock`); the S3 Control SDK client is at its version (`service/s3control` `v1.68.4`) |
| aws-sdk-go-v2 models | `v1.34.0` | tag | the Smithy model the generator reads (`--aws-sdk-go-version`), the version rds-controller `v1.14.2` was generated from |

The runtime, code generator and controllers move together: ACK releases
them in step, and a pin move takes all of them to one ACK release.

## Patches

`patches/code-generator/`, applied in order by `hack/upstream.sh`:

| Patch | What | Upstream? |
| --- | --- | --- |
| `0001-api-group-suffix-flag.patch` | adds `--api-group-suffix` to `ack-generate apis` and `controller`, so kinds generate into `<service>.aws.hfl.dev`; the generator already supports a suffix internally (its Crossplane path uses one) but its ACK commands always pass the default | offered as aws-controllers-k8s/code-generator#762 (2026-11-10), which also passes the flag to `release` |
| `0002-sdk-v2-createvolume-output-shape.patch` | takes EC2's `CreateVolume` off the generator's "shame list" of operations whose output keeps the v1 SDK's shape name: in aws-sdk-go-v2 the output is `CreateVolumeOutput`, and with the list entry the generator writes `*ec2.Volume`, which v2 does not have, so a generated `Volume` does not compile | offered as aws-controllers-k8s/code-generator#763 (2026-11-10), with an assertion in `TestEC2_Volume`; the other EC2 entries (`CreateSnapshot`, `AttachVolume`, `DetachVolume`) are probably the same case and were left alone |
| `0003-slot-guard.patch` | the version-slot guard (`docs/slot-moves.md`): the `main.go` template binds `--slot`, adds the slot tag to ACK's default tags and wraps the resource manager factories with `pkg/slot`; the `registry.go` template adds the RBAC marker for core events (the guard's `SlotMismatch` events) | no: ours alone (it imports `pkg/slot`, an ack-extensions package) |

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
| `services/ec2/pkg/resource/resource_tag/{descriptor,identifiers,manager,manager_factory,resource}.go` | iam-controller PR #184 `pkg/resource/role_policy_attachment/` (the hand-written skeleton, by way of our `volume_attachment` copy) | `78eb350e…` | package, kind, API group, service client, ARN form, adoption fields (`resourceID`, `key`) and the `IsSynced` hook (synced once read) |
| `services/iam/pkg/resource/user_group_membership/{descriptor,identifiers,manager,manager_factory,resource}.go` | iam-controller PR #184 `pkg/resource/role_policy_attachment/` (the hand-written skeleton) | `78eb350e…` | package, kind, ARN form, adoption fields and the `IsSynced` hook (synced once read) |
| `services/iam/pkg/resource/ssh_public_key/{descriptor,identifiers,manager,manager_factory,resource}.go` | iam-controller PR #184 `pkg/resource/role_policy_attachment/` (the hand-written skeleton) | `78eb350e…` | package, kind, ARN form, adoption by key ID and the `IsSynced` hook (synced once read) |
| `services/iam/pkg/resource/account_alias/{descriptor,identifiers,manager,manager_factory,resource}.go` | iam-controller PR #184 `pkg/resource/role_policy_attachment/` (the hand-written skeleton) | `78eb350e…` | package, kind, ARN form, adoption field and the `IsSynced` hook (synced once read) |
| `services/iam/pkg/resource/account_password_policy/{descriptor,identifiers,manager,manager_factory,resource}.go` | iam-controller PR #184 `pkg/resource/role_policy_attachment/` (the hand-written skeleton) | `78eb350e…` | package, kind, ARN form, no adoption fields and the `IsSynced` hook (synced once read) |
| `services/rds/pkg/resource/db_role_association/{descriptor,identifiers,manager,manager_factory,resource}.go` | iam-controller PR #184 `pkg/resource/role_policy_attachment/` (the hand-written skeleton, by way of our `db_proxy_target` copy) | `78eb350e…` | package, kind, API group, RDS client, ARN format, `IsSynced` (synced when the association is ACTIVE), adoption fields (`roleARN`, `featureName`, the cluster or instance) |

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
`services/ec2/pkg/resource/vpn_gateway_route_propagation/{hooks,sdk_test}.go`,
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
`services/ec2/pkg/resource/resource_tag/{sdk,delta,references,sdk_test}.go`,
`services/wafv2/apis/v1alpha1/ack_kinds.go`,
`services/wafv2/pkg/resource/web_acl_association/{hooks,hooks_test}.go`,
`services/wafv2/templates/hooks/web_acl_association/*.go.tpl`,
`services/s3control/templates/hooks/public_access_block/*.go.tpl`,
`services/s3control/pkg/resource/public_access_block/{hooks,custom_delta_test}.go`,
`services/s3control/pkg/endpoint/`,
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
