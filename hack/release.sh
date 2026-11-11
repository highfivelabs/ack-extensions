#!/usr/bin/env bash
# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
#
# task release TAG=v0.1.0 (release.yml's images job): build the operator
# and every manager image with ko, the way tests/operator/run.sh builds
# them (config/default's ko:// references), push them to KO_DOCKER_REPO
# tagged TAG, and write the resolved install manifest to
# out/ack-extensions-TAG.yaml.
#
# Pushing needs write access to the registry: release.yml logs ko in with
# a Workload Identity token (REGISTRY_TOKEN); by hand, a ko or docker
# login you already have. Environment: TAG, KO_DOCKER_REPO, REGISTRY,
# REGISTRY_TOKEN (optional), GITHUB_STEP_SUMMARY (optional).
set -euo pipefail

: "${TAG:?TAG is the version tag}" "${KO_DOCKER_REPO:?set KO_DOCKER_REPO to the image repository (see .env.example)}" "${REGISTRY:?}"
if [[ -n "${REGISTRY_TOKEN:-}" ]]; then
  ko login "${REGISTRY}" --username oauth2accesstoken --password-stdin <<<"${REGISTRY_TOKEN}"
fi

mkdir -p out
manifest="out/ack-extensions-${TAG}.yaml"
kubectl kustomize config/default >out/kustomized.yaml
# --preserve-import-paths: every manager's main package is named
# cmd/controller, so the import path keeps their images apart.
GOTOOLCHAIN=local ko resolve --preserve-import-paths --tags "${TAG}" \
  --platform=linux/amd64,linux/arm64 -f out/kustomized.yaml >"${manifest}"
rm -f out/kustomized.yaml
{
  echo "### Images pushed for ${TAG}"
  echo
  grep -o "${KO_DOCKER_REPO}/[^\"' ]*" "${manifest}" | sort -u | sed 's/^/- /'
} >> "${GITHUB_STEP_SUMMARY:-/dev/stdout}"
