#!/usr/bin/env bash
# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
#
# What a pull request must run, from the paths it changes (the builder,
# 2026-11-10: the whole suite nightly, otherwise only what changed).
#
#   hack/ci/select.sh plan BASE HEAD   the plan for git diff BASE...HEAD
#   hack/ci/select.sh plan-files       the plan for the paths on stdin
#   hack/ci/select.sh run GROUP...     check:static, build, the groups' tests
#
# The plan, as GITHUB_OUTPUT lines:
#   mode=none    docs only: nothing heavy runs
#   mode=groups  check:static, build and the tests of the groups only
#   mode=full    task check
#   groups=...   the groups (mode=groups)
#   e2e=true     task operator-e2e runs too (on a Spot runner)
#
# Each path is one of:
#   docs      *.md, docs/, LICENSE, NOTICE; UPSTREAM.md unless a row
#             with a commit (the pin) changed
#   group G   services/G/ (service G: ./services/G/... and the tests in
#             tests/e2e/ files that import it); tests/e2e/F_test.go with
#             tests (group e2e:F: that file's tests); cloudcontrol/ and
#             hack/cloudcontrol-gen/ (group cloudcontrol); hack/coverage/
#             (group coverage)
#   adjacent  what a service's pull request also touches and check:static
#             or the operator e2e covers: config/crds/, config/rbac/,
#             tests/operator/
#   full      everything else: go.mod, patches/, the pin, upstream.yaml,
#             cmd/, pkg/ (the operator, the fake AWS), hack/, a tests/e2e/
#             file without tests (the harness), Taskfile.yml, taskfiles/,
#             config/ (the rest), .github/
# A pull request is full if any path is; else groups if any path is a
# group or adjacent; else none. The nightly run (nightly.yml) runs
# everything on main, which catches what a group's tests miss.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
MODULE="$(sed -n "s/^module //p" go.mod)"

# classify PATH: prints "docs", "adjacent", "full" or "group G".
classify() {
  local p="$1" g
  case "$p" in
    # UPSTREAM.md holds the pin (a table row with a commit) and prose that
    # a kind's pull request updates; only a changed pin row is full.
    UPSTREAM.md) if [[ "${PIN_CHANGED:-1}" == 1 ]]; then echo full; else echo docs; fi ;;
    *.md|docs/*|LICENSE|NOTICE) echo docs ;;
    services/*/*)
      g="${p#services/}"; echo "group ${g%%/*}" ;;
    tests/e2e/*_test.go)
      if [[ -f "$p" ]] && grep -qE '^func Test[A-Za-z0-9_]*\(t \*testing\.T\)' "$p"; then
        g="${p#tests/e2e/}"; echo "group e2e:${g%_test.go}"
      else
        echo full
      fi ;;
    cloudcontrol/*|hack/cloudcontrol-gen/*) echo "group cloudcontrol" ;;
    hack/coverage/*) echo "group coverage" ;;
    config/crds/*|config/rbac/*|tests/operator/*) echo adjacent ;;
    *) echo full ;;
  esac
}

# pkgs GROUP: the group's Go packages (tested whole).
pkgs() {
  case "$1" in
    e2e:*) ;;
    cloudcontrol) echo ./cloudcontrol/... ./hack/cloudcontrol-gen/... ;;
    coverage) echo ./hack/coverage/... ;;
    *) [[ -d "services/$1" ]] && echo "./services/$1/..." ;;
  esac
  return 0
}

# e2e_tests GROUP: the tests/e2e tests of the group (names, one a line).
e2e_tests() {
  local files
  case "$1" in
    e2e:*) files="tests/e2e/${1#e2e:}_test.go" ;;
    cloudcontrol|coverage) return 0 ;;
    *) files="$(grep -lF "\"$MODULE/services/$1/" tests/e2e/*_test.go 2>/dev/null || true)" ;;
  esac
  [[ -n "$files" ]] || return 0
  # shellcheck disable=SC2086
  sed -nE 's/^func (Test[A-Za-z0-9_]*)\(t \*testing\.T\).*/\1/p' $files
}

plan() {
  local p c mode=none groups="" full="" lines=""
  while IFS= read -r p; do
    [[ -n "$p" ]] || continue
    c="$(classify "$p")"
    lines="$lines$c	$p
"
    case "$c" in
      full) full=1 ;;
      group*) groups="$groups ${c#group }"; [[ "$mode" == none ]] && mode=groups ;;
      adjacent) [[ "$mode" == none ]] && mode=groups ;;
    esac
  done
  [[ -n "$full" ]] && mode=full
  groups="$(echo "$groups" | tr ' ' '\n' | sed '/^$/d' | sort -u | tr '\n' ' ' | sed 's/ $//')"
  [[ "$mode" == groups ]] || groups=""
  local e2e=false
  [[ "$mode" == none ]] || e2e=true
  echo "mode=$mode"
  echo "groups=$groups"
  echo "e2e=$e2e"
  {
    echo "### What this pull request runs: $mode${groups:+ ($groups)}"
    echo
    echo "| Class | Paths | Example |"
    echo "| --- | --- | --- |"
    printf '%s' "$lines" | awk -F'\t' 'NF == 2 { n[$1]++; if (!($1 in ex)) ex[$1] = $2 } END { for (c in n) printf "| %s | %d | `%s` |\n", c, n[c], ex[c] }' | sort
  } >&2
}

run_groups() {
  [[ $# -gt 0 ]] || echo "select.sh run: no groups; running check:static and build only"
  local g units="" tests=""
  for g in "$@"; do
    units="$units $(pkgs "$g")"
    tests="$tests $(e2e_tests "$g" | tr '\n' ' ')"
  done
  units="$(echo "$units" | xargs)"
  tests="$(echo "$tests" | tr ' ' '\n' | sed '/^$/d' | sort -u | paste -sd'|' -)"
  task check:static
  task build
  if [[ -n "$units" ]]; then
    echo "select.sh: the packages of $*: $units"
    task test:pkgs PKGS="$units"
  fi
  if [[ -n "$tests" ]]; then
    echo "select.sh: the end-to-end tests of $*: $tests"
    task test:pkgs PKGS=./tests/e2e RUN="^($tests)\$"
  fi
}

case "${1:-}" in
  plan)
    [[ $# -eq 3 ]] || { echo "usage: hack/ci/select.sh plan BASE HEAD" >&2; exit 2; }
    # A diff that fails must fail the plan, never read as "nothing changed".
    files="$(git diff --name-only --no-renames "$2...$3")"
    pin="$(git diff "$2...$3" -- UPSTREAM.md)"
    PIN_CHANGED=0
    if grep -qE '^[-+][|].*[0-9a-f]{40}' <<<"$pin"; then PIN_CHANGED=1; fi
    PIN_CHANGED="$PIN_CHANGED" plan <<<"$files" ;;
  plan-files) plan ;;
  run) shift; run_groups "$@" ;;
  tests) shift; for g in "$@"; do e2e_tests "$g"; done ;;
  *) echo "usage: hack/ci/select.sh plan BASE HEAD | plan-files | run GROUP... | tests GROUP..." >&2; exit 2 ;;
esac
