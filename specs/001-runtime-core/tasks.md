---

description: "Task list for the runtime core"
---

# Tasks: Runtime Core

**Input**: Design documents from `/specs/001-runtime-core/`

**Prerequisites**: [plan.md](./plan.md), [spec.md](./spec.md), [research.md](./research.md),
[data-model.md](./data-model.md), [contracts/](./contracts/)

**Tests**: included, and not optional here. SC-002 requires a hostile-playbook corpus in which each
refusal fails the suite when its check is removed, and the constitution requires any code asserting
a bound to be tested against what it refuses. A guard probed only on its accepted values is
untested.

**Organization**: grouped by user story. The three P1 stories ship together — Principle I forbids
releasing US1 without US2, and Principle III forbids releasing either without US3, which requires
that a run be replayable and resumable from its record. They stay separate phases because each is
separately implementable and separately testable, not because each is separately releasable.

**Task numbering**: T068-T071 and T074-T078 were added after their phase was written and sit in
the phase they belong to rather than at the end of the file, so the identifiers are not in
document order. Identifiers
are stable and tasks reference each other; renumbering to restore the order would break those
references silently, which is exactly how the earlier renumbering went wrong.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependencies)
- **[Story]**: the user story the task serves

## Path Conventions

Paths follow the structure in [plan.md](./plan.md): a single Go module rooted at `runtime/`, with
`cmd/gronin` as the only non-`internal` package.

---

## Phase 1: Setup

- [ ] T001 Create `runtime/` module: `go.mod` with the Go 1.27 toolchain directive, the package
      skeleton from plan.md, and a `main` that prints its own version
- [ ] T002 Pin dependencies and record why the SQLite driver is the pure-Go one: `cobra`,
      `robfig/cron/v3`, `gopkg.in/yaml.v3`, `modernc.org/sqlite`, `santhosh-tekuri/jsonschema/v6`
- [ ] T003 [P] Add `.golangci.yml` and wire `golangci-lint` plus `go test` into
      `.pre-commit-config.yaml`
- [ ] T004 [P] Add the CI workflow: lint, test, and cross-compile for linux/amd64, linux/arm64,
      darwin/arm64 with `CGO_ENABLED=0`
- [ ] T005 Add a CI check that fails if the built binary is dynamically linked — SC-007 is the
      requirement most easily lost to an innocent dependency bump, and nothing else notices
- [ ] T074 One entry point for the suite — `go vet`, then `go test ./... -race -count=1` under a
      declared timeout — and run it in CI with no route to the network, so a test that reaches out
      fails on the machine that reviews the change rather than passing on the one that wrote it
      (SC-010, SC-011)
- [ ] T075 Make the gate required rather than advisory: lint, vet, the race suite, the mutation
      check and the static-link check each block the merge (SC-012). A job that reports without
      blocking is a dashboard
- [ ] T076 [P] Repeat-run job: the suite ten times against an unchanged tree, disagreement failing
      it (SC-011). A flake found here is a bug; found later it is a reason to stop reading red
- [ ] T077 `scripts/check-mutation.py`: mutate a named line in a copy of its target, assert the
      exit code flips, and prove on an unmodified tree that the harness can print zero (SC-014).
      T036 asserts through this rather than rolling its own
- [ ] T078 Binary-level test harness: build the executable into a temporary directory and drive it
      through its command surface (SC-013). Its first subject is `version`; every later
      operator-surface test uses it instead of calling the packages behind it

**Checkpoint**: an empty binary builds statically on three platforms, a hermetic race-enabled suite
runs against the binary itself, and CI blocks on all of it.

---

## Phase 2: Foundational (blocks every story)

- [ ] T006 `internal/config`: deployment configuration, the state directory, and value references
      that playbooks interpolate against. Interpolation MUST NOT read the process environment
      (FR-008) — write that test now, not later
- [ ] T007 `internal/record`: the SQLite schema from data-model.md and its migration path
- [ ] T008 [P] `internal/record`: blob store for gathered inputs, prompts and transcripts, keyed per
      run
- [ ] T009 [P] `internal/record`: the redactor, applied at the write boundary of the store and the
      logger, seeded from configured secret values (FR-029)
- [ ] T010 Test: no configured secret value appears in any record or log line produced by the whole
      suite, asserted by scanning the suite's own output (SC-005)
- [ ] T011 `internal/playbook`: YAML parse into typed structs (shape layer), and embed
      `contracts/playbook.schema.json` for publication (FR-002)
- [ ] T012 Port the schema probe to Go: fourteen documents, ten of which must be refused. The Python
      probe run on 2026-09-04 passed all fourteen; this is the same assertion, in the test suite,
      where it can keep passing
- [ ] T013 `internal/run`: run lifecycle — identifier, working directory creation and removal
      (FR-011), status transitions, and the single-flight guard that stops one playbook running
      twice at once (FR-016)
- [ ] T014 `testdata/fakeclaude/`: a stub agent binary emitting canned `stream-json`, with knobs for
      success, timeout, malformed output, a mismatched tool-set receipt, and a non-zero exit. Every
      agent-stage test uses it; none spends a token
- [ ] T015 `cmd/gronin`: cobra skeleton with `version` and the flag plumbing for the state
      directory (FR-020)

**Checkpoint**: playbooks parse, runs can be created and recorded, and the agent can be faked.

---

## Phase 3: User Story 1 — A scheduled playbook produces a report (P1) 🎯 MVP

**Goal**: a playbook on a schedule gathers, runs one agent, and delivers a report.

**Independent Test**: a playbook whose gather step reads a fixture and whose sink is a chat channel,
scheduled a minute out; a report arrives referencing the fixture.

### Tests for User Story 1

- [ ] T016 [P] [US1] Scheduler test: a cron-triggered playbook executes once at its time, and a
      missed occurrence is recorded with its reason (FR-009, FR-030)
- [ ] T017 [P] [US1] Gather test: a step exiting non-zero aborts the run before the agent stage and
      no tokens are spent (FR-010); output past the limit is truncated and the truncation recorded
- [ ] T018 [P] [US1] Agent-stage test against the stub: the terminal event's cost, usage, turn count
      and stop reason land in the record (FR-026)
- [ ] T019 [P] [US1] Timeout test: a stub that never terminates is killed at the declared timeout,
      the run is marked timed out, and the partial transcript survives (FR-015)
- [ ] T020 [P] [US1] Output-schema test: a report that does not satisfy the declared schema marks
      the run failed and sends the validation failure, not the malformed content (FR-014)
- [ ] T021 [P] [US1] Working-directory test: after a run ends by any path — success, failure,
      timeout, refusal — the directory is gone and the gathered inputs are still in the record
- [ ] T068 [P] [US1] Concurrency test: a trigger firing while a run of the same playbook is in
      flight does not start a second one (FR-016, US1 acceptance scenario 4)
- [ ] T069 [P] [US1] Manual-invocation test: a playbook invoked by hand runs immediately, is
      recorded as manually invoked, and is subject to the same bounds as a scheduled run (FR-022,
      US1 acceptance scenario 5)
- [ ] T070 [P] [US1] Sinks-only test: with an agent report asking for something no sink was
      declared to do, nothing outside the sinks is created or modified (FR-017, US1 acceptance
      scenario 7). This is Principle II's runtime property and the only test that asserts it

### Implementation for User Story 1

- [ ] T022 [US1] `internal/schedule`: cron parsing, the scheduler, missed-occurrence recording
- [ ] T023 [US1] `internal/stage/gather`: command execution into the working directory, output
      limits, per-step exit code and stderr capture
- [ ] T024 [US1] `internal/stage/agent`: build the argument vector from the playbook's agent
      declaration; spawn the child; pass credentials through the environment, never `argv`
      (FR-012)
- [ ] T025 [US1] `internal/stage/agent`: `stream-json` decoder — ignore unknown event types and
      unknown fields, so a CLI update degrades rather than breaks (research.md §2)
- [ ] T026 [US1] `internal/stage/agent`: timeout, cancellation and child cleanup on shutdown
- [ ] T027 [US1] Validate the agent report against the playbook's `output_schema` (FR-014)
- [ ] T028 [P] [US1] `internal/sink`: the sink interface, the cap contract, and per-sink outcome
      recording (FR-017, FR-025)
- [ ] T029 [P] [US1] `internal/sink/discord` (FR-023)
- [ ] T030 [P] [US1] `internal/sink/slack` (FR-023)
- [ ] T031 [US1] Copy gathered inputs into the record store **before** removing the working
      directory — the record is empty without this, and the ordering is the whole of it
- [ ] T032 [US1] `gronin run`: manual invocation, recorded as manually invoked and subject to the
      same bounds as a scheduled run (FR-022)
- [ ] T033 [US1] `gronin serve`: load, verify, arm, serve (FR-020)

**Checkpoint**: a playbook runs on its schedule and a report arrives. Not shippable — US2 and US3
are the rest of this release.

---

## Phase 4: User Story 2 — An over-reaching playbook is refused (P1)

**Goal**: nothing unsafe is ever armed, and a bound that was declared is a bound that was applied.

**Independent Test**: point the runtime at a directory of hostile playbooks; each is refused with
its field named. No trigger, no agent, no network.

**Dependency note**: the load gate (T037-T044) depends on nothing in US1. The receipt check (T046)
builds on the agent stage from T024, so it lands after it. Stated rather than hidden, because
"independently testable" would otherwise be a claim the phase cannot honour.

### Tests for User Story 2

- [ ] T034 [P] [US2] `testdata/playbooks/hostile/`: one playbook per refusal rule — bare shell in
      the tool set, a writing tool, an allowlist naming a whole MCP server, a file scope escaping
      the working directory (absolute and relative forms), an unknown MCP server, a creating sink
      with no cap, a `guard` block, a `retrieve` block, an unknown sink type, a bare interpolation
      missing its namespace, restricted execution turned off with no stated reason, a duplicate
      name, and an interpolation naming something only the environment has (FR-041, FR-042)
- [ ] T035 [P] [US2] `testdata/playbooks/valid/`: the accepting corpus. A denylist probed only on
      its refusals is an allowlist in disguise
- [ ] T071 [P] [US2] Credential test: with no source configured the runtime refuses to start and
      names the sources it consulted; with either of two sources configured it starts and reports
      which one the agent process named (FR-032, FR-033, SC-008)

### Implementation for User Story 2

- [ ] T037 [US2] `internal/playbook/validate`: the gate skeleton — collect every refusal, never stop
      at the first, and refuse the whole set rather than arming the valid remainder (FR-001)
- [ ] T038 [US2] Refuse a shell or a writing tool in the declared tool set (FR-003)
- [ ] T039 [US2] Refuse an allowlist entry naming a whole MCP server (FR-004)
- [ ] T040 [US2] Refuse a file scope resolving outside the working directory — resolve the path
      before deciding, so a relative traversal is caught (FR-005)
- [ ] T041 [US2] Refuse a creating sink without a cap (FR-006), and an MCP server this
      deployment does not provide (FR-007)
- [ ] T042 [US2] Refuse a `guard` or `retrieve` block (FR-034): a declared bound the runtime
      does not apply is worse than an absent one
- [ ] T043 [US2] Refusal output per `contracts/cli.md`: playbook, field, what was found, what would
      be accepted, and the closing line stating that nothing was armed (FR-002)
- [ ] T044 [US2] `gronin validate`: the same code path as `serve`, with no credential required so
      it can run in CI (FR-035). A validator that can disagree with the runtime is worse than none
- [ ] T045 [US2] Restricted execution as the default, overridable only with a stated reason
      (FR-013, FR-041)
- [ ] T046 [US2] The receipt check: compare the tool set and MCP servers the child reports in its
      first event against what the playbook declared, and abort before any model output when they
      differ (FR-018). Test it with the stub reporting a wider set than it was given (SC-009)
- [ ] T047 [US2] Agent version floor, checked at startup and reported by `gronin version` (FR-019)
- [ ] T048 [US2] Credential verification at startup, reporting the source the agent process names
      rather than asserting one (FR-032, FR-033)
- [ ] T072 [US2] Refuse a sink type this deployment does not implement (FR-038), an interpolation
      that omits its namespace (FR-039), and two playbooks declaring the same name (FR-042)
- [ ] T073 [US2] `gronin config set` and `gronin config list`, with secret values redacted on
      display (FR-040)
- [ ] T036 [US2] The mutation check SC-002 demands: for each refusal rule, remove its check from a
      copy of the validator and assert the suite fails, through the harness from T077. A refusal
      case that passes with its check deleted is not testing anything, and nothing else would ever
      tell you

T036 sits after the implementation deliberately. Every other test in this file is written first and
fails first; this one is a post-hoc audit of a finished gate and cannot run before there is a
validator to mutate. Listing it with the tests would have read as red-green and been wrong.

**Checkpoint**: nothing unsafe can be armed. Still not shippable — a runtime that records runs
nobody can read fails Principle III, which US3 closes.

---

## Phase 5: User Story 3 — Finding out why a run did what it did (P1)

**Goal**: a record that answers the question, and two ways to act on it that cost differently.

### Tests for User Story 3

- [ ] T049 [P] [US3] Record-completeness test: for a completed run, every field FR-026 lists is
      present and readable, and its terminal status distinguishes refused from failed (FR-037,
      SC-003)
- [ ] T050 [P] [US3] Replay test: recorded inputs are reused, gather does not re-execute, no trigger
      fires, and the replay is a distinct run linked to its parent (FR-027)
- [ ] T051 [P] [US3] Resume test: the recorded report is reused, the agent does not re-execute, and
      the resumed run reports zero additional token cost (FR-028, SC-004)
- [ ] T052 [P] [US3] Interruption test: a runtime killed mid-run marks that run interrupted on
      restart, keeps its partial record, and does not resume it (FR-031)

### Implementation for User Story 3

- [ ] T053 [US3] Record every action the agent attempted that its bounds refused (FR-026). Small
      table, and the only thing in the record that says a playbook's tool set is wrong (FR-037)
- [ ] T054 [US3] `internal/api`: the local HTTP API — list runs, read one, invoke, replay, resume.
      Loopback by default; binding elsewhere requires a configured credential first (FR-021,
      FR-036)
- [ ] T055 [P] [US3] `gronin runs` and `gronin show` (FR-021)
- [ ] T056 [P] [US3] `gronin replay` — re-run the agent against recorded inputs
- [ ] T057 [P] [US3] `gronin resume` — re-run only the sinks against the recorded report
- [ ] T058 [US3] Interrupted-run reconciliation on startup

**Checkpoint**: a surprising run can be understood and acted on without paying twice. **This is the
first tag**: US1, US2 and US3 together are the smallest thing that satisfies the constitution.

---

## Phase 6: User Story 4 — Issues, capped (P3)

### Tests for User Story 4

- [ ] T059 [P] [US4] Cap test: seven findings against a cap of three creates three and records four
      skipped
- [ ] T060 [P] [US4] Saturation test: with open issues already at the cap, nothing is created and
      the run is recorded capped, not failed
- [ ] T061 [P] [US4] Partial-failure test: a sink failing midway records which items were created
      and which were not

### Implementation for User Story 4

- [ ] T062 [US4] `internal/sink/github`: create issues, count open ones against the cap, and avoid
      re-opening a match (FR-023, FR-024)

---

## Phase 7: Polish

- [ ] T063 Ship the example playbook and prompt that `quickstart.md` walks through, and follow that
      walkthrough end to end on a clean machine (SC-001)
- [ ] T064 Retire one existing scheduled workflow and replace it with a playbook (SC-006). This is
      the only success criterion that cannot be satisfied by the test suite
- [ ] T065 [P] Release workflow: signed cross-compiled binaries and a `FROM scratch` image
- [ ] T066 [P] Update `README.md` — it currently says nothing executes yet, and by here that is
      false
- [ ] T067 [P] Publish `playbook.schema.json` at a stable URL so editors can resolve it

---

## Dependencies & Execution Order

- **Setup (Phase 1)** → **Foundational (Phase 2)** → everything else. T077 and T078 are part of
  Setup on purpose: they are what every later phase asserts through, and a harness written after
  the tests that need it gets shaped to agree with them.
- **US1 and US2 (Phases 3-4)** are one release. US2's load gate needs nothing from US1 and can be
  built in parallel with it; US2's receipt check (T046) needs T024.
- **US3 (Phase 5)** needs a run to exist, so it follows US1 — but it ships with it. It does not
  need US4.
- **T036 needs T037-T042 and T072.** It mutates the validator, so it cannot precede it. This is the
  one test in the file that is not red-green, and it is placed after the implementation for that
  reason.
- **US4 (Phase 6)** needs only the sink interface from T028.
- **Polish (Phase 7)** follows the stories it documents.

### Parallel opportunities

Within Phase 2, T008 / T009 and T011 are independent. Within Phase 3, the nine tests and the three
sink tasks are independent. Within Phase 4 the tests are, but **the refusal rules T038-T042 are
not**: they are five independent functions in one package, behind one gate, landing in the same
file. They carried a `[P]` until the second review round pointed out that the marker promises no
file conflict, not conceptual independence.

---

## Implementation Strategy

**The first shippable thing is US1 + US2 + US3 together.** Both halves of that are constitutional
constraints rather than preferences. A runtime that executes unvalidated playbooks hands a shell to
a model on someone else's machine, and shipping it "temporarily" is how that becomes permanent.
A runtime that records runs nobody can read, replay or resume fails Principle III just as squarely
— and it is also where a tool gets abandoned, at the first surprising run.

This was originally written as a two-story first release. The analysis pass found that it
contradicted Principle III, and the priority was what was wrong, not the principle.

Then US4, which is genuinely deferrable: the messaging sinks already prove the pipeline, and the
creating sink adds reach rather than correctness.

## Notes

- Every agent test uses the stub from T014. If a test needs a real token, it is in the wrong place,
  and T074 fails it there rather than leaving it to a reader.
- T036 is the task most likely to be skipped and the one that makes the rest mean anything. A green
  suite is not evidence that a check ran — which is why T077 has to be able to print zero before
  any count it produces is worth reading.
- Commit per task or per logical group. Stop at any checkpoint.
