#!/usr/bin/env bash
# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
#
# Write docs/third-party-licenses.csv: every Go module linked into the
# managers and the operator, and its licence, as go-licenses reports it.
# Fails if a module's licence is not one the project accepts.
set -o errexit -o nounset -o pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"
GO_LICENSES="${ROOT}/bin/go-licenses"
[[ -x "${GO_LICENSES}" ]] || { echo "bin/go-licenses is missing; run make tools" >&2; exit 1; }
out=docs/third-party-licenses.csv
report="$("${GO_LICENSES}" report ./services/iam/cmd/controller ./services/rds/cmd/controller ./cmd/operator 2>/dev/null)"
{
  echo "module,licence"
  awk -F, '{print $1 "," $NF}' <<<"${report}" | sort -u
} >"${out}"
bad="$(awk -F, 'NR > 1 && $2 !~ /^(Apache-2\.0|MIT|BSD-2-Clause|BSD-3-Clause|ISC|MPL-2\.0)$/' "${out}")"
if [[ -n "${bad}" ]]; then
  echo "licences outside the accepted set:" >&2
  echo "${bad}" >&2
  exit 1
fi
echo "ok: $(($(wc -l <"${out}") - 1)) modules, all Apache-2.0, MIT, BSD, ISC or MPL-2.0 (${out})"
