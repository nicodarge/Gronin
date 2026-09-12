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

# No agent on the path. Every agent test drives the stub and names it, and a test that
# reaches for whatever `claude` the machine happens to have passes here and fails on a
# machine that has none — which is exactly what happened on the first CI run after
# `gronin version` started reporting the agent it found. A shim that refuses makes that
# failure happen wherever the test is written instead.
shim="$(mktemp -d)"
trap 'rm -rf "$shim"' EXIT
cat > "$shim/claude" <<'SHIM'
#!/bin/sh
echo "run-suite: a test reached for the machine's agent." >&2
echo "run-suite: name one instead — the stub is internal/fakeagent.Build(t)." >&2
exit 127
SHIM
chmod +x "$shim/claude"
export PATH="$shim:$PATH"

# SC-005's outside half. The suite asserts internally that no configured secret reaches
# the record; this asserts that none reached the suite's own output either, which is
# where a stray t.Logf or a printed error would put it. One literal, read from the
# package the tests use, so the two checks cannot come to assert different secrets.
sentinel="$(sed -n 's/^const Value = "\(.*\)"$/\1/p' internal/testsecret/testsecret.go)"
if [ -z "$sentinel" ]; then
    echo "run-suite: cannot read the test sentinel from internal/testsecret." >&2
    echo "run-suite: refusing to run a suite whose secret check cannot work." >&2
    exit 1
fi

# -v is not for the reader, it is what makes the scan above mean anything: `go test`
# buffers a passing test's log output and prints it only on failure, so without -v a
# secret written by a test that passes never appears in the output being scanned. Probed
# exactly that way — a t.Logf of the sentinel in a passing test slipped through. The file
# keeps everything; the terminal gets the per-test noise filtered back out.
echo "==> go test -race"
output="$(mktemp)"
trap 'rm -f "$output"; rm -rf "$shim"' EXIT
set +e
go test ./... -v -race -count=1 -timeout "${GRONIN_SUITE_TIMEOUT:-20m}" "$@" 2>&1 \
    | tee "$output" \
    | command grep -vE '^(=== (RUN|PAUSE|CONT)|--- PASS|    )'
status=${PIPESTATUS[0]}
set -e

# Scanned before the status is acted on: a failing suite is exactly where a secret
# printed by a test would sit, and an early exit here used to skip the check entirely.
if command grep -qF "$sentinel" "$output"; then
    echo "==> a test secret reached the suite's output:" >&2
    command grep -nF "$sentinel" "$output" >&2
    exit 1
fi

# The filter above drops every indented line, and an indented line is where a failing
# test writes the reason it failed — so a red CI job read `--- FAIL` and nothing else.
# Replayed whole rather than extracted: `go test -v` prints a test's output *before* its
# `--- FAIL` header, and a race report or a panic is not indented at all, so every
# cleverer filter tried here dropped the one line worth reading. Only on failure, and
# only once the scan above has cleared it of secrets.
if [ "$status" -ne 0 ]; then
    echo "==> the suite failed; its output in full follows." >&2
    cat "$output" >&2
    exit "$status"
fi
