#!/usr/bin/env bash
# Install the pinned release toolchain outside the workspace, without install scripts.
# release.yaml and release-toolchain.yaml both run this, so the check exercises what ships.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
prefix="${RUNNER_TEMP:?}/semantic-release"

mkdir -p "${prefix}"
cp "${here}/package.json" "${here}/package-lock.json" "${prefix}/"
cp -R "${here}/stubs" "${prefix}/"
npm ci --ignore-scripts --engine-strict --no-audit --no-fund --prefix "${prefix}"
