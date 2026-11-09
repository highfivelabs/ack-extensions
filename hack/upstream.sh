#!/usr/bin/env bash
# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
#
# Fetch ACK's code generator at the commit pinned in UPSTREAM.md into
# third_party/code-generator, apply patches/code-generator/ in order, and
# build bin/ack-generate. Only `task generate` needs it: the runtime and
# the service controllers come in as ordinary Go modules (go.mod).
#
#   hack/upstream.sh          fetch if missing, reset to the pin, apply patches, build
#   hack/upstream.sh --check  fail if the checkout is not the pin plus patches
set -o errexit -o nounset -o pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

# The single source of the pin: the "code-generator" row of UPSTREAM.md.
COMMIT="$(sed -n 's/^| code-generator | `[^`]*` | `\([0-9a-f]\{40\}\)`.*/\1/p' UPSTREAM.md | head -1)"
REPO="https://github.com/aws-controllers-k8s/code-generator.git"
DEST="third_party/code-generator"
PATCHES="${ROOT}/patches/code-generator"

[[ -n "${COMMIT}" ]] || { echo "could not read the code-generator commit from UPSTREAM.md" >&2; exit 1; }

if [[ "${1:-}" == "--check" ]]; then
  [[ -d "${DEST}/.git" ]] || { echo "${DEST} is missing; run hack/upstream.sh" >&2; exit 1; }
  head="$(git -C "${DEST}" rev-parse HEAD)"
  [[ "${head}" == "${COMMIT}" ]] || { echo "${DEST} is at ${head}, UPSTREAM.md pins ${COMMIT}" >&2; exit 1; }
  expected="$(mktemp)"; actual="$(mktemp)"
  trap 'rm -f "${expected}" "${actual}"' EXIT
  for p in "${PATCHES}"/*.patch; do
    [[ -e "${p}" ]] || continue
    git -C "${DEST}" apply --numstat "${p}" | awk '{print $3}'
  done | sort -u >"${expected}"
  git -C "${DEST}" status --porcelain --untracked-files=all | awk '{print $2}' | sort -u >"${actual}"
  diff -u "${expected}" "${actual}" || { echo "${DEST} differs from the pin plus patches/" >&2; exit 1; }
  [[ -x bin/ack-generate ]] || { echo "bin/ack-generate is missing; run hack/upstream.sh" >&2; exit 1; }
  echo "ok: ${DEST} is ${COMMIT} plus $(ls "${PATCHES}"/*.patch 2>/dev/null | wc -l | tr -d ' ') patch(es)"
  exit 0
fi

mkdir -p "$(dirname "${DEST}")"
if [[ ! -d "${DEST}/.git" ]]; then
  git init --quiet "${DEST}"
  git -C "${DEST}" remote add origin "${REPO}"
fi
if ! git -C "${DEST}" cat-file -e "${COMMIT}^{commit}" 2>/dev/null; then
  git -C "${DEST}" fetch --quiet --depth 1 origin "${COMMIT}"
fi
git -C "${DEST}" -c advice.detachedHead=false checkout --quiet --force "${COMMIT}"
git -C "${DEST}" reset --quiet --hard "${COMMIT}"
git -C "${DEST}" clean --quiet -fdx
for p in "${PATCHES}"/*.patch; do
  [[ -e "${p}" ]] || continue
  echo "applying $(basename "${p}")"
  git -C "${DEST}" apply --whitespace=nowarn "${p}"
done
(cd "${DEST}" && GOTOOLCHAIN=local go build -tags codegen -o "${ROOT}/bin/ack-generate" ./cmd/ack-generate)
echo "${DEST} is ${COMMIT} plus our patches; bin/ack-generate built"
