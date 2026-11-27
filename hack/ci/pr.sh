#!/usr/bin/env bash
# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
#
# What CI runs on a pull request or a push to main: the check and e2e
# commands of .github/workflows/hfl-ci.yml (highfivelabs/krm-ci's operator
# profile).
#
#   hack/ci/pr.sh check   task ci:check for the plan
#   hack/ci/pr.sh e2e     task operator-e2e (and the emulator test, where
#                         the plan has one) when the plan asks for it
#
# The plan is hack/ci/select.sh's for BASE_SHA...HEAD_SHA, which krm-ci.yml
# sets on a pull request; without BASE_SHA (a push to main, or by hand)
# everything runs. Tools are installed only when something runs, so a
# docs-only pull request costs a checkout.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
# The pinned tools (hack/ci/tools.sh) first on PATH. The Taskfile's env
# PATH does not reach the tasks' commands (Task keeps the PATH it was
# started with), so a runner without ko on its image needs this.
PATH="$(hack/ci/tools.sh --dir):${PATH}"
export PATH

what="${1:?usage: hack/ci/pr.sh check|e2e}"

if [[ -n "${BASE_SHA:-}" ]]; then
  plan="$(hack/ci/select.sh plan "${BASE_SHA}" "${HEAD_SHA:-HEAD}" 2>>"${GITHUB_STEP_SUMMARY:-/dev/stderr}")"
else
  plan=$'mode=full\ngroups=\ne2e=true\nemulator=true'
fi
echo "${plan}"
get() { sed -n "s/^$1=//p" <<<"${plan}" | head -n 1; }

# The pinned tools first on PATH, here in the shell: Task does not let a
# Taskfile's env override a PATH already in the environment, and a
# GitHub-hosted runner has kind, kubectl and helm but no crane (HFL-731).
tools() {
  task ci:tools
  PATH="$(hack/ci/tools.sh --dir):${PATH}"
  export PATH
  task tools
}

case "${what}" in
  check)
    mode="$(get mode)"
    if [[ "${mode}" == none ]]; then
      echo "pr.sh: documentation only; nothing to check"
      exit 0
    fi
    tools
    task ci:check MODE="${mode}" GROUPS="$(get groups)"
    ;;
  e2e)
    e2e="$(get e2e)"
    emulator="$(get emulator)"
    if [[ "${e2e}" != true && "${emulator}" != true ]]; then
      echo "pr.sh: these paths need no end-to-end test"
      exit 0
    fi
    tools
    if [[ "${e2e}" == true ]]; then
      if ! task operator-e2e; then
        task operator-e2e:debug || true
        exit 1
      fi
    fi
    # The task list is read whole before grep, so grep -q's early exit
    # cannot kill the writer (SIGPIPE under pipefail).
    if [[ "${emulator}" == true ]] && grep -q '^\* test:aws-emulator:ack:' <<<"$(task --list-all 2>/dev/null)"; then
      task test:aws-emulator:ack
    fi
    ;;
  *)
    echo "usage: hack/ci/pr.sh check|e2e" >&2
    exit 2
    ;;
esac
