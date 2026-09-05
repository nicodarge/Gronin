# Implementation Plan: Runtime Core

**Branch**: `add_speckit` | **Date**: 2026-09-04 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/001-runtime-core/spec.md`

## Summary

One executable that loads declarative playbooks, refuses the unsafe ones before arming anything,
runs the safe ones on a schedule, drives exactly one bounded agent per run as a child process,
routes the structured result to sinks, and records enough of all of it to answer "why did it do
that" afterwards.

The technical shape follows from two decisions taken in clarification. The agent stage is a child
process speaking the Claude Code command-line contract, because that contract is where the
containment flags live and a language-specific SDK only wraps it. And the deliverable is a single
static binary, because the product is judged on whether a stranger can install it.

Those two combine into the one constraint that governs every dependency choice below: **no cgo**.
A single dependency that needs a C toolchain forfeits the static binary and SC-007 with it.

## Technical Context

**Language/Version**: Go 1.27 (1.27.1 is current stable; the toolchain directive pins the minor,
not the patch)

**Primary Dependencies**:

- `spf13/cobra` — the CLI surface (FR-020, FR-021)
- `robfig/cron/v3` — schedule parsing and the scheduler (FR-009)
- `gopkg.in/yaml.v3` — playbook parsing, shape layer only
- `modernc.org/sqlite` — record store. **Pure Go on purpose**: `mattn/go-sqlite3` is the better-
  known driver and requires cgo, which would break the static binary and SC-007
- `santhosh-tekuri/jsonschema/v6` — validating an agent report against the playbook's declared
  output schema (FR-014)
- Standard library for everything else: `net/http` for the local API, `os/exec` for the agent
  child process, `encoding/json` for the event stream, `embed` for the published schema

**Storage**: One SQLite file for run records and their structured children; one directory of
per-run artifact blobs (gathered inputs, full prompt, raw transcript) referenced by row. Both under
a single configurable state directory.

**Testing**: `go test` with the standard library only. Two corpora under `testdata/` — hostile
playbooks and valid playbooks — drive the table tests for the load gate. A stub `claude` binary on
`PATH` makes the agent stage testable without tokens or network.

The chain around those tests is itself specified, because Principle VI's subject is the suite
rather than the code: one entry point running `go vet` and `go test ./... -race -count=1` under a
timeout, executed in CI with no route to the network so a test that reaches out fails where the
change is reviewed (SC-010, SC-011); a repeat-run job that treats a flake as a failure; a mutation
harness that is required to be able to print zero before any count it reports is read (SC-014); and
at least one test that drives the built executable rather than the packages behind it (SC-013).
SC-012 names the set that blocks a merge and is the one place it is enumerated.

**No coverage percentage is set, and that is a choice rather than an omission.** The property that
matters here is not how much of the tree a test touched but whether the guards fail when they
should, which SC-002 and SC-014 assert directly by mutation. A percentage floor is satisfied by
tests that execute a line without asserting anything about it, and it is the number a hurried
change raises rather than meets.

**Target Platform**: Linux (amd64, arm64) and macOS (arm64), cross-compiled from one machine.
Container image `FROM scratch`.

**Project Type**: Single Go module producing one CLI/daemon binary.

**Performance Goals**: Not a throughput system. The one bound that matters: loading and validating
a directory of playbooks completes fast enough that startup refusal is felt as immediate — target
under one second for a hundred playbooks. Run concurrency is bounded by a configured limit, not by
machine capacity, because each run costs money.

**Constraints**: No cgo. Idle memory small enough to sit on the same node as the things it watches.
Every state directory is configurable so nothing is written outside it.

**Scale/Scope**: Tens of playbooks, tens of runs per day, one node. Multi-node coordination is what
the guard feature exists for and is deliberately absent here.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

Checked against [constitution.md](../../.specify/memory/constitution.md), at 1.1.0.

### I. Bounds Are Declared and Enforced — PASS, and it is the design's centre

The load gate is the feature's largest single component and it runs before any trigger is armed
(FR-001 through FR-008, FR-013). Four mechanisms are applied to the child process, in decreasing
coarseness: restricted execution, the replacing tool set, strict MCP configuration, the allowlist.

Phase 0 found a fifth mechanism the specification had not anticipated: the agent process reports
the tool set it actually received, so the runtime asserts that receipt against the declaration and
aborts before the first token when they differ (FR-018). The bound is therefore verified at run
time as well as at load time, which no care in constructing the argument vector could achieve on
its own. Phase 0 also found that a flag an older executable does not recognise is ignored rather
than refused, so a version floor is checked at startup (FR-019).

The principle's second half — *test what it refuses* — becomes `testdata/playbooks/hostile/`, one
file per refusal rule, asserted as table tests. SC-002 requires each of those to fail the suite when
its check is removed, so the corpus is verified by mutation, not by being green.

### II. The Agent Reports, the Runtime Acts — PASS

The agent's declared tool set cannot include a creating tool (FR-003, FR-017), and sinks are the
only code path with credentials for anything outside the state directory. Caps are a load-time
requirement (FR-006), not a runtime check.

### III. Every Run Is Inspectable — PASS, with the reading deliberately narrowed

Records, replay and resume all ship in this feature (FR-026 through FR-031), **and in its first
tag**. That second clause is not redundant: an earlier draft of the task list drew a release
boundary inside the feature, below replay and resume, and this gate read PASS anyway because the
capabilities were present *somewhere* in the feature. A constitutional gate that only asks whether
a principle is satisfied eventually says nothing about what actually ships, so this one names the
release.

The web dashboard is deferred, and that is a considered reading rather than a departure: Principle
III demands the record and the ability to re-run from it, not a particular way of looking at it.
The operator commands that satisfy it ship in the first tag, and so does the API a dashboard would
later consume; only the HTML waits.

### IV. Playbooks Are Portable Data — PASS

Interpolation resolves against the trigger payload and deployment configuration only, never the
process environment (FR-008). This is a positive requirement on the resolver, so it gets a hostile
test: a playbook interpolating a name that exists in the environment must fail to resolve.

### V. Nothing From a Real Fleet Enters This Repository — PASS

No infrastructure identifier is required by anything in this plan. Test fixtures use
documentation-reserved values. `gitleaks` runs pre-commit.

### VI. The Suite Is the Gate — PASS, and it is the principle this plan gained late

The testing paragraph above is the whole of the compliance: hermetic, race-enabled, repeat-run,
mutation-proven, and asserted through the shipped binary. The principle was added after this plan
was first written (constitution 1.1.0) precisely because the plan named a stub agent and two
corpora and then said nothing about what would run them — which is how a suite ends up trusted for
being green.

The binary-level test (SC-013) is the clause with teeth for this feature specifically. Three
things here are real only in the executable: the argument vector that carries the containment
flags, the embedded `playbook.schema.json`, and the static linkage of SC-007. A package test
passes on all three while each is broken.

### Operational Constraints — PASS, its one open item closed by Phase 0

Wall-clock anchoring does not arise here (no dedup until the guard feature) but the record store
timestamps every run from the host clock, in UTC, so the guard feature inherits the right base.
Secrets never reach a command line: the agent child process receives credentials through its
environment, never through `argv`, and FR-029's redactor is applied at the write boundary of both
the record store and the logger.

**Configuration names** was the constraint with an open item: the credential sources in FR-032 had
to be read off the shipped CLI rather than assumed. Phase 0 established which sources exist and,
just as importantly, that their resolution order is *not* established — so the runtime reports the
source the agent process says it used rather than asserting one.

## Project Structure

### Documentation (this feature)

```text
specs/001-runtime-core/
├── plan.md              # This file
├── research.md          # Phase 0 output — the open questions and how they were closed
├── data-model.md        # Phase 1 output — the record store and the playbook shape
├── quickstart.md        # Phase 1 output — the 30-minute path of SC-001
├── contracts/
│   ├── playbook.schema.json   # The published playbook schema
│   └── cli.md                 # The operator command surface
└── tasks.md             # Phase 2 output (/speckit-tasks — NOT created here)
```

### Source Code (repository root)

```text
runtime/
├── cmd/gronin/            # main; wires cobra to the packages below
├── internal/
│   ├── playbook/          # parse (shape) + validate (semantics) + the published schema
│   ├── schedule/          # cron parsing, the scheduler, missed-occurrence recording
│   ├── run/               # run lifecycle, working directory, concurrency guard, replay/resume
│   ├── stage/
│   │   ├── gather/        # command execution into the working directory, output limits
│   │   └── agent/         # the child process: flag construction, stream-json decode, timeout
│   ├── sink/              # the sink interface + discord, slack, github
│   ├── record/            # SQLite store, artifact blobs, redaction at the write boundary
│   ├── config/            # deployment configuration and credential resolution
│   └── api/               # the local HTTP API the CLI talks to
└── testdata/
    ├── playbooks/{valid,hostile}/
    └── fakeclaude/        # stub binary emitting canned stream-json

playbooks/                 # shipped generic playbooks (unchanged by this feature)
docs/                      # unchanged by this feature
```

**Structure Decision**: a single Go module rooted at `runtime/`, with everything under `internal/`
except `cmd/gronin`. Nothing here is a library for other programs to import, and `internal/` says so
in a way a README cannot — it makes the package boundary a compiler error rather than a convention.
The split follows the six stages of the pipeline, so a reader who has read
[docs/architecture.md](../../docs/architecture.md) can find the code for a stage by its name.

`sink/` is one package rather than three because the interface it defines is the thing under test;
the three implementations are what prove the interface is real (FR-023).

## Phase 0 — Resolved

Complete. Findings and method in [research.md](./research.md); all three questions were answered by
running the shipped executable rather than by reading documentation about it.

1. **The containment flags are all present** on the shipped CLI, including the restricted-execution
   flag that FR-013 makes the default. A flag an older executable does not recognise is ignored,
   not refused — hence the version floor in FR-019.
2. **The event stream carries more than the record needs.** It reports the tool set the child
   actually received, which turns the bound from asserted into verified (FR-018), and it reports
   every action the bounds refused, which is now part of the record (FR-026).
3. **The credential sources are known; their resolution order is not.** The runtime therefore does
   not reimplement resolution — it reports what the agent process says it used (FR-032).

Two things Phase 0 deliberately did not establish, both handled by design rather than by further
research: the stream's shape on a failing run, and its stability across versions. The decoder
ignores unknown event types and unknown fields; the version floor protects the fields it depends
on.

## Complexity Tracking

No constitutional violation requires justification. One deliberate narrowing is recorded above under
Principle III (dashboard deferred, API built) rather than here, because it is a reading of the
principle rather than a departure from it.

One conflict existed and was removed rather than justified. The task list originally made the first
tag US1 + US2, leaving replay and resume to a later release; the analysis pass found that this
contradicted Principle III. The resolution was to correct User Story 3's priority to P1, not to
reinterpret the principle — which is what the constitution's own governance section requires.

| Choice | Why | Simpler alternative rejected because |
| ------ | --- | ------------------------------------ |
| Pure-Go SQLite driver | SC-007 requires a binary that runs where no toolchain exists | The common cgo driver is faster and better known, but forfeits static linking, and the record store is not on any hot path |
| Two validation layers (shape, then semantics) | The refusal rules are semantic — path resolution, cap presence, server existence — and a schema cannot express them | A JSON Schema alone would pass playbooks the constitution forbids. The schema is still published, for editors, but it is not the gate |
| Artifact blobs on disk, not in the row | A full transcript in a database row makes the store unreadable and unbackupable | Storing everything in SQLite is simpler until the first large transcript |
