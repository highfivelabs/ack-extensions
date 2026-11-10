#!/usr/bin/env bash
# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
"${PY}" -I - <<'PYEOF'
import boto3
PYEOF
aws s3 ls
curl -fsS https://s3.amazonaws.com/
