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

# Isolation is verified, not declared. An earlier version re-executed itself unless an
# environment variable said it had already done so, which made the whole hermetic
# guarantee something a stray `export` could switch off silently — and it did, in
# review. The namespace holds nothing but loopback, so the interface list is what says
# whether we are in one, and nothing in the environment can claim otherwise. The
# variable survives only as a recursion guard.
#
# /proc/net/dev, not /sys/class/net: sysfs keeps showing the mount's original namespace
# until it is remounted, so inside the namespace it still listed every host interface —
# a check that answered "not isolated" while the process was isolated. procfs follows
# the namespace the reading process is in, and was measured doing so.
isolated() {
    [ -r /proc/net/dev ] || return 1
    case "$(awk 'NR > 2 { sub(/:$/, "", $1); printf "%s ", $1 }' /proc/net/dev)" in
        "lo ") return 0 ;;
        *) return 1 ;;
    esac
}

if ! isolated; then
    if [ "${GRONIN_SUITE_ISOLATED:-}" = "1" ]; then
        echo "run-suite: re-executed under no-network.sh and the network is still there." >&2
        echo "run-suite: refusing to call a suite hermetic when it is not." >&2
        exit 1
    fi
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
