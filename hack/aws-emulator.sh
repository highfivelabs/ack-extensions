#!/usr/bin/env bash
# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
#
# Install AWS Emulator (github.com/aws/aws-emulator, CC0) at the pinned
# version from its Debian package, the signature checked against the
# publisher's key fingerprint first (docs/aws-emulator.md). Linux amd64 or
# arm64 with apt only; does nothing when that version is already on PATH.
# Opt-in: no task that check runs calls it.
set -euo pipefail

VERSION="${AWS_EMULATOR_VERSION:-1.0.322}"
FINGERPRINT="98BCF336A3BE53B734E967C52E5BF49BD940F624"
BASE="https://downloads.aws.com/aws-emulator/packages/aws-emulator/LTS"

if command -v aws-emulator >/dev/null; then
  have="$(aws-emulator -v | awk '/^Version/ {print $2}')"
  if [ "$have" = "$VERSION" ]; then
    echo "aws-emulator $VERSION already installed"
    exit 0
  fi
fi

case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "aws-emulator.sh: no package for $(uname -m)" >&2; exit 2 ;;
esac
command -v apt-get >/dev/null || { echo "aws-emulator.sh: needs apt-get (Debian or Ubuntu)" >&2; exit 2; }

dir="$(mktemp -d)"
trap 'rm -rf "$dir"' EXIT
pkg="aws-emulator_${VERSION}-1_${arch}.deb"
curl -fsSLo "$dir/$pkg" "$BASE/$pkg"
curl -fsSLo "$dir/$pkg.sig" "$BASE/$pkg.sig"
curl -fsSLo "$dir/key.asc" "https://downloads.aws.com/keys/gpg/current.asc"

export GNUPGHOME="$dir/gnupg"
mkdir -m 700 "$GNUPGHOME"
gpg --batch --quiet --import "$dir/key.asc"
got="$(gpg --batch --with-colons --fingerprint | awk -F: '/^fpr/ {print $10; exit}')"
if [ "$got" != "$FINGERPRINT" ]; then
  echo "aws-emulator.sh: key fingerprint $got, not $FINGERPRINT" >&2
  exit 1
fi
gpg --batch --verify "$dir/$pkg.sig" "$dir/$pkg"

sudo=""
[ "$(id -u)" = 0 ] || sudo=sudo
$sudo apt-get install -y "$dir/$pkg"
aws-emulator -v
