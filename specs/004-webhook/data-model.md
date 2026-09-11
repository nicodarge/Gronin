# Phase 1 — Data Model

Three places hold this feature's state.

- **The deployment's configuration** holds the sources: who may send, under which secret, in which
  header, with which identity and which replay window. Read at startup, like the playbooks.
- **The record store** of the host that received a delivery holds everything that follows from it:
  the delivery, the identity it was accepted under, one hand-off per bound playbook, and every
  refusal. It is the only memory repeat detection has, which is why its reach is this host's
  (FR-319).
- **Process memory** holds nothing a restart would need. A delivery's exact body is held there only
  while one of its hand-offs waits under the guard, and that waiting trigger dies with the process by
  the guard's own rule.

The guard's records — its refusals and its waiting triggers — gain a delivery link and a trigger kind.
They stay the guard's: this feature adds a column to each, and nothing else.

## Entities

### Source (in the deployment)

`sources.json` in the state directory, beside `config.json` and `mcp_servers.json`. Absent is a
deployment that configures no source. Its shape is in [contracts/cli.md](./contracts/cli.md); an
unknown key is refused at startup like an unknown playbook key.

| Field | Type | Notes |
| ----- | ---- | ----- |
| name | slug, the object key | `^[a-z0-9][a-z0-9-]*$`. What a playbook binds to and what the route names: `POST /hooks/<name>` |
| `secret` | reference | A `${config.x}` reference, never a literal, naming a value marked secret. A reference to a value not marked secret is refused: `gronin config list` would print it, and FR-306's redaction would hold everywhere but there |
| `signature_header` | header name | Required (FR-306). The header the sender puts its signature in |
| `signature_prefix` | string | Optional, default empty. Text the header value starts with before the hexadecimal MAC — `sha256=` for GitHub's header. Matched exactly |
| `identity` | JSON Pointer | Optional. Where in the body the delivery's identity is (FR-316). Absent, the identity is the SHA-256 digest of the exact body |
| `replay_window` | duration | Optional, default 10 m (plan, *Constraints*). Greater than zero |

The secret is resolved from the configuration when the ingress starts and seeds the redactor through
the configuration's own secret marking. The source listing (FR-311) never resolves it.

**Validation at startup**: an unknown key; a missing header; a header that is not a valid field
name; a secret given as a literal, naming an unconfigured key, or naming a key not marked secret; an
identity that is not a JSON Pointer; a window that does not parse or is not positive. Every problem
is reported at once, naming the source and the field.

### Webhook trigger (in the playbook)

The `trigger` block with `type: webhook`, validated at load. Its schema is
[contracts/webhook-trigger.schema.json](./contracts/webhook-trigger.schema.json), which replaces the
runtime core's `trigger` property when this feature lands.

| Field | Type | Notes |
| ----- | ---- | ----- |
| `source` | slug | A source the deployment configures, or the playbook is refused naming the ones it does (FR-310) |
| `values.<name>.at` | JSON Pointer | Where the value is in the body |
| `values.<name>.pattern` | RE2 expression | Matched against the whole value: the runtime anchors it (FR-322) |
| `values.<name>.max_length` | integer ≥ 1 | In Unicode code points, not bytes (FR-322) |

`<name>` has the shape of a configuration key, because it is referenced as `${trigger.<name>}`.

**Load-time rules**, beyond the schema's shape (FR-310, FR-322, FR-324, FR-325):

- the source is configured;
- every declared value has all three fields, its pointer parses, its pattern compiles — the schema
  requires the fields too, and the gate holds the rule where it can say why, as it already does for
  the reserved blocks;
- the prompt body holds no `${trigger.` reference;
- no sink field holds a `${trigger.` reference;
- a gather step references only declared values;
- no gather step writes a file named `trigger.json`, which is the data file's name.

A `cron` or `manual` trigger declaring `source` or `values` is refused by the schema.

### Declared value (at hand-off, never stored on its own)

What a delivery supplies for one declared name. Extracted from the body at the declared pointer, or
taken from `--trigger name=value` on a manual invocation (FR-327). The same function checks both.

| Body holds | Value | Accepted? |
| ---------- | ----- | --------- |
| a JSON string | the decoded string | if it matches and fits |
| a JSON number | the literal the sender wrote, never re-rendered from a float (FR-316) | if it matches and fits |
| `true` / `false` | `true` / `false` | if it matches and fits |
| `null`, an object, an array, or nothing at the pointer | — | no: not a single value, or absent |

A value that is not a single value, is absent, has more code points than `max_length`, or does not
wholly match its pattern is refused for that playbook, naming the playbook and the value (FR-322).
A value referenced by a gather step and beginning with `-` is refused the same way (FR-326). Both are
decided before the trigger reaches the guard, so a refused value produces no run and occupies no
waiting slot.

### Data file (in the run's working directory)

`trigger.json`: the declared values, as one JSON object of strings, written into the working
directory when the run begins and before its gather steps, and recorded as a gathered input named
`trigger.json` (FR-324). It is the only way a declared value reaches the agent; a replay restores it
like every other gathered input (FR-336). It holds nothing the playbook did not declare (FR-323).

Written when the run begins rather than at hand-off, because the working directory does not exist
until the guard has admitted the run.

### Delivery (in the record store)

One authenticated request that was accepted — new, or a retry of a dropped one.

| Field | Type | Notes |
| ----- | ---- | ----- |
| `id` | identifier | The run identifier's shape, so the blob store can file its body under it |
| `source` | slug | |
| `identity` | text | The declared value, or the body's SHA-256 in hexadecimal |
| `identity_kind` | enum | `declared` or `digest` |
| `received_at` | timestamp | The host clock's wall reading, UTC (FR-320) |
| `peer` | address | The connection's own (FR-334) |
| `body_ref` | blob ref | The body, redacted at the write boundary like every blob |
| `body_sha256` | hex | Over the exact bytes received, before redaction |
| `repeats` | integer | Repeats counted inside the window (FR-317) |
| `instance` | identifier | The accepting process, whose instance lock — the guard's — decides whether an `accepted` delivery is really in flight |
| `supersedes` | identifier, nullable | For a retry of a dropped delivery, the dropped one |
| `state` | enum | See below |

```text
accepted ─ every hand-off decided ─────────────────────────▶ handed_off
         ─ accepting process gone with a hand-off pending ─▶ dropped
unbound    (no playbook was bound to the source at acceptance)
```

A delivery's body is readable through the operator's surface and through no surface the ingress
serves (FR-335).

### Delivery identity (in the record store)

What repeat detection consults. Keyed on `(source, identity)`, pointing at the delivery currently
holding it.

| Field | Type | Notes |
| ----- | ---- | ----- |
| `source`, `identity` | text | The key |
| `delivery_id` | identifier | The delivery this identity was last accepted as |
| `accepted_at` | timestamp | Wall reading, UTC. The window is measured from it |

A separate table rather than a key on the delivery itself: when an identity recurs after its window,
the identity is repointed at a new delivery and the earlier delivery keeps its row. Keyed on the
delivery, the recurrence would overwrite the earlier one's record.

**The acceptance** is one write transaction (FR-318), taken with the write lock held from its first
statement, so a second acceptance of the same identity waits for the first to commit rather than
reading around it:

- the identity is **new** when no row holds it; when `now − accepted_at ≥ replay_window`; or when the
  delivery it points at is `dropped` (FR-315);
- new: a delivery row is inserted, the identity row inserted or repointed with `accepted_at = now`,
  and one hand-off row per playbook bound to the source — for a retry of a dropped delivery, only
  the bound playbooks the dropped one had not reached;
- a repeat: the delivery the identity points at gains one in `repeats`, and nothing else is written.

`now` is the runtime clock's wall reading (FR-320). The window is compared against a recorded
timestamp that another process may have written, so the monotonic reading, which does not survive
the process, cannot be the one used.

### Hand-off (in the record store)

One per delivery and bound playbook, created with the delivery.

| Field | Type | Notes |
| ----- | ---- | ----- |
| `delivery_id`, `playbook_name` | key | |
| `state` | enum | `pending`, `handed_off`, `refused`, `dropped` |
| `decided_at` | timestamp, nullable | |

A hand-off is **decided** when one of these durable records names its delivery and playbook: a run
(FR-336), a guard refusal (FR-328), a waiting trigger accepted by the guard, or a delivery refusal for
one of its values (FR-322, FR-326). The state column is written after that record, for a reader; what
the restart reads is the records themselves, so a kill between the two leaves nothing to reconcile
wrongly.

**On restart** — before either listener opens, and before `gronin deliveries` reads — every
`accepted` delivery whose instance lock can be taken has each undecided hand-off marked `dropped`,
and the delivery `dropped` if any was, `handed_off` otherwise (FR-315). A delivery whose accepting
process still holds its instance lock is left alone: two processes on one state directory each hold
their own.

**What "handed off" covers.** Once the guard has accepted a hand-off into its waiting slot, the
hand-off is decided. If the process then dies, the guard marks its waiting trigger dropped, and a
retry of the delivery is a repeat: FR-315 is written for a delivery not yet handed to the guard, and
the guard's own drop record names the delivery.

### Delivery refusal (in the record store)

An authenticated request that did not become a delivery, or a delivery whose value one playbook
refused (FR-332). One row each.

| Field | Type | Notes |
| ----- | ---- | ----- |
| `id` | identifier | |
| `source` | slug | |
| `reason` | enum | `body_not_json`, `identity_absent`, `identity_not_single`, `value_absent`, `value_not_single`, `value_too_long`, `value_no_match`, `value_leading_dash` |
| `value_name` | text, nullable | The declared value concerned |
| `playbook_name` | text, nullable | Set for a value refusal |
| `delivery_id` | identifier, nullable | Set for a value refusal: the delivery was accepted, and this is one playbook's refusal of it |
| `received_at` | timestamp | Wall reading, UTC |
| `peer` | address | The connection's own |
| `body_ref` | blob ref, nullable | The body, for a request that did not become a delivery. A value refusal points at its delivery's body instead |

A repeat is not a refusal (FR-317).

### Unauthenticated refusal count (in the record store)

A request refused before its signature passed (FR-333). Never a row per request.

| Field | Type | Notes |
| ----- | ---- | ----- |
| `interval_start` | timestamp | The wall reading truncated to the counting interval — one minute (plan, *Constraints*) |
| `reason` | enum | `not_found`, `busy`, `body_too_large`, `body_timeout`, `signature_absent`, `signature_malformed`, `signature_repeated`, `signature_mismatch`, `unknown_source` |
| `source_bucket` | text | A configured source's name, or `(unconfigured)` for every name the deployment does not configure, and for a request that named none |
| `count` | integer | |
| `last_peer` | address | The connection's own |
| `last_at` | timestamp | |

Keyed on `(interval_start, reason, source_bucket)`, so a flood adds at most one row per reason and
bucket per minute. No body and no header value is stored. A sender whose headers stall or exceed
their bound is ended by the HTTP server before any handler runs, so it is not counted: there is
nothing to count it with.

### Run (runtime core, changed)

The Run entity of [specs/001-runtime-core/data-model.md](../001-runtime-core/data-model.md) gains one
trigger kind and one field. That document is updated when these tasks land.

| Field | Type | Notes |
| ----- | ---- | ----- |
| `trigger_kind` | enum | Gains `webhook` |
| `delivery_id` | identifier, nullable | The delivery the run came from (FR-336). Null for any other kind; a replay or resume of a webhook run records its parent as the core already does, not the delivery again |

### Guard records (the guard's, changed)

The guard's refusal record and waiting trigger, in [specs/002-guard/data-model.md](../002-guard/data-model.md):

| Record | Change |
| ------ | ------ |
| Refusal | `trigger_kind` gains `webhook`; a nullable `delivery_id`, set for every refusal of a webhook trigger (FR-328) |
| Waiting trigger | `trigger_kind` gains `webhook`; a nullable `delivery_id` |

A webhook trigger is a trigger that will not come again, so it waits like a manual one. When a
waiting webhook trigger comes to run and the guard re-reads its playbook, its values are extracted
again from the delivery's body — held in memory by the waiting trigger, not read back redacted —
and checked against the declaration as it now stands. A value the edited declaration refuses is a
delivery refusal, as it would have been at hand-off.

A webhook run counts toward the playbook's rate limit like a scheduled or manual one, on both the
backend and the single-host window: a limit that did not count the trigger coming from outside
would not bound the sender it exists for.
