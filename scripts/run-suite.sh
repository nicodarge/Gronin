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
# Two planes, because closing one leaves the other open. The interface list comes from
# /proc/net/dev and not /sys/class/net: sysfs keeps showing the mount's original
# namespace until it is remounted, so from inside the namespace it listed every host
# interface and called a correctly isolated suite unisolated. And a namespace with only
# loopback still resolved names, through a resolver daemon reached over a Unix socket
# and living in the host's namespace, so a name that resolves is the other half of the
# answer. Neither reads anything the caller can set.
isolated() {
    [ -r /proc/net/dev ] || return 1
    case "$(awk 'NR > 2 { sub(/:$/, "", $1); printf "%s ", $1 }' /proc/net/dev)" in
        "lo ") ;;
        *) return 1 ;;
    esac
    command -v getent >/dev/null || return 1
    # A documentation domain, resolved rather than contacted: it answers everywhere the
    # network is reachable and nowhere it is not.
    ! getent hosts example.com >/dev/null 2>&1
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
