#!/usr/bin/env bash
# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
#
# Fill this checkout's cache of remote Taskfiles (.task/remote; the factory
# include in Taskfile.yml) one process at a time:
#
#   hack/ci/task-remote.sh [--dir DIR] [--from DIR2]
#
# DIR: the checkout (default this one); DIR2: another checkout whose
# .task/remote to copy in first.
#
# Why: Task (3.54) clones a remote include through one directory per user
# (macOS ~/Library/Caches/task/task-git-repos, Linux
# $XDG_CACHE_HOME/task/task-git-repos), and two processes cloning at once
# delete each other's clone: `task` fails before it runs anything ("fatal:
# Unable to read current working directory"). Measured 2026-11-11: three
# `task --list` at once in a checkout with no cache failed in 5 of 6
# rounds; with the cache filled, 0 of 6. The cache is per checkout
# (.task/remote, which offload packs for its VMs, since they have no
# GitHub credentials), so every new worktree fetches once.
#
# This takes a per-user lock (mkdir: macOS has no flock), so only one
# process fetches at a time, then runs `task --list-all`, which reads the
# cache when it is filled and fetches when not. hack/ci/gate.sh runs it in
# its temporary worktree; run it after making a worktree when several
# tasks will start in it, or in several worktrees, at once. It cannot
# guard a bare `task` started in a checkout with no cache: Task fetches
# while it reads Taskfile.yml, before any task of ours runs.
#
# Environment: TASK (the task binary, default bin/ci/task when installed,
# else task), HFL_TASK_REMOTE_LOCK (the lock directory), and
# HFL_TASK_REMOTE_STALE (seconds after which a lock whose owner cannot be
# found is taken over, default 300).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ROOT="$HERE" from=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dir) ROOT="$(cd "${2:?--dir DIR}" && pwd)"; shift 2 ;;
    --from) from="$(cd "${2:?--from DIR}" && pwd)"; shift 2 ;;
    *) echo "usage: hack/ci/task-remote.sh [--dir DIR] [--from DIR]" >&2; exit 2 ;;
  esac
done
cd "$ROOT"

if [[ -z "${TASK:-}" ]]; then
  if [[ -x "$HERE/bin/ci/task" ]]; then TASK="$HERE/bin/ci/task"; else TASK=task; fi
fi

LOCK="${HFL_TASK_REMOTE_LOCK:-${XDG_CACHE_HOME:-$HOME/.cache}/highfivelabs/task-remote.lock}"
STALE="${HFL_TASK_REMOTE_STALE:-300}"
mkdir -p "$(dirname "$LOCK")"

# mtime SECONDS-SINCE-EPOCH of a path (GNU or BSD stat).
mtime() { stat -c %Y "$1" 2>/dev/null || stat -f %m "$1"; }

waited=0
until mkdir "$LOCK" 2>/dev/null; do
  owner="$(cat "$LOCK/owner" 2>/dev/null || true)"
  host="${owner%% *}" pid="${owner##* }"
  stale=""
  if [[ -n "$owner" && "$host" == "$(hostname)" ]] && ! kill -0 "$pid" 2>/dev/null; then
    stale="its owner, process $pid, is gone"
  elif since="$(mtime "$LOCK" 2>/dev/null)" && (( $(date +%s) - since > STALE )); then
    stale="it is older than ${STALE}s"
  fi
  # Take a stale lock over only if no one else has meanwhile.
  if [[ -n "$stale" && "$(cat "$LOCK/owner" 2>/dev/null || true)" == "$owner" ]]; then
    echo "task-remote: taking over $LOCK: $stale" >&2
    rm -rf "${LOCK:?}"
    continue
  fi
  if (( waited % 10 == 0 )); then echo "task-remote: waiting for $LOCK (${owner:-no owner yet})" >&2; fi
  sleep 1
  waited=$((waited + 1))
done
# shellcheck disable=SC2329 # run by the EXIT trap
release() { rm -rf "${LOCK:?}"; }
trap release EXIT
echo "$(hostname) $$" >"$LOCK/owner"

if [[ -n "$from" && -d "$from/.task/remote" ]]; then
  mkdir -p .task/remote
  cp -pR "$from/.task/remote/." .task/remote/
fi
"$TASK" --list-all >/dev/null
echo "task-remote: $ROOT/.task/remote filled"
