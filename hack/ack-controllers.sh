#!/usr/bin/env bash
# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
#
# ACK's own service controllers, run per environment by the operator
# (docs/plan.md, section 7.7). For each account-level controller
# upstream.yaml pins, writes from the pinned commit (read-only: GitHub and
# the public ECR registry):
#
#   config/ack-controllers/<service>-controller-role.yaml
#       ACK's config/rbac/cluster-role-controller.yaml, renamed
#       ack-extensions-ack-<service>-controller (nothing else changed);
#   pkg/ackiam/recommended/<service>/recommended-{policy-arn,inline-policy}
#       ACK's config/iam/ files, as they are;
#   config/operator/operator.yaml ACK_IMAGE_<SERVICE>
#       public.ecr.aws/aws-controllers-k8s/<service>-controller:<version>@<digest>
#
# Run it after a pin move (docs/plan.md, section 3). TestACKControllerPins
# (task check) fails when its outputs and the pins disagree. Needs gh and
# crane.
set -o errexit -o nounset -o pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

# The account-level services whose ACK controller an ACKContext may run.
# Not organizations (organization-scoped, section 7.5), not secretsmanager
# (pinned for a test client only).
SERVICES=(applicationautoscaling autoscaling ec2 ecr eventbridge iam kms lambda rds s3 s3control wafv2)

pin_field() { # pin_field <name> <field>: a pin's field in upstream.yaml
  awk -v n="$1" -v f="$2" '
    $1 == "-" && $2 == "name:" { cur = $3; next }
    cur == n && $1 == f":" { print $2; exit }
  ' upstream.yaml
}

raw() { # raw <repo> <path> <ref>
  gh api "repos/$1/contents/$2?ref=$3" -H "Accept: application/vnd.github.raw"
}

# Docker Desktop's credential helper blocks on the keychain; the images
# are public, so read them with an empty Docker config.
DOCKER_CONFIG="$(mktemp -d)"
echo '{}' >"${DOCKER_CONFIG}/config.json"
export DOCKER_CONFIG
trap 'rm -rf "${DOCKER_CONFIG}" "${block:-}"' EXIT

roles=config/ack-controllers
mkdir -p "${roles}"
images=""
for svc in "${SERVICES[@]}"; do
  name="${svc}-controller"
  version="$(pin_field "${name}" version)"
  commit="$(pin_field "${name}" commit)"
  [[ -n "${version}" && -n "${commit}" ]] || { echo "no pin for ${name} in upstream.yaml" >&2; exit 1; }
  repo="aws-controllers-k8s/${name}"
  {
    echo "# Copied from ${repo} ${version} (${commit})"
    echo "# config/rbac/cluster-role-controller.yaml. Modified by High Five Labs for"
    echo "# ack-extensions: renamed ack-${name} -> ack-extensions-ack-${name}"
    echo "# (hack/ack-controllers.sh). SPDX-License-Identifier: Apache-2.0"
    raw "${repo}" config/rbac/cluster-role-controller.yaml "${commit}" \
      | sed "s/^  name: ack-${name}\$/  name: ack-extensions-ack-${name}/"
  } >"${roles}/${name}-role.yaml"
  grep -q "^  name: ack-extensions-ack-${name}\$" "${roles}/${name}-role.yaml" || { echo "${name}: ClusterRole not renamed" >&2; exit 1; }

  rec="pkg/ackiam/recommended/${svc}"
  rm -rf "${rec}"
  mkdir -p "${rec}"
  for f in $(gh api "repos/${repo}/contents/config/iam?ref=${commit}" --jq '.[].name'); do
    case "${f}" in recommended-policy-arn | recommended-inline-policy) raw "${repo}" "config/iam/${f}" "${commit}" >"${rec}/${f}" ;; esac
  done
  ls "${rec}"/recommended-* >/dev/null 2>&1 || { echo "${name}: no IAM recommendation" >&2; exit 1; }

  ref="public.ecr.aws/aws-controllers-k8s/${name}:${version#v}"
  digest="$(crane digest "${ref}")"
  images="${images}${svc}=${ref}@${digest}"$'\n'
  echo "${name} ${version}: ${ref}@${digest}"
done

{
  echo "# Copyright 2026 High Five Labs"
  echo "# SPDX-License-Identifier: Apache-2.0"
  echo "# ACK's own controllers' ClusterRoles, copied at the pins and renamed"
  echo "# (hack/ack-controllers.sh). The operator binds them per environment"
  echo "# (docs/plan.md, section 7.7)."
  echo "resources:"
  for svc in "${SERVICES[@]}"; do echo "  - ${svc}-controller-role.yaml"; done
} >"${roles}/kustomization.yaml"

# operator.yaml: replace the lines between the ACK_IMAGE markers.
op=config/operator/operator.yaml
block="$(mktemp)"
while IFS='=' read -r svc ref; do
  [[ -n "${svc}" ]] || continue
  printf '            - name: ACK_IMAGE_%s\n              value: %s\n' "$(tr '[:lower:]' '[:upper:]' <<<"${svc}")" "${ref}"
done <<<"${images}" >"${block}"
awk -v block="${block}" '
  /# BEGIN ACK_IMAGE/ { print; while ((getline l < block) > 0) print l; skip = 1; next }
  /# END ACK_IMAGE/ { skip = 0 }
  !skip { print }
' "${op}" >"${op}.new"
mv "${op}.new" "${op}"
grep -q "ACK_IMAGE_WAFV2" "${op}" || { echo "${op}: no ACK_IMAGE markers" >&2; exit 1; }
echo "wrote ${roles}/, pkg/ackiam/recommended/ and the ACK_IMAGE_ entries of config/operator/operator.yaml"
