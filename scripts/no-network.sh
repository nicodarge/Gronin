#!/usr/bin/env bash
# Run a command in a network namespace holding nothing but loopback.
#
# A test that reaches the network is not slow, it is conditional: it passes on the
# machine that has the access and fails on the machine that reviews the change. This is
# what makes that failure happen where the change is written.
#
# It refuses rather than degrades. If no namespace can be created the command does not
# run — a fallback that quietly restores the network is a check that reports success for
# the thing it was meant to catch. Two mechanisms are tried because unprivileged user
# namespaces are restricted on some hosts, and the CI runner is one of the places that
# can be true; the one that was used is printed, so a green run says which bound held.
set -euo pipefail

if [ "$#" -eq 0 ]; then
    echo "usage: $0 <command> [args...]" >&2
    exit 2
fi

# Loopback comes up DOWN in a fresh namespace and a suite that binds 127.0.0.1 needs it.
# `ip` is not required: without it the command still runs isolated, and a test needing
# loopback fails loudly rather than reaching outward.
raise_lo='ip link set lo up 2>/dev/null || true'

if unshare --user --map-root-user --net true 2>/dev/null; then
    echo "no-network: isolated with an unprivileged user namespace" >&2
    exec unshare --user --map-root-user --net -- \
        "${BASH:-/bin/bash}" -c "$raise_lo"'; exec "$@"' no-network "$@"
fi

if sudo -n unshare --net true 2>/dev/null; then
    echo "no-network: isolated with sudo unshare, dropping back to $(id -un)" >&2
    exec sudo -n unshare --net -- \
        "${BASH:-/bin/bash}" -c "$raise_lo"'; exec sudo -n -u "$0" \
            --preserve-env=PATH,HOME,GOPATH,GOMODCACHE,GOCACHE,GOPROXY,GOFLAGS,GRONIN_SUITE_ISOLATED,GRONIN_SUITE_TIMEOUT \
            -- "$@"' "$(id -un)" "$@"
fi

echo "no-network: no way to create a network namespace on this machine." >&2
echo "no-network: refusing to run '$1' with the network still reachable." >&2
exit 1
