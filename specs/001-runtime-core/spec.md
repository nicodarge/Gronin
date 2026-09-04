# Feature Specification: Runtime Core

**Feature Branch**: `add_speckit`

**Created**: 2026-09-04

**Status**: Draft

**Input**: User description: "The runtime core: load and validate playbooks, run them on a schedule, execute one bounded agent stage, route the result to a sink, and record every run so it can be inspected and replayed."

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
   startup fails naming the sink.
5. **Given** one invalid playbook among several valid ones, **When** the runtime loads them,
   **Then** no playbook is armed — the runtime refuses to start rather than starting partially.

---

### User Story 3 - An operator finds out why a run did what it did (Priority: P2)

A run produced a surprising report. The operator opens its record and sees the resolved playbook,
what each gather command returned, what the agent was asked, every tool call with its input and
output, how long each took, what it cost, and what each sink did. They replay the run against the
same inputs without re-triggering it.

**Why this priority**: what this replaces shows an operator where a run stopped and with what
data. Without it, adoption stalls at the first surprising run — but a first run has to exist
before there is anything to inspect, so it follows User Story 1 rather than blocking it.

**Independent Test**: execute a playbook, then reconstruct from its record alone what the agent
was asked and what it answered, without reading the runtime's logs. Replay it and confirm the
same inputs produce a second recorded run marked as a replay.

**Acceptance Scenarios**:

1. **Given** a completed run, **When** its record is read, **Then** it contains the resolved
   playbook, gathered inputs, the full prompt, every tool call with input and output, per-stage
   timings, token cost, and per-sink outcome.
2. **Given** a completed run, **When** it is replayed, **Then** the recorded inputs are reused,
   no trigger fires, and the replay is recorded as a distinct run linked to the original.
3. **Given** a run that failed at the sink stage, **When** its record is read, **Then** the agent
   report is present in full and the sink failure is attributed to the sink.
4. **Given** any recorded run, **When** its record is read, **Then** no credential value appears
   in it.

---

### User Story 4 - A playbook opens issues, and cannot flood the tracker (Priority: P3)

A playbook reports findings as issues in a tracker. It never opens more than its declared cap, and
it does not re-open an issue matching one already open.

**Why this priority**: the second sink proves the sink interface is genuinely an interface rather
than one destination with a plugin-shaped name. It is deferrable because the first sink already
demonstrates the pipeline end to end.

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
  and the run can be replayed against the sink alone.
- A playbook interpolates a name that resolves to nothing: refused at load, not at trigger time.
- The credential is missing or rejected at startup: the runtime refuses to start and names the
  credential source, rather than arming triggers that will each fail later.
- The system clock jumps or the runtime restarts across a scheduled time: a missed occurrence is
  not silently skipped without a record of the miss.
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

#### Execution

- **FR-009**: The runtime MUST execute a playbook on its declared schedule without operator
  interaction, and MUST support manual invocation of any playbook.
- **FR-010**: The runtime MUST execute gather steps before the agent stage, and MUST abort the run
  without invoking the agent if any gather step fails.
- **FR-011**: The runtime MUST execute each run in its own working directory, and MUST remove it
  when the run ends.
- **FR-012**: The runtime MUST enforce the declared tool set, MCP server list and allowlist on the
  agent, and MUST NOT allow a run to widen them.
- **FR-013**: The runtime MUST validate the agent's response against the declared output schema and
  MUST mark the run failed when it does not conform.
- **FR-014**: The runtime MUST terminate an agent stage that exceeds its declared timeout.
- **FR-015**: The runtime MUST NOT run two instances of the same playbook concurrently.
- **FR-016**: The runtime MUST pass side effects exclusively to sinks; the agent stage MUST NOT be
  granted tools that create, modify or delete outside its working directory.

#### Sinks

- **FR-017**: The runtime MUST support at least two sink types, one messaging and one creating.
- **FR-018**: A creating sink MUST NOT create more items than its declared cap, counting items it
  previously created and that remain open.
- **FR-019**: A sink failure MUST be recorded per sink and MUST NOT discard the agent report.

#### Recording

- **FR-020**: The runtime MUST record for every run: the resolved playbook, gathered inputs, the
  full prompt, every tool call with input and output, per-stage timings, token cost, terminal
  status, and per-sink outcome.
- **FR-021**: The runtime MUST allow a recorded run to be replayed from its recorded inputs
  without firing its trigger, recording the replay as a distinct run linked to the original.
- **FR-022**: The runtime MUST redact credential values from every record and log.
- **FR-023**: The runtime MUST record a scheduled occurrence that did not execute, and the reason.

#### Credentials

- **FR-024**: The runtime MUST support more than one credential source and MUST verify the
  configured credential at startup, refusing to start when it is absent or rejected.

### Key Entities

- **Playbook**: the declarative unit an operator writes and shares. Holds a name, a trigger, the
  gather steps, the agent declaration (model, prompt, tool set, MCP servers, allowlist, output
  schema, timeout) and the sinks. Contains no deployment-specific values.
- **Run**: one execution of one playbook. Holds a status, a working directory, the resolved
  playbook, timings and cost, and a link to the run it replays when it is a replay.
- **Gathered input**: a named artifact produced by a gather step and readable by the agent.
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
- **SC-002**: Every playbook in a hostile-playbook corpus covering all of FR-003 through FR-008 is
  refused at load, and every playbook in a valid corpus is accepted. Both corpora are part of the
  test suite, and each refusal case fails the suite when its check is removed.
- **SC-003**: For any completed run, an operator can state what the agent was asked, what it
  answered, and what each sink did, using only the run record.
- **SC-004**: A run whose sink failed can be replayed to completion without re-running the agent
  stage.
- **SC-005**: No credential value appears in any run record or log line, verified by scanning the
  full output of the test suite.
- **SC-006**: An existing scheduled workflow is retired and replaced by a playbook whose output the
  operator judges equivalent.

## Assumptions

- The full guard layer — cross-process locking, rate limiting and deduplication — is out of scope
  here and specified separately. This feature provides only the single-process guarantee that one
  playbook does not run twice concurrently (FR-015).
- Semantic retrieval is out of scope here. A playbook that declares no retrieval simply runs
  without prior context, and the field is reserved rather than implemented.
- Webhook triggers are out of scope here; only schedule and manual invocation are in scope.
- One agent stage per playbook. Multi-stage playbooks, conditional branching and iteration between
  stages are deliberately excluded until a real playbook needs them.
- The deployment provides the MCP servers a playbook names; discovering or installing them is out
  of scope.
- Run records are retained locally. Retention policy and external export are out of scope.
- Operators are comfortable editing YAML and reading a prompt file. There is no authoring UI.
