#!/usr/bin/env bash
# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
#
# task ci:remote-test: N checkouts with no remote-Taskfile cache (temporary
# worktrees of HEAD) fill it at the same moment through
# hack/ci/task-remote.sh (xargs -P N), then run `task --list` at the same
# moment; every one must exit 0. Without the lock, N bare `task --list` in
# such checkouts fail most rounds (hack/ci/task-remote.sh says why).
#
# It fetches the factory include from GitHub with your credentials, so it
# runs on a laptop, not in task check (an offload VM has no credentials).
#
#   hack/ci/task-remote-test.sh [N]   default 3
set -euo pipefail

# One process of a round (the script runs itself through xargs):
#   task-remote-test.sh --one fill|list I
if [[ "${1:-}" == --one ]]; then
  w="$TMP/w$3"
  case "$2" in
    fill) "$ROOT/hack/ci/task-remote.sh" --dir "$w" ;;
    list) cd "$w" && task --list ;;
  esac >"$TMP/out-$2-$3" 2>&1 || { echo "FAIL $3"; exit 0; }
  exit 0
fi

N="${1:-3}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
TMP="$(mktemp -d "${TMPDIR:-/tmp}"/task-remote-test.XXXXXX)"
# shellcheck disable=SC2329 # run by the EXIT trap
cleanup() {
  local i
  for i in $(seq 1 "$N"); do git worktree remove --force "$TMP/w$i" 2>/dev/null || true; done
  rm -rf "${TMP:?}"
}
trap cleanup EXIT
for i in $(seq 1 "$N"); do git worktree add -q --detach "$TMP/w$i" HEAD; done
export TMP ROOT
# A lock of this test's own, so a real fetch elsewhere does not wait on it.
export HFL_TASK_REMOTE_LOCK="$TMP/lock"

# round fill|list: N processes at once (xargs -P, not shell &); prints a
# FAIL line for each that failed, and fails if xargs itself does.
round() { seq 1 "$N" | xargs -P "$N" -I{} "$ROOT/hack/ci/task-remote-test.sh" --one "$1" {}; }
failed="$(round fill)"
[[ -z "$failed" ]] || { echo "task-remote-test: hack/ci/task-remote.sh failed: $failed" >&2; cat "$TMP"/out-fill-* >&2; exit 1; }
failed="$(round list)"
[[ -z "$failed" ]] || { echo "task-remote-test: task --list failed: $failed" >&2; cat "$TMP"/out-list-* >&2; exit 1; }
echo "task-remote-test: $N checkouts with no cache filled it at once, then ran task --list at once: all passed"
