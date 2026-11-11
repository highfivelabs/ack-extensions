# Throughput: why generation was not further along, and what changed

Written 2026-11-11 after the builder: "I would have expected the machinery
to generate code here to get further along." Numbers are from `git log`,
`gh pr list`, `cloudcontrol/readiness.csv` and `docs/coverage/` at main
`044c3ea`.

## Where it stood

| | |
| --- | --- |
| Kinds of ours | 85: 34 ACK-generated (`*.aws.hfl.dev`), 51 Cloud Control (`*.cloudcontrol.aws.hfl.dev`) |
| Cloud Control kinds proven on moto | 22 (one has a test double: `LandingZone`) |
| Cloud Control kinds with no test at all | 29 ("unproven on fake": one line in `types.yaml`, a CRD and nothing else) |
| Foundation and security gap left (no ACK kind, none of ours) | 219 types; 197 provisionable through Cloud Control, 195 of them with a clean CRD |
| Coverage, resources ours beyond ACK | 74 of 1,875 (platform-foundation 35 of 241, security-adjacent 30 of 112) |

The Cloud Control generator was never the limit: a type is one line in
`cloudcontrol/types.yaml`, no Go. 195 more types would generate in one
`task cloudcontrol:generate`.

## Where the time went

- **Authoring: about six types an hour.** Batches 1 to 8 (46 types) were
  written by one agent between 00:54 and 08:31 on 2026-11-10. Most of that
  went into what a type needs to be *proven*: a hand-written moto backend
  translating the CloudFormation model into the service's API (about 2,100
  lines for 22 types; `moto_config.go` alone is 679 lines for six), an
  end-to-end test (about 50 lines a type), and probing moto first.
- **The merge queue: 11 to 23 hours a batch.** Median time from pull
  request to merge over the last 60 is 0.8 hours, but the Cloud Control
  batches opened after 07:00 on 2026-11-10 waited 11.3 h (GuardDuty), 11.7 h
  (Security Hub), 14.7 h (DNS), 22.9 h (LandingZone) and 23.0 h (Secrets
  Manager): one coordinator merged them one at a time.
- **Conflicts on every merge.** Each batch touches the same generated and
  hand-kept files: the readiness survey, the coverage data and README
  section, the CRD list in `tests/operator/run.sh` (twice), the plan's
  status line. 77 "merge main into" commits on main's history; the DNS
  branch merged main six times before it landed. Each merge is a
  regenerate and a full check again (13 to 15 minutes).
- **Decisions redone per type.** Whether a type is a field of an ACK kind,
  waits on an upstream pull request, or is not foundation at all (Outposts,
  physical circuits) was worked out in prose per batch and never written as
  data, so the next agent read it again.
- **A cliff ahead.** The live-test role's policies were 7 of IAM's 10
  managed policies per role at 51 types; at about 70 types
  `task cloudcontrol:generate` would have failed outright.

So the queue of 195 clean types was not blocked by the generator but by
the bar a type had to clear (a moto backend, or nothing), one service per
pull request, and a serial human merge.

## What changed (this pull request and the next)

1. **The schema fake** (`cloudcontrol/test/fake/schemastore.go`): a backend
   for any type made from its registry schema alone. Every type moto lacks
   now runs on it instead of having no test.
2. **One generated end-to-end test for all of them**
   (`TestEveryKindOnSchemaFake`): an example object from each CRD (every
   required field, the least valid value, Secrets for Secret references),
   synced and ready with its identifier, the model Cloud Control received
   equal to the spec, no update on resyncs, the delete carried through. No
   per-type code: a type added to `types.yaml` is tested at once in
   `task check`. It proves our side; the kinds stay "unproven on fake" in
   the coverage data until a moto backend or a live run proves the service's.
3. **The batch tool** (`hack/cloudcontrol-batch`): adds every gap type of
   the services named, provisionable and clean, marked unproven with what
   moto has of the service (`moto-services.txt`), and prints the
   write-only strings that are candidates for `secrets:`.
4. **Exclusions as data** (`cloudcontrol/excluded.yaml`): the types never to
   add, each with its reason (a field of an ACK kind, an upstream pull
   request, not foundation). The batch tool skips them.
5. **No hand list per type**: `tests/operator/run.sh` reads the Cloud
   Control CRDs from the generated `cloudcontrol/config/crds/kustomization.yaml`.
6. **The live role compacted** (`iamgen.Compact`): the same allowed actions
   in one statement per service, 43,966 characters down to 11,952 at 51
   types (applied to the live template once pull request #68 is in).
7. **Self-merge on green** (the builder, 2026-11-11): batch pull requests
   are merged by the agent that wrote them once CI passes, a service group
   or several per pull request.

Writing a moto backend stays worth it where moto has the service and the
type matters (it proves the service's behaviour); it is now an upgrade to a
tested kind, not the price of adding one.

## Results

Filled in as batches land (types, coverage before and after).

| Batch | Pull request | Types | Resources ours beyond ACK (foundation + security) |
| --- | --- | ---: | --- |
| before | | 51 Cloud Control | 74 (foundation 35 of 241, security 30 of 112) |
| 9, security | #71 | 46 | 120 (security 76 of 112) |
| 10, foundation beyond EC2 | #72 | 25 | 145 |
| 11, EC2 networking | #73 | 66 | 211 (foundation 126 of 241) |
| 12, logging and identity | #74 | 15 | 226 |
| pins at the 2026-11-10 ACK releases | #75 | | 226 (denominator 1,878) |
| 13, application tier | #76 | 89 | 315 (application 113 of 263) |

Then five kinds moved from the schema fake to proven on moto (#77, #78):
ECR `RegistryPolicy` and `RegistryScanningConfiguration`, Identity Store
`Group` and `GroupMembership`, EC2 `NetworkInterface`.

Two limits reached on 2026-11-11, for the builder:

- the live-test role holds 10 managed policies, IAM's default maximum
  (plan question 21): no type is added until it is answered. Answered the
  same day: the role holds the kinds sampled on live runs only
  (`liveKinds` in `cloudcontrol/types.yaml`), one managed policy, and
  types can be added again;
- GitHub Actions stopped starting jobs ("recent account payments have
  failed or your spending limit needs to be increased"): #76 to #78
  passed on an offload VM (`task check`, then `task cloudcontrol:check`
  after fixes; the run IDs are in each PR) and wait for CI to merge.
  Actions were then turned off, and the merge gate became the
  `offload/ci` status that `task ci` posts (#80, docs/testing.md).

Written in one session on 2026-11-11: 152 types in four pull requests,
against 51 in the two days before. What is left of the foundation and
security gap is in `cloudcontrol/excluded.yaml` (each with its reason),
not provisionable through Cloud Control, or has a lossy CRD
(`AWS::CloudTrail::ResourcePolicy`, `AWS::Organizations::ResourcePolicy`).
