#!/usr/bin/env bash
# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
#
# Regenerate docs/coverage/ and the README's Coverage section from the
# pinned inputs in docs/coverage/sources.yaml (see docs/coverage/README.md).
#
#   hack/coverage/run.sh               regenerate
#   hack/coverage/run.sh --check       fail if the committed outputs are stale
#   hack/coverage/run.sh --update-pins move the pins to the latest ACK releases and CloudFormation schemas, then regenerate
set -o errexit -o nounset -o pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"
export GOTOOLCHAIN="${GOTOOLCHAIN:-local}"
exec go run ./hack/coverage -root "${ROOT}" "$@"
