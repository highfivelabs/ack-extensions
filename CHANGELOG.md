# Changelog

Changes that alter what a kind does on a live account. Newest first. The
design and the reasons are in `docs/plan.md`.

## Unreleased

### Added

- **`ec2.aws.hfl.dev` `ResourceTag`**: one tag on an EC2 resource made
  elsewhere, Terraform's `aws_ec2_tag` (`docs/plan.md` section 6.35).

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
