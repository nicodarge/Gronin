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
# namespace goes with it, and the resolver configuration is replaced — including the
# hosts file itself, which `files` would otherwise still read from the host. What is
# left resolves loopback and nothing else: dropping `files` altogether would close the
# same gap and take `localhost` with it.
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
printf '127.0.0.1 localhost\n::1 localhost\n' > "$tmp/hosts"
# The machine's own name goes in too: without it sudo answers "unable to resolve host"
# on every invocation under the fallback below, which was noise on a CI runner and would
# be a failure for anything that needs its own hostname. Skipped rather than written
# blank if the name cannot be read.
this_host="$(hostname 2>/dev/null || true)"
[ -n "$this_host" ] && printf '127.0.1.1 %s\n' "$this_host" >> "$tmp/hosts"

# Loopback comes up DOWN in a fresh namespace and a suite that binds 127.0.0.1 needs it;
# `ip` is not required, and without it a test needing loopback fails loudly rather than
# reaching outward. The binds are required: they are what closes the resolver, so a
# failure to apply them is a failure to isolate.
inner='
    ip link set lo up 2>/dev/null || true
    mount --bind "$1/nsswitch.conf" /etc/nsswitch.conf || exit 1
    mount --bind "$1/resolv.conf" /etc/resolv.conf || exit 1
    mount --bind "$1/hosts" /etc/hosts || exit 1
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
    # The environment is carried in a file, not on the command line. --preserve-env
    # cannot do it: measured, the OUTER sudo strips these variables before the inner one
    # runs, so preserving them by name preserves nothing — a silent no-op. Passing the
    # values as arguments does work and is what the constitution forbids: sudo journals
    # its whole COMMAND= line, and GOPROXY is routinely a URL with a token in it. The
    # file is read inside the command instead, so only its path is ever an argument.
    #
    # The list is what the suite needs today, not a complete one. A later phase reaching
    # for another variable under this path extends it here rather than working around it.
    : > "$tmp/env"
    for name in HOME PATH GOPATH GOMODCACHE GOCACHE GOPROXY GOFLAGS GOTOOLCHAIN \
                GRONIN_SUITE_ISOLATED GRONIN_SUITE_TIMEOUT; do
        # `+x` rather than `:-`: a variable deliberately set to empty is not the same as
        # one that was never set, and only the first should survive as empty.
        if [ -n "${!name+x}" ]; then
            printf '%s=%q\n' "$name" "${!name}" >> "$tmp/env"
        fi
    done

    sudo -n unshare --net --mount -- \
        "${BASH:-/bin/bash}" -c "$inner" no-network "$tmp" \
        sudo -n -u "$(id -un)" -- \
        "${BASH:-/bin/bash}" -c 'set -a; . "$1"; set +a; shift; exec "$@"' \
        no-network-env "$tmp/env" "$@" || rc=$?
else
    echo "no-network: no way to create a network namespace on this machine." >&2
    echo "no-network: refusing to run '$1' with the network still reachable." >&2
    exit 1
fi

exit "$rc"
