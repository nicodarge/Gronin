# FROM scratch, which is the point: SC-007 is judged on whether this installs where no
# runtime, package manager or interpreter exists, and an image carrying a distribution
# would make that untrue while looking fine.
#
# The binary is built by the release workflow with CGO_ENABLED=0 and checked by
# scripts/check-static.sh before it gets here. A dynamically linked binary in a scratch
# image does not start at all, so the check is what turns that into a build failure
# rather than a deployment one.
FROM scratch

# Certificate authorities, because the sinks talk HTTPS and scratch has none. Copied from
# a named image rather than curled at build time: a build that fetches is a build whose
# output depends on the day it ran.
COPY --from=alpine:3.21 /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

COPY gronin-linux-amd64 /gronin

# Not root. The runtime writes only under its state directory, and a container that runs
# as root to do that invites the state directory to be somewhere else.
USER 65532:65532

ENTRYPOINT ["/gronin"]
CMD ["serve"]
