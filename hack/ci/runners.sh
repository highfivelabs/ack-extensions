#!/usr/bin/env bash
# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
#
# Where ci.yml's heavy jobs run (GitHub only: a local run, task ci, runs
# them on one offload VM). Reads the plan and the repository variables
# from the environment, writes check= and e2e= to GITHUB_OUTPUT:
#
#   check  ubuntu-latest, or PREFIX-check (a Spot runner) when SPOT and
#          CHECK_ON_SPOT are true and the plan runs anything
#   e2e    PREFIX-e2e when SPOT is true and the plan says e2e=true, else
#          empty (the job is skipped; the step summary says why)
#
# A pull request from a fork (SAME_REPO not true) is run as if the Spot
# runners were off: the Spot runner steps are skipped for it.
#
# Environment: MODE, E2E (the plan's), SPOT, CHECK_ON_SPOT, SAME_REPO ("true" or not),
# PREFIX (the runner label prefix), GITHUB_OUTPUT, GITHUB_STEP_SUMMARY.
set -euo pipefail

out="${GITHUB_OUTPUT:-/dev/stdout}"
summary="${GITHUB_STEP_SUMMARY:-/dev/stderr}"
check=ubuntu-latest e2e=""
if [[ "${SAME_REPO:-}" != "true" ]]; then
  SPOT=false
  echo "A pull request from a fork: no Spot runners." >> "${summary}"
fi
if [[ "${SPOT}" == "true" ]]; then
  [[ "${E2E}" == "true" ]] && e2e="${PREFIX}-e2e"
  [[ "${MODE}" != "none" && "${CHECK_ON_SPOT}" == "true" ]] && check="${PREFIX}-check"
elif [[ "${E2E}" == "true" ]]; then
  echo "**operator-e2e not run:** the Spot runners are off (repository variable \`SPOT_RUNNERS_ENABLED\`). With them on, the VM \`gh-${PREFIX}-e2e\` would run \`task tools\` and \`task operator-e2e\`." >> "${summary}"
fi
echo "check=${check}" >> "${out}"
echo "e2e=${e2e}" >> "${out}"
