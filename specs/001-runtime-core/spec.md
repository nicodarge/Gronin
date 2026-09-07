# Feature Specification: Runtime Core

**Feature Branch**: `add_speckit`

**Created**: 2026-09-04

**Status**: Draft

**Input**: User description: "The runtime core: load and validate playbooks, run them on a schedule, execute one bounded agent stage, route the result to a sink, and record every run so it can be inspected and replayed."

## Clarifications

### Session 2026-09-04

- Q: Which language and execution model for the agent stage? → A: Go. The Claude Agent SDK exists only for Python and TypeScript, so the runtime drives the Claude Code CLI directly over its documented `--print --output-format stream-json` contract — the same contract the SDK wraps, and the same one the containment flags belong to. Distribution as a single static binary was judged worth more than a language-specific SDK wrapper.
- Q: What does replaying a run mean? → A: Two distinct verbs. `replay` re-runs the agent stage against the recorded inputs; `resume` re-runs only the sinks against the report already produced. Different costs, different purposes, no ambiguity.
- Q: How does an operator invoke a playbook manually and inspect a run? → A: One binary that is both daemon and client. `gronin serve` runs the scheduler and a local API; `gronin run`, `gronin runs`, `gronin show`, `gronin replay` and `gronin resume` are clients of that API. A web dashboard is deferred to a later feature and will be served by the same API.
- Q: Which concrete sinks ship first? → A: Three — Discord, Slack, and GitHub issues. Two messaging implementations validate the sink interface properly, and nothing is left to build before the repository goes public. (Superseded 2026-09-07: phase 1 no longer means the repository goes public — see docs/roadmap.md. The sink answer itself stands.)

## User Scenarios & Testing *(mandatory)*

### User Story 1 - A scheduled playbook produces a report where the operator works (Priority: P1)

An operator writes a playbook: a schedule, a couple of commands that gather context, a prompt, a
declared tool set, and a destination. They start the runtime. At the scheduled time the runtime
gathers the inputs, runs one agent against them, and posts the resulting report to their chat
channel. Nobody watches it happen.

**Why this priority**: this is the entire product in one slice. Without it there is nothing to
inspect, bound or extend. It is also the smallest thing that replaces an existing workflow.

**Independent Test**: write a playbook whose gather step reads a fixture directory and whose sink
is a chat channel, set the schedule to the next minute, start the runtime, and confirm a report
arrives that references the fixture content. No other user story needs to exist.

**Acceptance Scenarios**:

1. **Given** a valid playbook with a cron trigger due in one minute, **When** the runtime is
   started, **Then** exactly one run executes at the scheduled time and its report reaches the
   declared sink.
2. **Given** a playbook whose gather command exits non-zero, **When** the trigger fires, **Then**
   the run stops before the agent stage, no tokens are spent, and the failure is reported to the
   sink with the command and its stderr.
3. **Given** a playbook whose agent returns a response that does not satisfy the declared output
   schema, **When** the run completes, **Then** the run is marked failed and the sink receives the
   validation failure rather than the malformed content.
4. **Given** a run already in progress for a playbook, **When** its trigger fires again, **Then**
   the second run does not start concurrently.
5. **Given** any playbook, **When** an operator invokes it manually rather than waiting for its
   schedule, **Then** it executes immediately and its run is recorded as manually invoked.
6. **Given** a running playbook, **When** the run ends for any reason, **Then** its working
   directory no longer exists.
7. **Given** a playbook whose agent report asks for something no sink was declared to do, **When**
   the run completes, **Then** nothing outside the sinks is created or modified.

---

### User Story 2 - An over-reaching playbook is refused before it can run (Priority: P1)

An operator adds a playbook that asks for an unrestricted shell, or names a whole MCP server
rather than individual tools, or scopes a file tool outside the run directory. The runtime refuses
to start, names the playbook, names the field, and says what would have been permitted.

**Why this priority**: also P1, and deliberately so. Constitution Principle I forbids shipping
User Story 1 without it — a runtime that executes unvalidated playbooks hands a language model a
shell on a machine its author does not own. The two ship together or not at all.

**Independent Test**: point the runtime at a directory of deliberately hostile playbooks and
confirm each is refused with the field named. Requires no trigger, no agent and no network.

**Acceptance Scenarios**:

1. **Given** a playbook declaring a bare shell tool, **When** the runtime loads it, **Then**
   startup fails, the message names the playbook and the `tools` field, and no trigger is armed.
2. **Given** a playbook whose allowlist names an MCP server rather than a tool within it, **When**
   the runtime loads it, **Then** startup fails and the message names the offending entry.
3. **Given** a playbook whose file-tool scope resolves outside the run's working directory,
   **When** the runtime loads it, **Then** startup fails, including when the escape is expressed
   through a relative path.
4. **Given** a playbook with a creating sink and no cap, **When** the runtime loads it, **Then**
   startup fails naming the sink and its missing `cap` field.
5. **Given** one invalid playbook among several valid ones, **When** the runtime loads them,
   **Then** no playbook is armed — the runtime refuses to start rather than starting partially.
6. **Given** a playbook that does not explicitly opt out of restricted execution, **When** it
   runs, **Then** the agent is started with the command- and code-running built-in tools removed
   at the process level, independently of its declared tool set.
7. **Given** an agent process that reports receiving a tool set wider than the playbook declared,
   **When** the run starts, **Then** it aborts before any model output is produced and the
   discrepancy is recorded.
8. **Given** an agent executable older than the verified version floor, **When** the runtime
   starts, **Then** it refuses to start and names the version it found and the one it requires.

---

### User Story 3 - An operator finds out why a run did what it did (Priority: P1)

A run produced a surprising report. The operator opens its record and sees the resolved playbook,
what each gather command returned, what the agent was asked, every tool call with its input and
output, how long each took, what it cost, and what each sink did. They then either re-run the
agent against the same recorded inputs to test a prompt change, or re-run only the sinks against
the report already produced.

**Why this priority**: P1, and not by preference. Constitution Principle III requires that a run
be replayable and resumable from its record, and that inspection ship with the stage it inspects —
so a release that records runs nobody can read does not satisfy it. This story was first written as
P2 and the analysis pass caught the contradiction: the plan claimed compliance because replay and
resume are in the feature, while the task strategy drew a release boundary underneath them. The
priority was what was wrong, not the principle.

It still depends on User Story 1 — a run has to exist before there is anything to inspect — so it
is built after it. It ships with it.

**Independent Test**: execute a playbook, then reconstruct from its record alone what the agent
was asked and what it answered, without reading the runtime's logs. Replay it and confirm the
same inputs produce a second recorded run marked as a replay.

**Acceptance Scenarios**:

1. **Given** a completed run, **When** its record is read, **Then** it contains the resolved
   playbook, gathered inputs, the full prompt, every tool call with input and output, per-stage
   timings, token cost, and per-sink outcome.
2. **Given** a completed run, **When** it is replayed, **Then** the recorded inputs are reused,
   the gather stage does not re-execute, no trigger fires, and the replay is recorded as a
   distinct run linked to the original.
3. **Given** a run whose agent succeeded and whose sink failed, **When** it is resumed, **Then**
   the recorded report is reused, the agent stage does not re-execute, no tokens are spent, and
   only the sinks run again.
4. **Given** a run that failed at the sink stage, **When** its record is read, **Then** the agent
   report is present in full and the sink failure is attributed to the sink.
5. **Given** any recorded run, **When** its record is read, **Then** no credential value appears
   in it.

---

### User Story 4 - A playbook opens issues, and cannot flood the tracker (Priority: P3)

A playbook reports findings as issues in a tracker. It never opens more than its declared cap, and
it does not re-open an issue matching one already open.

**Why this priority**: the creating sink is the one whose failure mode is loud and public, and it
is what makes a reported finding actionable rather than merely visible. It is deferrable because
the messaging sinks already demonstrate the pipeline end to end.

**Independent Test**: run a playbook whose agent returns more findings than the cap and confirm
only the cap is created, then run it again unchanged and confirm nothing further is created.

**Acceptance Scenarios**:

1. **Given** a cap of three and an agent reporting seven findings, **When** the sink runs, **Then**
   three are created and the four skipped are recorded in the run record.
2. **Given** open issues already at the cap, **When** the sink runs, **Then** nothing is created
   and the run is recorded as capped rather than failed.
3. **Given** a sink that fails partway through creating items, **When** the run ends, **Then** the
   record states which items were created and which were not.

---

### Edge Cases

- The agent stage exceeds its declared timeout: the run is terminated, marked timed out, and the
  partial transcript is retained in the record.
- A sink fails after the agent succeeded: the report is preserved in the record so no work is lost
  and the run can be resumed against the sinks alone.
- A playbook interpolates a name that resolves to nothing: refused at load, not at trigger time.
- No credential source is configured, or the configured one is rejected: the runtime refuses to
  start and names the source it tried, rather than arming triggers that will each fail later.
- The system clock jumps or the runtime restarts across a scheduled time: a missed occurrence is
  not silently skipped without a record of the miss.
- The runtime is stopped while a run is in flight: on restart that run is marked interrupted with
  its partial record intact, and is never silently resumed.
- A gather command writes more output than the run is allowed to hold: truncated at a declared
  limit, with the truncation recorded.
- Two playbooks declare the same name: refused at load.

## Requirements *(mandatory)*

### Functional Requirements

#### Loading and validation

- **FR-001**: The runtime MUST load every playbook from a configured directory at startup and MUST
  refuse to start if any one of them is invalid.
- **FR-002**: The runtime MUST validate each playbook against a published schema and MUST report
  the playbook name and the offending field on failure.
- **FR-003**: The runtime MUST refuse a playbook whose declared tool set includes an unrestricted
  shell, or a tool capable of writing outside the run's working directory.
- **FR-004**: The runtime MUST refuse a playbook whose allowlist names a whole MCP server rather
  than individual tools within it.
- **FR-005**: The runtime MUST refuse a playbook whose file-tool scope resolves outside the run's
  working directory, including through relative traversal.
- **FR-006**: The runtime MUST refuse a playbook declaring a creating sink without a cap.
- **FR-007**: The runtime MUST refuse a playbook naming an MCP server that this deployment does
  not provide.
- **FR-008**: The runtime MUST resolve interpolation only against the trigger payload and the
  deployment configuration, and MUST NOT expose the process environment to interpolation.
- **FR-034**: The runtime MUST refuse a playbook declaring a capability it does not yet apply — a
  `guard` or a `retrieve` block — rather than accepting it and ignoring the block. A declared bound
  nothing enforces is worse than an absent one, because it reads as enforced in review.
- **FR-038**: The runtime MUST refuse a playbook naming a sink type this deployment does not
  implement, on the same terms as FR-007 refuses an unknown MCP server. Without it a mistyped sink
  name survives the gate and fails at delivery, after a full agent run has been paid for.
- **FR-041**: The runtime MUST refuse a playbook that turns off restricted execution without
  stating why in its description. Setting the flag is a decision about the coarsest bound, and a
  decision nobody wrote down is indistinguishable from an accident on review.
- **FR-042**: The runtime MUST refuse a set of playbooks in which two declare the same name. A run
  record names the playbook it ran, so two playbooks answering to one name make every record
  ambiguous after the fact.
- **FR-039**: Interpolation MUST name its source — the deployment configuration or the trigger
  payload — and the runtime MUST refuse a reference that does not. A bare reference resolves
  against whichever source happens to hold the name, so a trigger payload, which for a webhook is
  written by whoever sends the request, can shadow a configuration value.

#### Execution

- **FR-009**: The runtime MUST execute a playbook on its declared schedule without operator
  interaction.
- **FR-010**: The runtime MUST execute gather steps before the agent stage, and MUST abort the run
  without invoking the agent if any gather step fails.
- **FR-011**: The runtime MUST execute each run in its own working directory, and MUST remove it
  when the run ends.
- **FR-012**: The runtime MUST enforce the declared tool set, MCP server list and allowlist on the
  agent, and MUST NOT allow a run to widen them.
- **FR-013**: The runtime MUST start the agent with the command- and code-running built-in tools
  removed at the process level by default, independently of the declared tool set. A playbook MAY
  opt out only by declaring the opt-out explicitly.
- **FR-014**: The runtime MUST validate the agent's response against the declared output schema and
  MUST mark the run failed when it does not conform.
- **FR-015**: The runtime MUST terminate an agent stage that exceeds its declared timeout.
- **FR-016**: The runtime MUST NOT run two instances of the same playbook concurrently.
- **FR-017**: The runtime MUST pass side effects exclusively to sinks. No stage other than a sink
  may reach anything outside the run's working directory. (The tool-set half of this bound is
  FR-003, which refuses such a tool at load.)
- **FR-018**: The runtime MUST read the agent process's own report of the tool set and MCP servers
  it received, MUST compare it against what the playbook declared, and MUST abort the run before
  any model output is produced when the two differ.
- **FR-019**: The runtime MUST verify at startup that the agent executable is at or above the
  version on which its bounding flags were confirmed, and MUST refuse to start otherwise — a
  bounding flag an older executable does not recognise is ignored rather than refused, which leaves
  a run unbounded while appearing bounded.

#### Operator surface

- **FR-020**: The runtime MUST be a single executable that both runs the scheduler and serves as
  the client for every operator action.
- **FR-021**: Operators MUST be able to invoke any playbook immediately, list runs, read one run's
  record, replay a run and resume a run, without stopping the scheduler.
- **FR-022**: A manually invoked run MUST be recorded as manually invoked and MUST be subject to
  the same validation and bounds as a scheduled one.
- **FR-035**: The runtime MUST offer a way to run the load gate alone, exiting non-zero on refusal,
  without requiring a credential and without arming anything — so the gate can run wherever the
  playbooks are reviewed. It MUST be the same code path the scheduler runs: a validator that can
  disagree with the runtime is worse than none.
- **FR-036**: The operator API MUST bind a loopback address by default, and binding it to any other
  address MUST require a credential to be configured first. An unauthenticated listener that can
  invoke playbooks is a remote execution surface, and the default must not be one step from it.
- **FR-040**: Operators MUST be able to set and inspect the deployment configuration values that
  playbooks interpolate against, without editing a file the runtime also writes. Inspecting a
  secret value MUST redact it.

#### Sinks

- **FR-023**: The runtime MUST support at least three sinks: two messaging and one creating.
- **FR-024**: A creating sink MUST NOT create more items than its declared cap, counting items it
  previously created and that remain open.
- **FR-025**: A sink failure MUST be recorded per sink and MUST NOT discard the agent report.

#### Recording

- **FR-026**: The runtime MUST record for every run: the resolved playbook, gathered inputs, the
  full prompt, every tool call with input and output, per-stage timings, token cost, how the run
  was triggered, terminal status, per-sink outcome, and every action the agent attempted that its
  bounds refused.
- **FR-027**: The runtime MUST support replaying a recorded run — re-executing the agent stage
  against the recorded inputs without re-running gather and without firing the trigger — and MUST
  record the replay as a distinct run linked to the original.
- **FR-028**: The runtime MUST support resuming a recorded run — re-executing only its sinks
  against the recorded agent report, without re-executing the agent stage.
- **FR-029**: The runtime MUST redact credential values from every record and log.
- **FR-030**: The runtime MUST record a scheduled occurrence that did not execute, and the reason.
- **FR-031**: The runtime MUST mark a run interrupted by runtime shutdown as interrupted, retain
  its partial record, and MUST NOT resume it automatically on restart.
- **FR-037**: A run's terminal status MUST distinguish a run that was refused before it started
  from one that ran and failed. A playbook refused every night costs nothing and is not an
  incident, but it is broken, and one status for both hides that.

#### Credentials

- **FR-032**: The runtime MUST accept a credential from more than one source and MUST report which
  source was used, reading that from the agent process's own report rather than asserting it.
- **FR-033**: The runtime MUST verify the configured credential at startup and MUST refuse to
  start when it is absent or rejected.

### Key Entities

- **Playbook**: the declarative unit an operator writes and shares. Holds a name, a trigger, the
  gather steps, the agent declaration (model, prompt, tool set, MCP servers, allowlist, output
  schema, timeout) and the sinks. Contains no deployment-specific values.
- **Run**: one execution of one playbook. Holds a status, how it was triggered, a working
  directory, the resolved playbook, timings and cost, and a link to the run it replays or resumes.
- **Gathered input**: a named artifact produced by a gather step into the run's working directory
  and readable by the agent.
- **Agent report**: the structured result of the agent stage, valid against the playbook's output
  schema; the only thing a sink is allowed to act on.
- **Tool call**: one tool invocation within a run, with its input, output, duration and outcome.
- **Sink outcome**: what one sink did for one run — created, skipped, capped or failed — item by
  item.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: An operator who has never seen this runtime can take a documented example playbook,
  change its schedule and its destination, and get a report delivered, in under 30 minutes and
  without reading the runtime's source.
- **SC-002**: Every playbook in a hostile-playbook corpus covering FR-003 through FR-008, FR-013,
  FR-034, FR-038, FR-039, FR-041 and FR-042
  is refused at load, and every playbook in a valid corpus is accepted. Both corpora are part of
  the test suite, and each refusal case fails the suite when its check is removed.
- **SC-003**: For any completed run, an operator can state what the agent was asked, what it
  answered, and what each sink did, using only the run record.
- **SC-004**: A run whose sink failed can be resumed to completion without re-running the agent
  stage, and the resumed run reports zero additional token cost.
- **SC-005**: No credential value appears in any run record or log line, verified by scanning the
  full output of the test suite.
- **SC-006**: An existing scheduled workflow is retired and replaced by a playbook whose output the
  operator judges equivalent.
- **SC-007**: The runtime is installable as a single self-contained executable with no language
  runtime, package manager or interpreter present on the target machine.
- **SC-008**: With the deployment's credential removed, the runtime refuses to start and names the
  sources it consulted; with either of two configured sources present, it starts and reports which
  one was used.
- **SC-009**: A run whose agent process reports a tool set wider than declared is aborted with no
  model output produced, demonstrated by a stub agent that reports a wider set than it was given.
- **SC-010**: The full test suite passes on a machine with no route to the network and no
  configured credential, and no test in it spends a token.
- **SC-011**: The suite passes under the race detector with test caching disabled, and ten
  consecutive runs against an unchanged tree agree.
- **SC-012**: Every merge is gated on lint, `go vet`, the suite under the race detector, the
  mutation check in both its uses — the refusal corpus of SC-002 and the harness self-check of
  SC-014 — the binary-level test of SC-013, and the static-link check of SC-007. A change failing
  any one of them cannot be merged.
- **SC-013**: At least one test drives the built executable through the operator's own command
  surface rather than the packages behind it, so a change that breaks the argument vector, the
  embedded schema or the static linkage fails the suite rather than only the release.
- **SC-014**: The mutation harness reports zero surviving mutants on an unmodified tree. A harness
  that cannot report zero cannot report a survivor either, and its counts mean nothing.

## Assumptions

- **Guard is out of scope.** Rate limiting and deduplication are specified separately. This
  feature provides the guarantee that one playbook does not run twice concurrently on the same
  host (FR-016) — an in-memory claim plus an advisory file lock held for the life of the run, so
  two `gronin` processes sharing a state directory are covered as well as two triggers racing
  within one. It does not extend across hosts; that is phase 3's guard specification. A playbook
  declaring a `guard` block is refused, per FR-034.
- **Retrieve is out of scope.** Semantic retrieval is specified separately. A playbook declaring a
  `retrieve` block is refused on the same terms, per FR-034.
- **Webhook triggers are out of scope.** Only schedule and manual invocation are in scope.
- **One agent stage per playbook.** Multi-stage playbooks, conditional branching and iteration
  between stages are deliberately excluded until a real playbook needs them.
- **The agent stage is driven through the Claude Code command-line contract**, not through a
  language-specific agent SDK. The bounds in FR-012 and FR-013 are expressed as that contract's
  own execution flags.
- **A web dashboard is out of scope**, deferred to a later feature. The operator surface here is
  the executable itself.
- **The deployment provides the MCP servers a playbook names**; discovering or installing them is
  out of scope.
- **Run records are retained locally.** Retention policy and external export are out of scope.
- **Operators are comfortable editing YAML and reading a prompt file.** There is no authoring UI.
