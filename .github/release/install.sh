#!/usr/bin/env bash
# One install for release.yaml and release-toolchain.yaml, so the pull request check runs what ships.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
prefix="${RUNNER_TEMP:?}/semantic-release"

mkdir -p "${prefix}"
cp "${here}/package.json" "${here}/package-lock.json" "${prefix}/"
cp -R "${here}/stubs" "${prefix}/"
npm ci --ignore-scripts --engine-strict --no-audit --no-fund --prefix "${prefix}"
