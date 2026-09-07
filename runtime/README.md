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

Neither runs on a commit or a push. They are the merge gate and CI is where a merge gate
belongs; run before every push they cost minutes, long enough that git's own SSH
connection idles out and the push fails. `pre-commit run --hook-stage manual go-suite
mutation` runs them through the hook definitions rather than around them.

The harness builds in a cache of its own, thrown away when it finishes. Each mutant is a
fresh copy of the tree at a fresh path, so the compiler writes a distinct set of entries
for each; against the developer's own cache a day of runs took it to 46 GB on 2026-09-06,
and Go trims on five days of disuse rather than on size. The first mutant of every run therefore
compiles the standard library and the dependencies cold.

The mutations live in [testdata/mutations.json](testdata/mutations.json). Add one when
you add a guard: a test that has never been watched failing is not evidence of anything.
`scripts/check-mutation.py --self-test` shows the harness reporting zero, which is what
makes the zero it reports on the real mutations worth reading. It also shows it refusing
rather than reporting — a mutant that does not compile is one of those, because a build
failure exits non-zero exactly like a failing test and would otherwise be counted as
caught by a test that never ran, and so is a mutation whose tree holds Go the harness
cannot compile.

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
