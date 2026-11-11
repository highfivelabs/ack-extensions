# Testing: what proves a kind

Three levels, cheapest first. Every kind has the first; the second where
moto has the service; the third is sampled over time.

| Level | Where | What it proves | Marked |
| --- | --- | --- | --- |
| Schema fake | `task check`, every Cloud Control kind moto lacks (`TestEveryKindOnSchemaFake`) | the CRD is served and accepts an example built from it; the spec reaches Cloud Control as the type's model (names, nesting, Secret references); Cloud Control's own rules (required, read-only, create-only, write-only, asynchronous requests); the identifier and read-only values land in the status; no update on a resync; the delete ends | "unproven on fake" in the coverage data, with the reason |
| moto | `task check`, a backend in `cloudcontrol/test/fake` and a test in `cloudcontrol/test/e2e` (Cloud Control kinds), or `tests/e2e` (ACK-generated kinds) | the above, and what the service does in moto: values it validates, state it keeps, drift made through the service's own API put back | proven on fake (no note) |
| Live | the sandbox account only, sampled (below) | the real service: defaults, validation, eventual consistency, what moto gets wrong | a row in `docs/live-runs.md`'s ledger |

## In `task check`

Every kind is tested on a fake in `task check`, or its CRD says why not.
A Cloud Control type moto has no backend for is marked `unproven:` in
`cloudcontrol/types.yaml` (the batch tool writes the reason from moto's
service list) and runs on the schema fake; one with neither fails the
harness ("no fake backend"). Nothing in a test path can reach AWS
(`pkg/fakeaws`, `task noaws`; `docs/cloudcontrol.md` section 6).

## Live runs

- **Where:** the sandbox account `$AWS_SANDBOX_ACCOUNT_ID` only (`.env.example`),
  as the role `ack-extensions-live-tests`, from offload VMs in
  `$OFFLOAD_PROJECT` started with `--sa "$OFFLOAD_SA"`
  (federation, no stored keys), with `ACK_EXT_LIVE=create-real-aws-resources`.
  Never from the laptop, never with a laptop AWS profile.
- **What:** sampled over time, not every kind on every change. Free kinds
  first (configuration objects: filters, rules, policies, settings), then
  cheap ones; anything billed hourly (transit gateways, endpoints, firewalls)
  only with a reason and its teardown checked. One ledger row per kind per
  run in `docs/live-runs.md`; a kind is "untested on real AWS" until it has
  one.
- **Today:** the deployed role lacks the sweeper's and the fixtures'
  permissions until the builder redeploys the stack from pull request #68
  (register O26b). `hack/live/run.sh` sweeps first, so a run stops before
  any test until then.

## Rechecking the fake against real AWS

A moto backend is a claim about the service. Each live run of a kind
proven on moto is also a recheck of that claim: when the live run and the
fake disagree, the ledger row says how, and the backend or its test is
fixed to match AWS (or the difference is written down where moto cannot
change). Sample at least one moto-proven kind per service each week, the
oldest live row first, so every backend is rechecked over time.
