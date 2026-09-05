#!/usr/bin/env bash
# The one entry point for the Go suite. Everything that runs the tests — pre-commit,
# CI, a developer — runs this, so there is one definition of what a green suite means.
#
# It isolates itself from the network rather than trusting CI to do it. A suite that is
# hermetic only where it is reviewed lets the non-hermetic test be written, pushed, and
# discovered by someone else.
#
# The race detector needs cgo, so the suite builds with a C toolchain. The product does
# not: the release build sets CGO_ENABLED=0 and scripts/check-static.sh holds it to it.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"

if [ "${GRONIN_SUITE_ISOLATED:-}" != "1" ]; then
    exec env GRONIN_SUITE_ISOLATED=1 "$here/no-network.sh" "$0" "$@"
fi

cd "$here/../runtime"

# Off rather than unset: an absent module resolves to an error naming what is missing,
# instead of a fetch that hangs until the namespace times it out.
export GOPROXY=off GOFLAGS=-mod=mod

echo "==> gofmt"
unformatted="$(gofmt -l . 2>&1)"
if [ -n "$unformatted" ]; then
    echo "gofmt reports:" >&2
    echo "$unformatted" >&2
    exit 1
fi

echo "==> go vet"
go vet ./...

echo "==> go test -race"
go test ./... -race -count=1 -timeout "${GRONIN_SUITE_TIMEOUT:-10m}" "$@"
