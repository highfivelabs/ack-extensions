# Moving resources between version slots

Hosted Bundt runs its infrastructure clusters as **version slots**
(for example `current` and `previous`), each running one version of every
provider's managers. An organisation moves from one slot to another with
one sequence for every provider:

1. **pause and release** on the old slot,
2. **adopt** on the new slot,
3. **verify** the new slot has no diff,
4. **delete the old object** (the cloud resource stays).

Bundt drives the sequence with ACK's own annotations, so our managers and
ACK's are moved the same way. This page says what our managers support,
the **slot guard** we add (a soft lease that keeps two slots from writing
one resource), and how it is tested. Code: `pkg/slot`, wired into every
manager's `main` (patch `patches/code-generator/0003-slot-guard.patch` for
the ACK-generated managers; `cloudcontrol/cmd/manager/main.go`).

## The annotations (ACK's, unchanged)

| Step | Annotation on the object | What the manager does |
| --- | --- | --- |
| release | `services.k8s.aws/deletion-policy: retain` | deleting the object removes the finalizer and leaves the AWS resource |
| adopt | `services.k8s.aws/adoption-policy: adopt-or-create` (or `adopt`) and `services.k8s.aws/adoption-fields: {"<id field>": "<id>"}` | finds the existing resource by its identifier and manages it; with `adopt-or-create` the spec is the object's, with `adopt` it is filled from AWS |
| pause | `services.k8s.aws/read-only: "true"` | reads only: no create, update or delete; status keeps updating |

All three come from ACK's runtime (`github.com/aws-controllers-k8s/runtime`
`v0.67.0`, `pkg/runtime/reconciler.go`: `reconcile`, `Sync`,
`handlePopulation`, `getDeletionPolicy`), which every manager here is
built on, Cloud Control's included. The feature gates `ResourceAdoption`
and `ReadOnlyResources` are Beta and on by default at that version; we do
not turn them off.

### Audit, per kind family (2026-11-12)

| Family | Release (`deletion-policy: retain`) | Adopt (`adoption-policy`, `adoption-fields`) | Pause (`read-only`) |
| --- | --- | --- | --- |
| ACK-generated managers (`services/*`, 34 kinds, Organizations included) | had: runtime `reconcile` → `setResourceUnmanaged`, no `rm.Delete` | had: runtime `handlePopulation` → each kind's generated `PopulateResourceFromAnnotation` (`services/<svc>/pkg/resource/<kind>/resource.go`); the identifier fields per kind are in that function | had: runtime `Sync` skips `EnsureTags`, create and update |
| Cloud Control manager (`cloudcontrol/`, 292 kinds) | had: the same runtime path; types marked `neverDelete` are always retained (`manager.Delete`) | had: `PopulateResourceFromAnnotation` in `cloudcontrol/pkg/resource/resource.go` takes the primary identifier's fields or `identifier` | had: the same runtime path |
| Organizations kinds (organization-scoped install, `docs/plan.md` 7.5) | had (ACK-generated) | had; `Organization` adopts the account's organization by design | had |

Proven offline before this change: retain (`TestCustomerGateway`,
`TestVolume`, `TestTransitGatewayRouteTable`, others), adopt
(`TestTransitGatewayRouteTable`, `TestOrganization`). Added by this change:
pause, adopt-or-create and release together as a move (`TestSlotMove`,
below). Gaps: none in the annotations. The runtime does not reconcile on
an annotation change (it filters events on generation), so a pause or
release takes effect at the next resync (`--reconcile-default-resync-seconds`);
Bundt waits for it (below).

## The slot guard

Neither ACK nor Cloud Control checks who owns a resource: two managers
adopting the same resource both write it. The guard is an opt-in soft
lease, a tag on the AWS resource.

- **Turn it on**: `--slot=<name>` on a manager, or `HFL_SLOT=<name>` in its
  environment (the flag wins). The name is a DNS label (lower-case
  letters, digits, `-`, at most 63). Unset: nothing changes.
- **The lease**: the AWS tag `hfl.dev/slot=<slot>`. The manager adds it to
  ACK's default tags (`--resource-tags`), so it is written wherever ACK
  writes its default tags: on create, and on the first update after an
  adoption. The object is labelled `hfl.dev/slot=<slot>` when the manager
  creates or updates the resource.
- **The check**: after every read, and again before every update and
  delete, the manager reads `hfl.dev/slot` from the observed resource.
  - no tag, or this slot: go on (an untagged resource is claimed: the
    next write tags it);
  - another slot, and the object's annotation `hfl.dev/slot-takeover`
    names that slot: go on (the move; the write re-tags it as this slot's);
  - another slot otherwise: **refuse**. No AWS write or delete;
    `ACK.Terminal` True with reason `SlotMismatch` and message
    `cloud resource is held by slot <holder>; this controller is slot <this>`
    (so `Ready` is False); a Warning event, reason `SlotMismatch`, same
    message, on the object. The object is read again every 30 seconds
    (`slot.RetryAfter`): ACK does not resync a terminal object or
    reconcile on an annotation change, and the ways out of a refusal are
    annotation changes. A refused delete keeps the finalizer, so the
    object stays until it is given `deletion-policy: retain`.
- **The takeover annotation**, `hfl.dev/slot-takeover: <old slot>`, is
  ours (not ACK's): the explicit hand-over that lets the new slot write a
  resource the old one holds. It is needed only during a move and may be
  removed afterwards.

### Kinds the guard cannot cover

The guard reads the tag from what the manager reads back, so it covers a
kind only when the kind has tags and its read returns them.

- **ACK-generated**: guarded are the 8 kinds in `slot.TaggedKinds`
  (`CustomerGateway`, `Volume`, `VPNConnection`, `VPNGateway` (ec2);
  `ReplicaKey` (kms); `Policy` (organizations); `OptionGroup` (rds);
  `ScheduleGroup` (scheduler)); `TestTaggedKinds` keeps the list equal to
  the generated reads. The other 26 are unguarded: they have no tags in
  AWS (attachments, associations, permissions, account settings:
  `RolePolicyAttachment`, `UserGroupMembership`, `LoginProfile`,
  `SSHPublicKey`, `AccessKey`, `AccountAlias`, `AccountPasswordPolicy`,
  `VolumeAttachment`, `NetworkInterfacePermission`,
  `VPNGatewayRoutePropagation`, `Permission`, `WebACLAssociation`,
  `DBProxyTarget`, `DBRoleAssociation`, `PolicyAttachment`,
  `DelegatedAdministrator`, `AccountPlacement`, `Organization`,
  `ReplicationConfiguration`, the two `ScheduledAction`s,
  `ScalingPolicy`, `Schedule`), or we do not manage their tags
  (`VirtualMFADevice`; `Connection` and `APIDestination` have none in
  EventBridge's API).
- **Cloud Control**: guarded are the kinds whose schema has a top-level
  Key/Value tag list (151 of 292 at the pin), the list `EnsureTags` writes
  ACK's default tags into. The other 141 are in
  `cloudcontrol/slot-untagged.txt` (`TestSlotUntaggedList` keeps it
  current).

**What Bundt must do for an unguarded kind**: the manager cannot tell
whose it is, so the sequence alone keeps one writer. Pause the old
object (`read-only: "true"`) and wait one resync before the new slot
adopts; delete the old object (with `retain`) before taking the pause off
anything; never leave two unpaused objects for one resource. Most
unguarded kinds hang off a guarded parent (an attachment of a role, a
route of a table), so moving the parent first and the children with it
keeps the window small.

## The move, as Bundt runs it

For each object, old slot `blue` to new slot `green`:

1. On `blue`'s object: `services.k8s.aws/read-only: "true"` and
   `services.k8s.aws/deletion-policy: retain`. Wait one resync.
2. On `green`: the same spec, with
   `services.k8s.aws/adoption-policy: adopt-or-create`,
   `services.k8s.aws/adoption-fields: {"<id field>": "<id>"}` (the
   identifier from `blue`'s status) and `hfl.dev/slot-takeover: blue`.
3. Wait for `green`'s object `ACK.ResourceSynced` True; the resource is
   now tagged `hfl.dev/slot=green`. Check its identifier and, where AWS
   keeps one, its creation time are unchanged, and that `green` makes no
   further update (no diff).
4. Delete `blue`'s object. It goes (retain); the resource stays.

From step 2 on, `blue` is refused if anything unpauses it.

Note: ACK's other default tag, `services.k8s.aws/namespace`, changes to
`green`'s namespace on the first update, as it would on any adoption.

## Tests

`task test:slot-move` runs, offline (envtest, moto, the fake Cloud
Control), with no AWS credentials:

- `pkg/slot`: the flag and `HFL_SLOT`, the default tag, the slot name,
  the guard's decisions (claim, own slot, other slot refused with the
  condition, event and requeue, takeover), the reference ensurer kept,
  `TaggedKinds` equal to the generated reads;
- `cloudcontrol/pkg/resource`: `SlotTags` and the untagged list;
- `tests/e2e` `TestSlotMove`: two runs of the ec2 manager as two slots
  (`--slot blue` watching one namespace, `HFL_SLOT=green` another), one
  moto, for `Volume` and `CustomerGateway`: the four steps above, the
  same ID, creation time (Volume) and count; `green` labelled and the tag
  moved; `blue` unpaused and changed is refused (condition, event, AWS
  unchanged); `blue`'s delete refused, then with retain its object goes
  and the resource stays; a new `blue` adoption refused; `green` deletes
  it;
- `cloudcontrol/test/e2e` `TestSlotMove`: the same for
  `TransitGatewayRouteTable`, with two Cloud Control managers (each as its
  environment's restricted identity), counting Cloud Control's
  `UpdateResource` and `DeleteResource` calls to show `blue` makes none
  and `green` settles with none.

`task test` and `task check` run them too.

Not tested: two different manager versions as the two slots (both slots
run the same binary); a live move against real AWS (none run yet; the
sandbox account's rules in `docs/live-runs.md` apply).

## Not done

- The operator does not set the slot: `ACKContext` and
  `CloudControlContext` have no slot field. A slot's managers get
  `HFL_SLOT` from their Deployment (or `--slot`) until they do.
- The guard trusts the tag: anyone able to tag the resource can move the
  lease. It is a guard against two of our own slots, not a security
  boundary.
