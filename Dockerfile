# A stage that exists only to make a directory owned by the runtime's user. The final
# image has no shell, so a writable state directory has to arrive already made.
FROM alpine:3.21 AS filesystem
RUN mkdir -p /data && chown 65532:65532 /data

# distroless, not scratch — and that is a correction rather than a preference.
#
# plan.md said `FROM scratch`, on the reasoning that SC-007 is judged on installing where
# no runtime exists. That is true of the BINARY, and it stays true: it is built with
# CGO_ENABLED=0 and scripts/check-static.sh refuses a dynamically linked one. It is not
# true of the image, because the image's whole purpose is to run `serve`, and `serve`
# drives the agent — which is itself a dynamically linked ELF needing glibc and
# ld-linux.so. Measured: `ldd` on the shipped executable lists libc and the loader, it
# cannot start in a scratch image at all, and mounting it in does not help.
#
# So a scratch image could run `validate`, `runs` and `show` and never the thing it
# exists for. This base has glibc and the CA certificates the sinks need, no shell, no
# package manager, and a non-root user — everything scratch was chosen for except the
# part that made the product impossible.
FROM gcr.io/distroless/base-debian12:nonroot

COPY --from=filesystem --chown=65532:65532 /data /data
COPY gronin-linux-amd64 /gronin

# The agent is not in here. It is a separate executable with its own release cadence and
# its own credentials, and baking a copy in would pin a version this image cannot check.
# Mount it, or build on top of this image, and name it with --agent.
USER 65532:65532

# Named, not inferred. Without it the runtime falls back through XDG_STATE_HOME and HOME
# to /tmp, and a container that writes to /tmp loses its record on every restart.
ENV GRONIN_STATE_DIR=/data
VOLUME ["/data"]

ENTRYPOINT ["/gronin"]
CMD ["serve"]
