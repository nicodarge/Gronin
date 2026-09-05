# Runtime

The six-stage runtime. What is here: the load gate, the schedule, gather, one bounded
agent stage with its receipt check, the sinks — two that deliver and one that creates,
capped — and the record with replay and resume.
The guard and retrieve stages are refused rather than ignored — a declared bound nothing
enforces reads as enforced in review.

## Running the tests

```sh
scripts/run-suite.sh        # gofmt, go vet, and the suite under the race detector
scripts/check-mutation.py   # break each declared line and confirm something notices
```

`run-suite.sh` puts itself in a network namespace holding nothing but loopback, and
refuses to run at all if it cannot. Everything that runs the tests — pre-commit, CI, a
developer — goes through it, so a green suite means one thing rather than three.

The mutations live in [testdata/mutations.json](testdata/mutations.json). Add one when
you add a guard: a test that has never been watched failing is not evidence of anything.
`scripts/check-mutation.py --self-test` shows the harness reporting zero, which is what
makes the zero it reports on the real mutations worth reading.

## Layout

Everything is under `internal/` except `cmd/gronin`. Nothing here is a library for
another program to import, and `internal/` says so as a compiler error rather than as a
convention. The packages follow the stages of the pipeline, so a stage's code is found
by its name in [../docs/architecture.md](../docs/architecture.md).

## Why the SQLite driver is the pure-Go one

`modernc.org/sqlite` rather than the better-known `mattn/go-sqlite3`, which needs a C
toolchain. One cgo dependency forfeits the static binary, and with it the requirement
that this installs where no toolchain exists. `scripts/check-static.sh` is what holds
the build to it; the release binaries are built with `CGO_ENABLED=0`.
