#!/usr/bin/env bash
# Run a command with no way to reach the network, by name or by address.
#
# A test that reaches the network is not slow, it is conditional: it passes on the
# machine that has the access and fails on the machine that reviews the change. This is
# what makes that failure happen where the change is written.
#
# The network namespace closes the data plane. It does not close name resolution, which
# was measured leaking straight through it: with only loopback present, curl to an
# address failed while `getent hosts` still answered, because glibc's NSS asks
# systemd-resolved over a Unix socket and that daemon does the lookup from the host's
# namespace. Unix sockets are not what a network namespace separates. So the mount
# namespace goes with it, and the resolver configuration is replaced by one that can
# only read /etc/hosts.
#
# It refuses rather than degrades. If no namespace can be created the command does not
# run — a fallback that quietly restores the network is a check that reports success for
# the thing it was meant to catch. Two mechanisms, because unprivileged user namespaces
# are restricted on some hosts and a CI runner is one of the places that can be true;
# the one that was used is printed, so a green run says which bound held.
set -euo pipefail

if [ "$#" -eq 0 ]; then
    echo "usage: $0 <command> [args...]" >&2
    exit 2
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
printf 'hosts: files myhostname\n' > "$tmp/nsswitch.conf"
: > "$tmp/resolv.conf"

# Loopback comes up DOWN in a fresh namespace and a suite that binds 127.0.0.1 needs it;
# `ip` is not required, and without it a test needing loopback fails loudly rather than
# reaching outward. The binds are required: they are what closes the resolver, so a
# failure to apply them is a failure to isolate.
inner='
    ip link set lo up 2>/dev/null || true
    mount --bind "$1/nsswitch.conf" /etc/nsswitch.conf || exit 1
    mount --bind "$1/resolv.conf" /etc/resolv.conf || exit 1
    shift
    exec "$@"
'

rc=0
if unshare --user --map-root-user --net --mount true 2>/dev/null; then
    echo "no-network: isolated with an unprivileged user namespace" >&2
    unshare --user --map-root-user --net --mount -- \
        "${BASH:-/bin/bash}" -c "$inner" no-network "$tmp" "$@" || rc=$?
elif sudo -n unshare --net --mount true 2>/dev/null; then
    echo "no-network: isolated with sudo unshare, dropping back to $(id -un)" >&2
    # The preserved list is what the suite needs today, not a complete one. A later
    # phase that reaches for an environment variable under this path extends it here
    # rather than working around it.
    sudo -n unshare --net --mount -- \
        "${BASH:-/bin/bash}" -c "$inner"' ' no-network "$tmp" \
        sudo -n -u "$(id -un)" \
            --preserve-env=PATH,HOME,GOPATH,GOMODCACHE,GOCACHE,GOPROXY,GOFLAGS,GRONIN_SUITE_ISOLATED,GRONIN_SUITE_TIMEOUT \
            -- "$@" || rc=$?
else
    echo "no-network: no way to create a network namespace on this machine." >&2
    echo "no-network: refusing to run '$1' with the network still reachable." >&2
    exit 1
fi

exit "$rc"
