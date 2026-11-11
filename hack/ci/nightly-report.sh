#!/usr/bin/env bash
# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
#
# nightly.yml's report job: on a failed nightly run, open the issue
# labelled ci-nightly, or comment on the open one.
#
# Environment: GH_TOKEN, GH_REPO, LAUNCH and FULL (the jobs' results),
# CHECK and E2E (the steps' outcomes, empty when not run), RUN_URL, SHA.
set -euo pipefail

issue="$(gh issue list --label ci-nightly --state open --json number --jq '.[0].number // empty')"
line="$(date -u +%F) on ${SHA:0:12}: launch ${LAUNCH}, task check ${CHECK:-not run}, task operator-e2e ${E2E:-not run}. ${RUN_URL}"
if [[ -n "${issue}" ]]; then
  gh issue comment "${issue}" --body "Still failing: ${line}"
else
  gh label create ci-nightly --color B60205 --description "The nightly full run on main failed" --force
  gh issue create --label ci-nightly --title "Nightly full run on main is failing" \
    --body "The nightly run of \`task check\` and \`task operator-e2e\` on main failed: ${line}"
fi
