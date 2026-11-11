#!/usr/bin/env bash
# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
#
# ci.yml's `ci` job, the one status branch protection requires:
#
#   hack/ci/results.sh "RESULT ..."   fail unless every job's result
#                                     is success or skipped
#
# A local run (task ci) needs no such step: the gate's exit status is the
# steps' own.
set -euo pipefail

read -ra results <<<"$*"
[[ ${#results[@]} -gt 0 ]] || { echo "no job results" >&2; exit 1; }
echo "plan, check, e2e: ${results[*]}"
for r in "${results[@]}"; do
  case "${r}" in
    success|skipped) ;;
    *) echo "a job ended ${r}" >&2; exit 1 ;;
  esac
done
