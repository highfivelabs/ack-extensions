#!/usr/bin/env bash
# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
#
# The ACK-kind tests (tests/e2e) on AWS Emulator, judged against the
# tests expected to fail there (docs/aws-emulator.md, "In CI"):
#
#   hack/ci/emulator-ack.sh        task test:aws-emulator:ack runs it,
#                                  with KUBEBUILDER_ASSETS set
#
# Beside moto, not instead of it: task check still runs every test on
# moto. Fails when any test other than the expected failures fails, skips
# or does not finish, when an expected failure passes, or when the
# installed emulator is not the release the list is for
# (hack/emulator-expect). The emulator must be installed
# (hack/aws-emulator.sh, the pinned release).
#
# Every test runs: the judgement needs them all. Environment:
# KUBEBUILDER_ASSETS (required), OUT (default
# offload-artifacts/aws-emulator-ack), TIMEOUT (go test's, default 120m).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
EXPECT=tests/e2e/aws-emulator-expected-failures.yaml
OUT="${OUT:-offload-artifacts/aws-emulator-ack}"
[[ -n "${KUBEBUILDER_ASSETS:-}" ]] || { echo "emulator-ack: KUBEBUILDER_ASSETS is not set (task test:aws-emulator:ack sets it)" >&2; exit 2; }
command -v aws-emulator >/dev/null || { echo "emulator-ack: aws-emulator is not installed (hack/aws-emulator.sh)" >&2; exit 2; }
version="$(aws-emulator -v | awk '/^Version/ {print $2}')"
[[ -n "$version" ]] || { echo "emulator-ack: aws-emulator -v gave no version" >&2; exit 2; }

mkdir -p "$OUT"
go test -list . ./tests/e2e/ >"$OUT/list.txt"

echo "emulator-ack: tests/e2e on AWS Emulator $version; expected failures: $EXPECT"
# go test fails on the expected failures, so its status is not the
# result: emulator-expect reads every test's result from the events.
set +e
ACK_TEST_BACKEND=aws-emulator EMULATOR_TELEMETRY=off \
  go test -json -count=1 -timeout "${TIMEOUT:-120m}" ./tests/e2e/ >"$OUT/events.json"
rc=$?
set -e
echo "emulator-ack: go test exited $rc; events in $OUT/events.json"
go run ./hack/emulator-expect -expect "$EXPECT" -list "$OUT/list.txt" -json "$OUT/events.json" -version "$version"
