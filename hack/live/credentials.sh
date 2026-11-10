# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
# shellcheck shell=bash
#
# Federated AWS credentials for a live run, sourced by hack/live/run.sh on
# an offload VM started with --sa: Google's metadata server gives the VM's
# service account an ID token for LIVE_AUDIENCE; the AWS SDK exchanges it
# with AssumeRoleWithWebIdentity for LIVE_ROLE_ARN (the sandbox account's
# template, cloudcontrol/config/live). No stored keys. Tokens last an hour;
# a background loop writes a fresh one every 30 minutes, and live_creds_stop
# ends it.
LIVE_AUDIENCE="${LIVE_AUDIENCE:-ack-extensions-live-tests}"
_live_token_pid=""

live_token() {
  curl -fsS -H 'Metadata-Flavor: Google' \
    "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/identity?audience=${LIVE_AUDIENCE}&format=full"
}

live_creds_start() { # live_creds_start <role ARN> <directory>
  local role="$1" dir="$2"
  [[ "${role}" =~ ^arn:aws:iam::[0-9]{12}:role/ ]] || { echo "live: LIVE_ROLE_ARN '${role}' is not a role ARN" >&2; return 1; }
  # No other credential source may be in the environment.
  unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN AWS_PROFILE AWS_ENDPOINT_URL
  export AWS_CONFIG_FILE=/dev/null AWS_SHARED_CREDENTIALS_FILE=/dev/null
  local tok="${dir}/google-id-token"
  live_token >"${tok}.new" || { echo "live: no ID token from the metadata server: is this an offload VM run with --sa?" >&2; return 1; }
  mv "${tok}.new" "${tok}"
  chmod 600 "${tok}"
  ( while sleep 1800; do live_token >"${tok}.new" && mv "${tok}.new" "${tok}"; done ) &
  _live_token_pid=$!
  export AWS_ROLE_ARN="${role}" AWS_WEB_IDENTITY_TOKEN_FILE="${tok}" AWS_ROLE_SESSION_NAME="ack-extensions-live"
}

live_creds_stop() {
  [[ -n "${_live_token_pid}" ]] && kill "${_live_token_pid}" 2>/dev/null || true
}
