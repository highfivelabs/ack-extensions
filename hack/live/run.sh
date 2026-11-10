#!/usr/bin/env bash
# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
#
# Run the Cloud Control end-to-end tests twice from the same checkout:
# against the fake (moto), then against the real sandbox account, and write
# a summary for docs/live-runs.md. `task test-live` calls this; task check
# never does (hack/noaws checks that no task it runs reaches hack/live or
# -tags live).
#
#   ACK_EXT_LIVE=create-real-aws-resources \
#     hack/live/run.sh --account ID --role-arn ARN [TEST...]
#
# Refuses unless ACK_EXT_LIVE is exactly that, the account is in
# hack/live/accounts.txt, the credentials are federated (credentials.sh)
# and STS says they are that account's (cloudcontrol/test/live). TEST is a
# go test -run pattern (default: every test in cloudcontrol/test/e2e).
#
# The run's ID (ackx-<UTC MMDDhhmm>-<hex4>) is the value of the tag
# hfl.dev/live-run on everything the manager makes. Before the run,
# leftovers of runs older than 2h are swept; after it, pass or fail,
# everything of this run is swept, and what is left is listed.
#
# Output: offload-artifacts/live/<ID>/{summary.md,fake.log,live.log,left.txt}.
set -o errexit -o nounset -o pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"
# shellcheck source=hack/live/credentials.sh
source hack/live/credentials.sh

account="" role="" tests=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --account) account="$2"; shift 2 ;;
    --role-arn) role="$2"; shift 2 ;;
    -*) echo "live: unknown flag $1" >&2; exit 2 ;;
    *) tests+=("$1"); shift ;;
  esac
done
if [[ "${ACK_EXT_LIVE:-}" != create-real-aws-resources ]]; then
  echo "live: a live run creates real AWS resources; set ACK_EXT_LIVE=create-real-aws-resources to mean it" >&2
  exit 2
fi
grep -qx "${account:-none}" <(sed 's/#.*//; s/[[:space:]]//g' hack/live/accounts.txt) \
  || { echo "live: account '${account}' is not in hack/live/accounts.txt" >&2; exit 2; }
[[ "${role}" == "arn:aws:iam::${account}:role/"* ]] || { echo "live: --role-arn must be a role in ${account}" >&2; exit 2; }
: "${KUBEBUILDER_ASSETS:?run through task test-live, which installs envtest}"
pattern="."
[[ ${#tests[@]} -gt 0 ]] && pattern="^($(IFS='|'; echo "${tests[*]}"))$"

run_id="ackx-$(date -u +%m%d%H%M)-$(od -An -N2 -tx1 /dev/urandom | tr -d ' \n')"
out="offload-artifacts/live/${run_id}"
mkdir -p "${out}"
commit="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
dirty="$(git status --porcelain 2>/dev/null | wc -l | tr -d ' ')"
started="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# The fake run first, with nothing real in its environment.
echo "== live ${run_id}: fake"
fake=PASS
env -u ACK_EXT_LIVE CLOUDCONTROL_E2E_TARGET=fake \
  go test -count=1 -v -timeout 30m -run "${pattern}" ./cloudcontrol/test/e2e/ >"${out}/fake.log" 2>&1 || fake=FAIL
grep -E '^\s*--- (PASS|FAIL|SKIP)|^(ok|FAIL)\s' "${out}/fake.log" || true

live_creds_start "${role}" "$(mktemp -d)"
export LIVE_ACCOUNT_ID="${account}" LIVE_RUN_ID="${run_id}"
SWEEP=(go run -tags live ./hack/live/sweep)
finish() {
  local code=$?
  set +o errexit
  echo "== live ${run_id}: cleaning up"
  "${SWEEP[@]}" --run "${run_id}" || code=1
  "${SWEEP[@]}" || code=1
  "${SWEEP[@]}" --list | tee "${out}/left.txt"
  live_creds_stop
  echo "== live ${run_id}: summary (${out}/summary.md)"
  cat "${out}/summary.md" 2>/dev/null
  exit "${code}"
}
trap finish EXIT

echo "== live ${run_id}: sweeping leftovers older than 2h"
"${SWEEP[@]}"

echo "== live ${run_id}: real account ${account}"
live=PASS
t0=$(date +%s)
CLOUDCONTROL_E2E_TARGET=real \
  go test -tags live -count=1 -v -timeout 90m -run "${pattern}" ./cloudcontrol/test/e2e/ 2>&1 | tee "${out}/live.log" \
  | grep -E --line-buffered '^\s*--- (PASS|FAIL|SKIP)|^(ok|FAIL)\s|_test.go:[0-9]+:|^=== RUN' || live=FAIL
grep -q '^ok' "${out}/live.log" || live=FAIL
{
  echo "Run \`${run_id}\`, account \`${account}\`, commit \`${commit}\` (${dirty} changed files), started ${started}, tests \`${pattern}\`."
  echo
  echo "| Fake | Live | Live time |"
  echo "| --- | --- | --- |"
  echo "| ${fake} | ${live} | $(( $(date +%s) - t0 ))s |"
} >"${out}/summary.md"
[[ "${fake}" == PASS && "${live}" == PASS ]]
