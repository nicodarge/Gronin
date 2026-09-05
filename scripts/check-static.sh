#!/usr/bin/env bash
# Refuse a release binary that is not self-contained.
#
# SC-007 — installable where no toolchain, runtime or package manager exists — is the
# requirement an innocent dependency bump loses, because a package that needs cgo
# builds fine and nothing else in the pipeline looks at the result.
#
# The two platforms are checked differently, and not for convenience. A Linux Go binary
# built without cgo is genuinely statically linked and `file` says so. A macOS binary
# never is: Mach-O always links libSystem, so asserting "statically linked" there would
# be a check that can never pass. What is asserted on both is the cause rather than the
# symptom — that the binary was built with cgo disabled.
#
# --self-test builds a cgo binary and asserts this script refuses it. A check probed
# only on what it accepts is untested.
set -euo pipefail

# Exits rather than returns. A `return 1` here made the verdict depend on how the
# caller invoked the function: under `set -e` the script stopped at the first failure,
# but in a `||` context — which is how the self-test calls it — execution continued to
# the closing "ok" and the function reported success on a binary it had just refused.
# The self-test caught it on its first run. check_one is therefore always called in a
# subshell, so this exit ends the check and not the caller.
fail() { echo "check-static: $*" >&2; exit 1; }

check_one() {
    local bin="$1" settings goos cgo
    [ -f "$bin" ] || fail "$bin: no such file"

    settings="$(go version -m "$bin" 2>/dev/null)" || fail "$bin: not a Go binary"
    goos="$(printf '%s\n' "$settings" | awk '$1=="build" && $2 ~ /^GOOS=/ {sub(/^GOOS=/,"",$2); print $2}')"
    cgo="$(printf '%s\n' "$settings" | awk '$1=="build" && $2 ~ /^CGO_ENABLED=/ {sub(/^CGO_ENABLED=/,"",$2); print $2}')"

    [ -n "$goos" ] || fail "$bin: build settings name no GOOS"
    [ -n "$cgo" ] || fail "$bin: build settings name no CGO_ENABLED"
    [ "$cgo" = "0" ] || fail "$bin: built with CGO_ENABLED=$cgo; SC-007 requires 0"

    if [ "$goos" = "linux" ]; then
        local described
        described="$(file -b "$bin")"
        case "$described" in
            *"dynamically linked"*|*interpreter*)
                fail "$bin: dynamically linked — $described" ;;
            *"statically linked"*) ;;
            *) fail "$bin: file does not call it statically linked — $described" ;;
        esac
    fi

    echo "check-static: $bin ok ($goos, CGO_ENABLED=0)"
}

self_test() {
    local dir pkg rc
    dir="$(mktemp -d)"
    trap 'rm -rf "$dir"' RETURN
    pkg="$(cd "$(dirname "$0")/../runtime" && pwd)/cmd/gronin"

    ( cd "$(dirname "$pkg")/.." && CGO_ENABLED=1 go build -o "$dir/dynamic" ./cmd/gronin )
    ( cd "$(dirname "$pkg")/.." && CGO_ENABLED=0 go build -o "$dir/static" ./cmd/gronin )

    rc=0
    ( check_one "$dir/dynamic" ) >/dev/null 2>&1 || rc=$?
    [ "$rc" -ne 0 ] || fail "self-test: a CGO_ENABLED=1 binary was accepted"

    ( check_one "$dir/static" ) >/dev/null || fail "self-test: a CGO_ENABLED=0 binary was refused"

    echo "check-static: self-test ok — refuses a cgo build, accepts a static one"
}

if [ "${1:-}" = "--self-test" ]; then
    self_test
    exit 0
fi

[ "$#" -gt 0 ] || { echo "usage: $0 <binary>... | --self-test" >&2; exit 2; }
for b in "$@"; do ( check_one "$b" ); done
