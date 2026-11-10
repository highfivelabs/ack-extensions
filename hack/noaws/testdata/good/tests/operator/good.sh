#!/usr/bin/env bash
# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
fake_py() {
  env -i HTTPS_PROXY=http://127.0.0.1:9 "${PY}" -I "$@"
}
[[ -x "${PY}" ]] || hack/moto.sh
fake_py - <<'PYEOF'
import boto3
PYEOF
# aws s3 ls, in a comment
kubectl annotate sa x "eks.amazonaws.com/role-arn=arn:aws:iam::1:role/x"
