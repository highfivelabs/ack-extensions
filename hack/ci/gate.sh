#!/usr/bin/env bash
# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
#
# The merge gate while GitHub Actions is off (the builder, 2026-11-11:
# "run those actions locally (or on the vm) prior to commit; keep the gh
# actions but disable for now").
#
#   hack/ci/gate.sh [SHA]      task ci [SHA=...]; the pre-push hook runs it
#
# For one commit (default HEAD) it runs what .github/workflows/ci.yml's
# pull-request jobs run, then posts the commit status `offload/ci` on that
# commit, with a link to the run's log. A pull request merges only when
# that status on its head is success (docs/testing.md, "The merge gate").
#
# What runs, as ci.yml decides it:
#   plan    hack/ci/select.sh plan BASE SHA, BASE = the merge base with
#           origin/main (CI_BASE=<ref> for a stacked pull request)
#   check   mode=full: task tools, task check; mode=groups: task tools,
#           hack/ci/select.sh run GROUPS; mode=none (docs only): nothing
#   e2e     when the plan says e2e=true: task operator-e2e
# check and e2e run on one offload Spot VM (16 vCPUs, 64 GB; on a quota
# error 8 vCPUs, 32 GB, never smaller), against the repository's fake
# cloud only, as ci.yml's jobs do: no credentials reach a test.
#
# The commit is checked out into a temporary worktree first, so the run
# is of exactly SHA, whatever the working tree holds.
#
# GitHub takes a status only for a commit it has. When SHA is not on
# GitHub yet (the pre-push hook runs before the push), a passing run
# pushes SHA to the branch offload-ci/SHA, posts the status, and deletes
# the branch; a failing run posts nothing there (the push is refused).
#
# Environment: CI_BASE (default origin/main), OFFLOAD (the offload command,
# default ../scripts/offload/offload beside this repository, else on PATH),
# CI_NO_STATUS=1 (run, post nothing).
set -euo pipefail

# A git hook runs with GIT_DIR (and GIT_INDEX_FILE ...) set to this
# repository; a test that runs git in a directory of its own would then
# commit, branch and configure here instead (it did, 2026-11-11). Unset them.
# shellcheck disable=SC2046
unset $(git rev-parse --local-env-vars)

STATUS_CONTEXT=offload/ci
BUCKET=hfl-dev-514205-offload
PROJECT=hfl-dev-514205

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"
die() { echo "gate: $*" >&2; exit 2; }

SHA="$(git rev-parse --verify "${1:-HEAD}^{commit}")" || die "no commit ${1:-HEAD}"
REPO="$(git remote get-url origin | sed -E 's#^(git@github\.com:|https://github\.com/)##; s#\.git$##')"
[[ "$REPO" == */* ]] || die "origin is not a GitHub repository: $(git remote get-url origin)"

if [[ -z "${OFFLOAD:-}" ]]; then
  common="$(cd "$(git rev-parse --git-common-dir)/.." && pwd)"
  if [[ -x "$common/../scripts/offload/offload" ]]; then OFFLOAD="$common/../scripts/offload/offload"
  elif command -v offload >/dev/null; then OFFLOAD="$(command -v offload)"
  else die "offload not found: set OFFLOAD to scripts/offload/offload"; fi
fi

git fetch -q origin main
BASE="$(git merge-base "${CI_BASE:-origin/main}" "$SHA")" || die "no merge base of ${CI_BASE:-origin/main} and $SHA"

TMP="$(mktemp -d "${TMPDIR:-/tmp}"/gate.XXXXXX)"
SRC="$TMP/src"
# shellcheck disable=SC2329 # run by the EXIT trap
cleanup() {
  git worktree remove --force "$SRC" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT
git worktree add -q --detach "$SRC" "$SHA"

# The plan, as ci.yml's plan job makes it.
(cd "$SRC" && hack/ci/select.sh plan "$BASE" "$SHA") >"$TMP/plan" 2>"$TMP/summary" \
  || { cat "$TMP/summary" >&2; die "hack/ci/select.sh plan failed"; }
cat "$TMP/summary"
mode="$(sed -n 's/^mode=//p' "$TMP/plan")"
groups="$(sed -n 's/^groups=//p' "$TMP/plan")"
e2e="$(sed -n 's/^e2e=//p' "$TMP/plan")"
echo "gate: $REPO $SHA against $BASE: mode=$mode${groups:+ groups=$groups} e2e=$e2e"

start=$SECONDS
url="" machine="" rc=0
case "$mode" in
  none)
    what="docs only (select.sh plan): nothing to run" ;;
  full|groups)
    if [[ "$mode" == full ]]; then check="task check"; what="task check"
    else check="hack/ci/select.sh run $groups"; what="check:static, build, tests of $groups"; fi
    steps=(--prep "task tools" --step-skippable "$check")
    if [[ "$e2e" == true ]]; then steps+=(--step "task operator-e2e"); what="$what, operator-e2e"; fi
    set +e
    OFFLOAD_FALLBACK="${OFFLOAD_FALLBACK-8}" "$OFFLOAD" run --dir "$SRC" "${steps[@]}" </dev/null 2>&1 | tee "$TMP/log"
    rc=${PIPESTATUS[0]}
    set -e
    id="$(sed -n -E 's/^offload: (off[a-z]*-[^ ]+) \(.*/\1/p' "$TMP/log" | head -n 1)"
    machine="$(sed -n -E 's/.*VM [^ ]+: ([a-z0-9-]+) in .*/\1/p' "$TMP/log" | tail -n 1)"
    [[ -n "$id" ]] && url="https://console.cloud.google.com/storage/browser/$BUCKET/runs/$id?project=$PROJECT" ;;
  *) die "hack/ci/select.sh plan gave mode '$mode'" ;;
esac
mins=$(( (SECONDS - start + 59) / 60 ))

if [[ $rc -eq 0 ]]; then state=success; verb=passed; else state=failure; verb="failed (exit $rc)"; fi
desc="$what: $verb${machine:+ on $machine} in ${mins}m"
desc="${desc:0:140}"
echo "gate: $state: $desc${url:+ ($url)}"

post() {
  gh api "repos/$REPO/statuses/$SHA" -f state="$state" -f context="$STATUS_CONTEXT" \
    -f description="$desc" ${url:+-f target_url="$url"} --silent
  echo "gate: posted $STATUS_CONTEXT=$state on $SHA"
}
if [[ "${CI_NO_STATUS:-}" == 1 ]]; then
  echo "gate: CI_NO_STATUS=1: no status posted"
elif gh api "repos/$REPO/commits/$SHA" --silent 2>/dev/null; then
  post
elif [[ $rc -eq 0 ]]; then
  # Not on GitHub yet: put it there on a branch of its own (the hook
  # passes this push through: OFFLOAD_CI_VERIFIED), post, delete the branch.
  OFFLOAD_CI_VERIFIED="$SHA" git push -q origin "$SHA:refs/heads/offload-ci/$SHA"
  post
  OFFLOAD_CI_VERIFIED="$SHA" git push -q origin --delete "offload-ci/$SHA"
else
  echo "gate: $SHA is not on GitHub and failed: no status posted"
fi
exit "$rc"
