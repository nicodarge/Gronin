# Phase 1 — Data Model

Two stores, on purpose. A relational store holds what is queried — status, timing, cost, outcome.
A blob directory holds what is only ever read whole — gathered inputs, the prompt as sent, the raw
transcript. Putting a transcript in a row makes the database unreadable with ordinary tools and
unbackupable with ordinary habits, and it is never selected on.

## Entities

### Playbook

Not persisted. Loaded from disk at startup, validated, and held in memory; the *resolved* copy is
written into each run's record so a record stays readable after the file changes underneath it.

| Field | Type | Notes |
| ----- | ---- | ----- |
| `name` | string | Unique across the loaded set; refused at load if duplicated |
| `description` | string | Required when `agent.restricted` is false |
| `trigger` | one of: cron, manual | Webhook triggers are a later feature |
| `gather[]` | list of steps | Each has a command and the name it writes |
| `agent` | see below | Exactly one |
| `sinks[]` | list | At least one |

The agent declaration is where the bounds live:

| Field | Type | Notes |
| ----- | ---- | ----- |
| `model` | string | Passed through; not interpreted |
| `prompt_file` | path | Relative to the playbook |
| `restricted` | bool | Default true. False requires `description` to say why |
| `tools[]` | list | The replacing tool set. Refused if it contains a shell or a writing tool |
| `mcp[]` | list | Server names this deployment must provide, else refused |
| `allow[]` | list | Path-scoped file tools and individually named MCP tools only |
| `output_schema` | JSON Schema | The agent report is validated against it |
| `timeout` | duration | The stage is killed at it |

### Run

The central row. One per execution, including replays and resumes.

| Field | Type | Notes |
| ----- | ---- | ----- |
| `id` | identifier | Assigned by the runtime |
| `playbook_name` | string | The name at the time it ran |
| `resolved_playbook` | blob ref | The playbook as actually used |
| `trigger_kind` | enum | `schedule`, `manual`, `replay`, `resume` |
| `parent_run_id` | identifier, nullable | Set on a replay or a resume; names the run it derives from |
| `status` | enum | `running`, `succeeded`, `failed`, `timed_out`, `capped`, `interrupted`, `refused`, `claim_lost` |
| `started_at`, `ended_at` | timestamp | Host clock, UTC. Never a payload timestamp |
| `cost_usd`, `tokens` | numeric | From the agent stage's terminal event |
| `agent_session_id` | string | The agent process's own session identifier, for correlation |
| `credential_source` | string | As reported by the agent process, not asserted |
| `error` | text, nullable | Terminal failure, already redacted |
| `waiting_trigger_id` | identifier, nullable | The waiting trigger this run started from, from the guard feature. Empty for a run that did not wait |
| `waited_ms` | integer, nullable | From acceptance to the run's start, on the monotonic reading of the process that waited. Null for a run that did not wait |
| `claim_reach` | enum, nullable | `cross-host` or `single-host` — which guarantee this run actually ran under, from the guard feature |
| `claim_token` | integer, nullable | The fencing token the run held; null on a single-host deployment, which has none |

`trigger_kind` deserves a note before `status` does. Its values are not the playbook's
`trigger.type` values and are not meant to match them: a playbook is triggered by `cron` or
`manual`, while a run records how *it* came to exist — including `replay` and `resume`, which no
playbook can declare. Two axes, deliberately.

`status` deserves one note. `refused` is not the same as `failed`: a refused run never started,
because the bounds receipt did not match the declaration or a gather step failed. It costs nothing
and it is not an incident — but it must be visible, because a playbook that is refused every night
is broken in a way that silence would hide. `claim_lost`, added by the guard feature
([specs/002-guard/data-model.md](../002-guard/data-model.md)), is neither: the run was stopped
because its claim could no longer be proven held, nothing in the run itself went wrong, and it is
not `interrupted`, which is reserved for a run the next process finds still `running`.

### Gathered input

| Field | Type | Notes |
| ----- | ---- | ----- |
| `run_id` | identifier | |
| `name` | string | As declared by the gather step |
| `blob_ref` | path | Under the run's record directory |
| `bytes`, `truncated` | integer, bool | Truncation at the configured limit is recorded, not silent |
| `exit_code`, `stderr` | integer, blob ref | A failing step is why the run was refused |

Copied into the record store **before** the working directory is removed. FR-011 removes the
directory at the end of every run and FR-026 requires the inputs to survive it, so the copy is not
an optimisation — the record is empty without it.

### Agent report

The structured result, valid against the playbook's output schema. Stored whole as a blob and
referenced by the run. It is the only thing a sink reads, and it is what a resume replays against.

### Tool call

| Field | Type | Notes |
| ----- | ---- | ----- |
| `run_id`, `sequence` | identifier, integer | Ordered as received |
| `name` | string | |
| `input`, `output` | blob ref | Redacted at the write boundary |
| `started_at`, `duration_ms` | timestamp, integer | |
| `outcome` | enum | `ok`, `error` |

### Refused action

Every action the agent attempted that its bounds rejected, as reported by the agent process itself.

| Field | Type | Notes |
| ----- | ---- | ----- |
| `run_id` | identifier | |
| `tool` | string | What was reached for |
| `reason` | text | As reported |

Small table, high value. A run that succeeds while repeatedly reaching for something it cannot have
is telling you its declared tool set is wrong, or that its prompt is steering somewhere it should
not. Nothing else in the record surfaces that.

### Sink outcome

| Field | Type | Notes |
| ----- | ---- | ----- |
| `run_id`, `sink` | identifier, string | |
| `status` | enum | `delivered`, `created`, `skipped`, `capped`, `failed` |
| `items_created`, `items_skipped` | integer | A creating sink accounts for both |
| `detail` | text | Per-item outcome for a partial failure |

Recorded per sink, so one sink failing does not make the run's other deliveries unknowable.

## Redaction

Applied at the write boundary of the record store and of the logger — not at read time, and not by
the callers. A caller that must remember to redact will eventually forget, and the value is durable
by then.

The redactor is seeded from the deployment's own configured secret values. This catches what the
deployment knows about itself; it does not catch a secret that appears for the first time inside a
gathered input. That limit is real and is stated here rather than papered over: `gather` steps must
not print secrets, the same rule the constitution already states for command lines.

### Missed occurrence

| Field | Type | Notes |
| ----- | ---- | ----- |
| `playbook_name` | string | |
| `due_at` | timestamp | When it should have run |
| `noticed_at` | timestamp | When the runtime saw that it had not |
| `reason` | text | Why, e.g. the previous occurrence was still running |

Not a run with an unusual status: nothing about it has a working directory, a cost or an outcome.
It is here because FR-030 needs somewhere to put it, and because silence about a schedule that did
not fire is indistinguishable from one that fired and found nothing to say.

## What is deliberately absent

No table for guard state, retrieval results, or webhook deliveries. Those belong to features that
do not exist yet, and a schema that anticipates them would be asserting a design nobody has
validated.
