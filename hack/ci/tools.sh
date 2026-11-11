#!/usr/bin/env bash
# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
#
# The command-line tools the tasks and CI call, at pinned versions, as
# release binaries into one directory (default bin/ci; CI_TOOLS_DIR):
#
#   hack/ci/tools.sh          install what is missing or at another version
#   hack/ci/tools.sh --dir    print the directory
#
# task tools runs it (task ci:tools), the Taskfile puts the directory first
# on PATH, CI's tools action (.github/actions/tools) and the merge gate
# (hack/ci/gate.sh) run it too: GitHub's runner, an offload VM and a laptop
# get the same versions. The pins are the offload VM image's (kind, ko:
# highfivelabs/offload, vm-startup.sh); where that image takes "latest"
# (task, crane, kubectl, helm 3), the release current on 2026-11-10.
#
# Release binaries, not `go install`: task v3.54.0 needs a newer Go than
# go.mod's, and the binaries do not depend on it. Linux and macOS, amd64
# and arm64.
set -euo pipefail

TASK_VERSION=v3.54.0
KIND_VERSION=v0.33.0
KO_VERSION=v0.19.1
CRANE_VERSION=v0.22.1
KUBECTL_VERSION=v1.37.2
HELM_VERSION=v3.22.1
PINS="task-${TASK_VERSION}_kind-${KIND_VERSION}_ko-${KO_VERSION}_crane-${CRANE_VERSION}_kubectl-${KUBECTL_VERSION}_helm-${HELM_VERSION}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DIR="${CI_TOOLS_DIR:-$ROOT/bin/ci}"

case "${1:-}" in
  --dir) echo "$DIR"; exit 0 ;;
  --pins) echo "$PINS"; exit 0 ;;
  "") ;;
  *) echo "usage: hack/ci/tools.sh [--dir|--pins]" >&2; exit 2 ;;
esac

# The stamp names the pins the directory holds; a change of any pin
# reinstalls all six.
if [[ "$(cat "$DIR/.pins" 2>/dev/null || true)" == "$PINS" ]]; then
  echo "tools: $DIR holds $PINS"
  exit 0
fi

case "$(uname -s)" in
  Linux) os=linux; Os=Linux ;;
  Darwin) os=darwin; Os=Darwin ;;
  *) echo "tools: unsupported OS $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64; xarch=x86_64 ;;
  arm64|aarch64) arch=arm64; xarch=arm64 ;;
  *) echo "tools: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

mkdir -p "$DIR"
TMP="$(mktemp -d "${TMPDIR:-/tmp}"/ci-tools.XXXXXX)"
trap 'rm -rf "$TMP"' EXIT

# fetch URL FILE: download, failing on any HTTP error.
fetch() { curl -sfL --retry 3 -o "$2" "$1"; }
gh=https://github.com

fetch "$gh/go-task/task/releases/download/$TASK_VERSION/task_${os}_${arch}.tar.gz" "$TMP/task.tgz"
tar -xzf "$TMP/task.tgz" -C "$DIR" task

fetch "$gh/kubernetes-sigs/kind/releases/download/$KIND_VERSION/kind-${os}-${arch}" "$DIR/kind"
chmod +x "$DIR/kind"

fetch "$gh/ko-build/ko/releases/download/$KO_VERSION/ko_${KO_VERSION#v}_${Os}_${xarch}.tar.gz" "$TMP/ko.tgz"
tar -xzf "$TMP/ko.tgz" -C "$DIR" ko

fetch "$gh/google/go-containerregistry/releases/download/$CRANE_VERSION/go-containerregistry_${Os}_${xarch}.tar.gz" "$TMP/crane.tgz"
tar -xzf "$TMP/crane.tgz" -C "$DIR" crane

fetch "https://dl.k8s.io/release/$KUBECTL_VERSION/bin/$os/$arch/kubectl" "$DIR/kubectl"
chmod +x "$DIR/kubectl"

fetch "https://get.helm.sh/helm-$HELM_VERSION-$os-$arch.tar.gz" "$TMP/helm.tgz"
tar -xzf "$TMP/helm.tgz" -C "$DIR" --strip-components=1 "$os-$arch/helm"

"$DIR/task" --version
"$DIR/kind" version
echo "$PINS" >"$DIR/.pins"
echo "tools: installed $PINS into $DIR"
