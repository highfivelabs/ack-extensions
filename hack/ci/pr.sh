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

what="${1:?usage: hack/ci/pr.sh check|e2e}"

if [[ -n "${BASE_SHA:-}" ]]; then
  plan="$(hack/ci/select.sh plan "${BASE_SHA}" "${HEAD_SHA:-HEAD}" 2>>"${GITHUB_STEP_SUMMARY:-/dev/stderr}")"
else
  plan=$'mode=full\ngroups=\ne2e=true\nemulator=true'
fi
echo "${plan}"
get() { sed -n "s/^$1=//p" <<<"${plan}" | head -n 1; }

tools() {
  task ci:tools
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
    if [[ "${emulator}" == true ]] && task --list-all 2>/dev/null | grep -q '^\* test:aws-emulator:ack:'; then
      task test:aws-emulator:ack
    fi
    ;;
  *)
    echo "usage: hack/ci/pr.sh check|e2e" >&2
    exit 2
    ;;
esac
