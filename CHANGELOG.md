# Changelog

Changes that alter what a kind does on a live account. Newest first. The
design and the reasons are in `docs/plan.md`.

## Unreleased

### Added

- **`s3.aws.hfl.dev` `Bucket`**: ACK s3-controller `v1.15.0`'s `Bucket`,
  the same kind in our group, fixed: ACK's read panics (a nil pointer) on
  a bucket whose `GetBucketEncryption` answers
  `ServerSideEncryptionConfigurationNotFoundError` (S3-compatible stores,
  moto); ours reads it as having no default encryption. An intentional
  overlap with ACK's `Bucket` (`docs/plan.md` section 6.38 and section 8,
  item 15), retired when ACK releases the fix; never point both at one
  bucket. `s3.aws.hfl.dev/force-destroy` works on it as on ACK's. The s3
  manager now needs the S3 actions of a bucket's configuration
  (`task ack:iam`).
- **`ec2.aws.hfl.dev` `DefaultNetworkACL`, `DefaultRouteTable` and
  `DefaultVPC`** (ACKQ16-1; Terraform's `aws_default_network_acl`,
  `aws_default_route_table`, `aws_default_vpc`): take over a VPC's default
  network ACL and main route table (by `vpcID` or `vpcRef`) and the
  region's default VPC (no reference). Entries, subnet associations,
  routes, propagating VPN gateways, DNS settings and tags become the
  spec's, exclusively; nothing is created or deleted.
- **`ec2.aws.hfl.dev` `DefaultSecurityGroup`** (open question 16;
  Terraform's `aws_default_security_group`): takes over a VPC's default
  security group, found by `vpcID` or `vpcRef` (ACK's `VPC`). Its rules
  and tags become the spec's, exclusively: EC2's default egress rule is
  revoked unless listed, and a rule added outside Kubernetes (or by a
  `SecurityGroupIngress`/`SecurityGroupEgress` on the same group) is revoked.
  Deleting the object leaves the group and its rules.
- **References to ACK's ec2 kinds** (open question 16): `VPNGateway.vpcRef`,
  `VPNConnection.transitGatewayRef`, `VPNGatewayRoutePropagation.routeTableRef`,
  `VolumeAttachment.instanceRef`, `Volume.availabilityZoneRef`.
- **Fields ACK's own kinds lack, as annotations on ACK's objects**
  (`docs/extensions.md`; open question 1, answered 2026-11-13): on ACK's
  `Key`, `kms.aws.hfl.dev/rotation-period-in-days` (the rotation period)
  and `kms.aws.hfl.dev/enabled` (enable or disable the key), set by our
  kms manager; on ACK's `Bucket`, `s3.aws.hfl.dev/force-destroy` (empty
  the bucket on delete), by a new S3 manager beside ACK's s3-controller
  (`services/s3`). Every service manager now calls `pkg/extend` (generator
  patch 0004). Notes to upstream drafted in `docs/upstream-notes.md`.
- **`ec2.cloudcontrol.aws.hfl.dev` `SecurityGroupIngress` and
  `SecurityGroupEgress`**: standalone security group rules, Terraform's
  `aws_vpc_security_group_ingress_rule` and `_egress_rule` (`docs/plan.md`
  section 9, question 7), generated from Cloud Control's
  `AWS::EC2::SecurityGroupIngress` and `...Egress`. `groupRef` and the
  peer group's reference name ACK's `SecurityGroup`, the prefix list's
  ACK's `ManagedPrefixList`. A rule on a group whose ACK `SecurityGroup`
  lists rules is revoked at that group's next sync (documented; not
  refused).
- **References on Cloud Control kinds** (`docs/cloudcontrol.md` section
  12, item 4): `ec2.cloudcontrol.aws.hfl.dev` `VPCBlockPublicAccessExclusion`
  takes `vpcRef` and `subnetRef`, `SecurityGroupVpcAssociation` `groupRef`
  and `vpcRef`, and `EIPAssociation` `allocationRef`, `instanceRef` and
  `networkInterfaceRef`, each naming ACK's kind (or our
  `NetworkInterface`) by its Kubernetes name. The ID fields they fill are
  no longer required on their own: one of the ID or its reference is.
- **`s3control.aws.hfl.dev` `PublicAccessBlock`**: the account's S3 Block
  Public Access settings, Terraform's `aws_s3_account_public_access_block`,
  in a new service, `services/s3control` (`docs/plan.md` section 6.36).
- **`ec2.aws.hfl.dev` `ResourceTag`**: one tag on an EC2 resource made
  elsewhere, Terraform's `aws_ec2_tag` (`docs/plan.md` section 6.35).

### Changed

- **`wafv2.aws.hfl.dev` `WebACLAssociation` is generated** (`docs/plan.md`
  section 6.17). Its behaviour is the same, except: `spec.webACLARN` may
  change (the new web ACL is associated, replacing the old); setting both
  `webACLARN` and `webACLRef` is refused at reconcile, not by the CRD;
  the CRD has no `aws`/`hfl` categories or short name, as the other
  generated kinds; `status.ackResourceMetadata.arn` is not set.

### Fixed

- **`iam.aws.hfl.dev` `AccountAlias`: a new object for the account's
  current alias takes it over.** When `CreateAccountAlias` answers
  `EntityAlreadyExists` (an error IAM's API reference lists for it) and
  the account's alias is the one asked for, the object syncs instead of
  going terminal; another account's alias is still terminal
  (`docs/plan.md` section 6.21). Found by aws-emulator; moto accepted it.

- **`rds.aws.hfl.dev` `OptionGroup`: the read works on AWS.** It sent
  `DescribeOptionGroups` with `OptionGroupName` together with
  `EngineName` and `MajorEngineVersion`, a combination AWS's API
  reference rules out; it now names the group only (`docs/plan.md`
  section 6.2). Found by aws-emulator; moto accepted it.

### Changed

- **Cloud Control kinds: no value checks under `status`** (open question
  20, answered 2026-11-11; `docs/plan.md` section 9). A status value
  AWS returns outside the pinned schema (a new enum value, a longer
  string, a number out of range) no longer makes the API server refuse
  the manager's status update. `status` keeps its types and structure;
  `spec` keeps its validation. 103 CRDs changed.

- **`organizations.aws.hfl.dev` `Organization`: `enabledPolicyTypes` and
  `awsServiceAccessPrincipals` are additive** (decision A3,
  2026-11-10; `docs/plan.md` section 6.28). Before, each list was the
  whole truth and anything enabled outside Kubernetes was disabled. Now
  an entry enabled outside Kubernetes is kept and reported in
  `status.unmanagedEnabledPolicyTypes` or
  `status.unmanagedAWSServiceAccessPrincipals`, for Bundt to offer to
  import. The object disables only what it owns
  (`status.ownedEnabledPolicyTypes`,
  `status.ownedAWSServiceAccessPrincipals`): entries it enabled or found
  enabled while listed. A listed entry disabled outside Kubernetes is
  still put back.

  **Upgrading:** an existing object has no owned lists yet. Its first
  reconcile after the upgrade takes on the listed entries that are
  enabled and reports the rest as unmanaged; nothing is disabled.
