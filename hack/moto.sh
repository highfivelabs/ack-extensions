#!/usr/bin/env bash
# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
#
# Install the fake AWS (moto in server mode, the version in
# hack/moto-requirements.txt) into .cache/moto-venv. The tests start
# .cache/moto-venv/bin/moto_server on a free port (pkg/test/harness).
set -o errexit -o nounset -o pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"
want="$(grep -v '^#' hack/moto-requirements.txt | sed -n 's/.*==//p')"
if [[ -x .cache/moto-venv/bin/python ]] && [[ "$(.cache/moto-venv/bin/python -c 'import moto; print(moto.__version__)' 2>/dev/null)" == "${want}" ]]; then
  echo "ok: moto ${want} in .cache/moto-venv"
  exit 0
fi
python3 -m venv .cache/moto-venv
.cache/moto-venv/bin/pip install --quiet --disable-pip-version-check -r hack/moto-requirements.txt
echo "installed moto $(.cache/moto-venv/bin/python -c 'import moto; print(moto.__version__)') into .cache/moto-venv"
