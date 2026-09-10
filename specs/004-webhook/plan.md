# Implementation Plan: Webhook Trigger

**Branch**: `add_webhook_spec` | **Date**: 2026-09-10 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/004-webhook/spec.md`

## Summary

A playbook started by an HTTP delivery from outside the deployment. The owner's decision of
2026-09-10 fixes the shape: a second listener, the ingress, beside the operator API rather than
routes added to it (FR-301, FR-302), with the operator API's loopback default left exactly as the
runtime core ships it (FR-303). Everything else in the feature follows from one question — what may
something outside the deployment cause, and what may it read — and the answer is deliberately
narrow.

It may cause a run of the playbooks an operator bound to its source, and nothing else (FR-321). It
may supply the values those playbooks declared, each held to a pattern and a length (FR-322), and
nothing it sends beyond them reaches the run (FR-323). It reaches the agent as data in a file rather
than as text spliced into the prompt (FR-324), and it cannot name where the report goes (FR-325). It
may read nothing: the ingress serves one route, and the record is on another listener.

Three decisions the specification makes, and the reasoning behind each, are what a reader needs
before the design.

**The answer means "recorded", and it comes only after the record is durable (FR-312).** A receiver
that acknowledges on receipt and records afterwards loses exactly the events that arrive while
recording is failing, and loses them silently: the sender was told they succeeded and has no reason
to send them again. Answering after the write moves the failure to where the sender can see it — an
answer saying "not accepted" is a retry. What the answer cannot mean is "ran": the guard may make the
trigger wait, and no sender holds a connection open for that. Phase 0 is where the requirement
this depends on came from, by running it: an answer that outlives the server's write
limit vanishes while the handler believes it was sent (FR-313). One more follows from it by
reasoning rather than measurement: a handler that stops waiting at its bound has not rolled the write
back, which may already have committed, so the hand-off has to follow the record rather than the
answer (FR-314) — see [research.md](./research.md), question 3.

**The identity is the delivery, not the event (FR-316, FR-317).** The guard specification left
deduplication here because a cron tick has no identity. A delivery has one — its authenticated body,
or a declared value inside it for a sender that stamps each attempt differently (SC-308) — and what repeat detection is for is a sender's retry of it. It is
explicitly not for an event that recurs: an alerting system re-notifying an unchanged, still-firing
alert sends a body identical to the last, and suppressing it for longer than a retry takes is the
failure the constitution's wall-clock rule was written after. The replay window is therefore short,
per source, measured on the runtime's clock (FR-320); recurrence is the guard's rate limit's job.

**A payload's reach is declared, not inferred (FR-322 through FR-326).** A playbook names every value
it takes from a delivery, where it is, and what it must look like. The runtime then has something to
refuse against: a value that does not match, one that would become a command option, one referenced
from the prompt or a sink. The residual is stated rather than implied: a declared value with a
permissive pattern carries as much of the sender's text as the author allowed, to the agent, as data.
The agent can only report (Principle II), and the report goes only where the playbook says — so the
worst that text can do is shape a report the operator's own sinks deliver, within their caps.

The prompt refusal (FR-324) is not what contains that text; Principle II is. What it does is keep the
prompt the author's: a value spliced into it is indistinguishable, to the model and to anyone reading
the record afterwards, from an instruction the author wrote. Read from a file, the same value arrives
as the output of a tool call, which the record shows as such and which the prompt can tell the agent
to treat as data. That is a weaker claim than containment, and it is not offered as one.

## Technical Context

**Language/Version**: Go, as the runtime core. No change.

**Primary Dependencies**: none new. `net/http` serves the ingress, `crypto/hmac` and `crypto/sha256`
authenticate it, and the record store's existing SQLite driver holds deliveries. Phase 0 established
that the store, opened exactly as it is today, gives the durability and the single-statement "is this
new" decision the feature needs (research question 5).

**Storage**: the record store gains three things and changes one.

- **Deliveries**: source, identity, receipt time, peer address, body blob and digest, repeat count,
  and a state — accepted, handed off, dropped, bound to nothing. Keyed on source and identity, which
  is what makes FR-318's decision one statement.
- **Hand-offs**, one per delivery and bound playbook. Per playbook rather than per delivery, because
  a process killed partway through handing one delivery to three playbooks has handed it to some of
  them, and FR-315's retry must reach only the rest.
- **Delivery refusals**: individual rows for authenticated refusals (FR-332), and counters keyed on
  interval, reason and source for unauthenticated ones (FR-333), every unconfigured source name
  sharing one key so a stranger cannot mint rows by inventing names.
- **Runs** gain `webhook` as a trigger kind and a link to their delivery (FR-336). This changes the
  runtime core's data model, whose "deliberately absent" section names webhook deliveries as a thing
  that does not exist yet.

**Testing**: the existing suite under Principle VI, on the line research question 1 draws — see the
Constitution Check below.

**Target Platform**: unchanged.

**Project Type**: a second listener in the `serve` process, a trigger type the playbook schema
stops refusing, and the load-gate rules that go with it.

**Performance Goals**: none beyond the bounds. The ingress is sized for a sender that retries, not
for throughput.

**Constraints**: the bounds of FR-329 through FR-331, and FR-313's durable-step bound, have values
chosen here rather than derived. Each is a threshold decided on 2026-09-10, with its reason:

| Bound | Value | Reason |
| ----- | ----- | ------ |
| Body | 1 MiB | The largest prompt file the runtime already accepts. `in-progress × body` is the ingress's memory ceiling, so this and the next row are chosen together |
| Deliveries in progress | 32 | With a 1 MiB body, 32 MiB held at most; a sender that needs more in flight at once is outside what this ingress is sized for |
| Headers | 16 KiB | The standard library's default is 1 MiB (research question 2); the only header the ingress reads is a signature |
| Time to headers | 10 s | What `serve` already gives the operator API |
| Time to the whole request | 15 s | Counted from the start of the request, headers included; covers a full-size body from a slow sender, and one that has stopped is not waited on longer |
| Durable step | 5 s | The record store's own busy timeout, so a write lock held by another process is waited out once before the ingress gives up |
| Answer write | 25 s | The standard library starts this limit once the headers are read, so it has to cover the rest of the body and the durable step before the answer is written. 15 s plus 5 s leaves room; less, and FR-313 cannot hold — research question 3 |
| Replay window, default | 10 min | Long enough to cover a sender retrying a delivery whose answer it lost; short enough not to suppress an event deliberately re-sent. Per source, because senders differ in how they retry |

The durable step and the busy timeout are the same number deliberately, and a change to one is a
change to both.

**Scale/Scope**: a handful of sources per deployment, and deliveries measured per minute. The
single-host reach of repeat detection (FR-319) matches the guard's reach when no coordination backend
is configured.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

Checked against [constitution.md](../../.specify/memory/constitution.md), at 1.2.0.

### I. Bounds Are Declared and Enforced — PASS, and the ingress is a new bound to test

The webhook trigger joins the declared bounds and is refused at load on the same terms as the rest:
a source the deployment does not configure (FR-310), a declared value missing its pattern or its
length (FR-322), a payload reference in the prompt (FR-324) or in a sink (FR-325). SC-315 is the
hostile corpus for those, each case failing the suite when its check is removed.

The principle's sharper edge is the ingress itself, which is a surface a stranger can reach. Its
guard is the signature, and SC-312 probes it on the values it must refuse — absent, empty, short,
doubled, over another body, under another source's secret — not only on the one it accepts. An
unknown source and a wrong signature get the same answer, byte for byte (FR-309, SC-314), so the
ingress does not enumerate the deployment's sources for whoever asks. FR-304 refuses the two
listeners sharing a port, because two surfaces that could be one socket are one surface with extra
steps. FR-303 is the other half: no webhook setting widens the operator API. SC-310 tests both.

### II. The Agent Reports, the Runtime Acts — PASS, and the payload is held to it

Nothing here grants the agent a tool. What the principle needs from this feature is that a
delivery cannot reach around it: it cannot pick the playbook (FR-321, SC-320), cannot name a
destination, a credential or the label a cap is counted against (FR-325), and cannot make a gather
step's argument an option (FR-326, SC-319). The sink refuses a payload reference again when it is built, for
the reason the label rule is already duplicated there: the sink holds the rule even when it is
reached by something that did not come through the gate.

### III. Every Run Is Inspectable — PASS, extended to requests that did not become runs

A delivery and every refusal of one are recorded (FR-332, FR-333) and read through the operator's
surface (FR-335, SC-328); a run links to its delivery and replays from it (FR-336, SC-329). A
delivery lost to a kill is visible as dropped rather than absent (FR-315, SC-304).

The one place this principle is deliberately not applied in full is the unauthenticated refusal,
recorded as a count rather than a row (FR-333). A row per request would let anyone who can reach the
ingress fill the disk, which is a worse failure than a count an operator reads as "ten thousand bad
signatures from one address in a minute". The count still names the reason, the source and the last
peer, and SC-326 holds it to growing with time rather than with volume.

### IV. Playbooks Are Portable Data — PASS

A playbook names its source as it names an MCP server: a name the deployment must provide, refused
where it does not (FR-310). The secret, the replay window and the identity location live in the
deployment's configuration (FR-306). The locations of declared values are properties of the sender's
payload format, which is the same on every deployment that uses that sender, so a playbook written
against one format runs on any deployment that configures a source sending it.

### V. Nothing From a Real Fleet Enters This Repository — PASS

No new surface for it. Test fixtures sign with a literal test secret and send from loopback; examples
use `example.com`, `192.0.2.0/24` and `REPLACE_ME`.

### VI. The Suite Is the Gate — PASS, with the line drawn by running it

**Hermetic.** Research question 1 established that a loopback listener exists inside the namespace
`scripts/run-suite.sh` verifies it runs in, and nothing beyond it does. So the line is drawn by what
is under test. A property of a request — signature, route, identity, repeat, payload shape, what is
recorded — is tested against the handler with no socket at all. A property of a connection — a
stalled header or body (SC-324), the number in progress (SC-325), a body counted byte by byte
(SC-323) — binds loopback, because the thing under test is the server's handling of a real
connection. The binary-level test (SC-301) binds loopback because the executable is the subject. The
guard's Phase 0 is answering the same question for its coordination backend in parallel; if it draws
the line differently, the two are reconciled before either has tasks.

**Deterministic.** The replay window is judged against an injected clock (SC-305), never by sleeping
to it. Atomicity is shown with the interleaving forced — a second acceptance issued while the first
holds its transaction open, from a second process — rather than by racing and hoping (SC-306). The
connection bounds are real time, set to milliseconds in the tests, against senders built to stall:
the outcome does not depend on scheduling, only on the bound being enforced at all.

**A test must be able to fail.** SC-330 states it as a criterion. Every one of SC-301 through
SC-329 needs a mutant, and several are easy to satisfy while asserting nothing, which is worth naming:

- **Absences.** SC-309's not-found, SC-311's "nothing listens", SC-312's refusals and SC-318's
  missing sentinel are all satisfied by an ingress that never started. Each is paired with a positive
  in the same test — a delivery that is accepted, a sentinel that is found in the delivery record —
  so a dead listener fails it.
- **Bounds.** SC-302, SC-323, SC-324 and SC-325 are exercised only by a subject that exceeds them: a
  store that holds, a body that is too large, a sender that stalls, deliveries that fill the slots. A
  prompt subject passes each with no bound enforced.
- **Ordering.** SC-303 fails only against a hand-off tied to the answer, so its store must land the
  write after the answer said "not accepted"; a store that fails outright proves nothing about it.
  SC-304 kills rather than stops, for the reason the guard's own kill-not-stop criterion does: a
  record written on the way out passes a graceful stop.
- **Clocks.** SC-305 carries a timestamp in the body that would put the repeat outside the window,
  so an implementation reading it fails.
- **Constant time.** SC-313 cannot be a timing test and is not one: it asserts the verifying code
  goes through the standard library's constant-time comparison, and the mutant replaces it with an
  ordinary one.
- **Redaction.** SC-316 reuses the suite's existing secret sentinel, which `scripts/run-suite.sh`
  already scans the whole output for — so a source secret leaking into a log fails the suite the
  same way a configuration secret does.

### Operational Constraints

**Time** is FR-320. Nothing a sender writes enters a comparison; the window is judged from the
acceptance, on the host's clock.

**Secrets** is FR-306's last clause. Setting a configuration value today takes it as a command
argument, which the constitution forbids for a secret. A source secret is the first secret an
operator of this feature must set, so `gronin config set --secret` gains the form that reads the value
from standard input, and SC-316 drives it through the built executable. The argument form is left as
it is for values that are not secrets; that it accepts secrets too is the runtime core's to correct.

**Configuration names**: the one this feature introduces from outside is the signature header, and
which header real senders use is not established here — Phase 0, question 1.

## Project Structure

### Documentation (this feature)

```text
specs/004-webhook/
├── spec.md          # this feature's requirements
├── plan.md          # this file
├── research.md      # Phase 0 — the six questions answered by running or reading
└── tasks.md         # not yet written
```

### Source Code (repository root)

```text
runtime/internal/
├── ingress/         # new — the listener, its limits, signature, identity, acceptance, hand-off
├── sources/         # new — the deployment's source catalogue, beside the MCP catalogue
├── playbook/        # the webhook trigger in the schema and the validator (FR-310, FR-322, FR-324, FR-325)
├── config/          # binding validated values into gather steps; the leading-dash refusal (FR-326)
├── sink/            # refusing a payload reference when a sink is built (FR-325)
├── record/          # deliveries, hand-offs, refusals; runs gain their delivery link
└── api/             # read-only routes for deliveries and refusals on the operator API (FR-335)
```

`ingress` is a package of its own rather than routes in `api`, because the separation the owner
decided is the thing under test: one package serving both route sets is how a future change puts
them back behind one listener without anyone deciding to. The two packages share nothing but the
record store.

The source catalogue follows the MCP catalogue's pattern: a file beside the deployment
configuration, never in a playbook, whose secret entry is a reference to a configuration value rather
than a value. The runtime refuses a source whose secret reference names a configuration value that is
not marked secret — otherwise `gronin config list` would print it, and FR-306's redaction would hold
everywhere but there. A read-only listing sits beside `gronin mcp list` and, like it, never resolves a
reference (FR-311).

## Phase 1 — Design sketch

Not a contract yet; the shape the tasks will be written against.

**The delivery route.** `POST /hooks/{source}` on the ingress, and nothing else (FR-302). The body is
JSON. The signature is HMAC-SHA256 over the exact body bytes, hexadecimal, in one header.

**The order of a delivery through the ingress**, which is load-bearing:

1. Route and method, else not-found (FR-302).
2. A slot among the deliveries in progress, else refused at once (FR-331).
3. Declared length against the body bound, before reading (FR-329).
4. The body, read under the byte bound and the time bound (FR-329, FR-330).
5. Source and signature together, answered identically when either fails (FR-307, FR-308, FR-309).
   Nothing about the body is stored or parsed before this step passes.
6. Parse as JSON and resolve the identity (FR-316).
7. The atomic acceptance under the durable-step bound (FR-312, FR-313, FR-318, FR-317).
8. The answer: accepted, whether new or a repeat; or not accepted when step 7 did not complete.
9. The hand-off, driven by step 7's completion rather than step 8's (FR-314): for each bound playbook,
   validate its declared values (FR-322), refuse a leading dash for any value a gather step binds
   (FR-326), write the data file (FR-324), and hand a trigger to the guard (FR-328, SC-322).

Steps 1 through 4 are before authentication, which is why nothing they refuse is stored (FR-333).
Every refusal records the connection's own peer address, never a forwarded one (FR-334, SC-327).
Step 9 is after the answer, which is why a delivery whose every playbook refuses its values is still
answered as accepted: the sender cannot fix a declaration by retrying, and the refusal is in the
record for the operator who can (FR-332).

**The playbook shape.** The trigger's `type` gains `webhook`, with the source's name and the declared
values:

```yaml
trigger:
  type: webhook
  source: alerts
  values:
    alertname:
      at: /alerts/0/labels/alertname
      pattern: '[A-Za-z0-9_.:-]+'
      max_length: 128
```

`at` is a JSON Pointer into the body. The pattern is matched against the whole value — the runtime
anchors it, so an author who forgets `^` and `$` does not get a partial match (SC-317). A value is a
JSON string, number or boolean, rendered as text; an object or an array is not a single value. The
published schema changes with this, and GitHub Pages publishes it.

**The data file.** The declared values, as one JSON object, written as `trigger.json` into the run's
working directory and recorded as a gathered input named that. An agent reads it with whatever file
tool its playbook allows; a playbook that allows none has declared values its agent cannot see, which
is its author's choice rather than the runtime's to refuse.

**Manual invocation.** `gronin run <playbook> --trigger name=value` already exists. For a webhook
playbook the values pass through the same validation step 9 applies, before anything runs (FR-327,
SC-321), so the manual path is also the cheapest way to exercise a declaration.

**Startup.** `serve` gains `--ingress-address`, with no default: absent, nothing listens (FR-305).
Its startup output names the ingress address and says that repeat detection reaches this host only
(FR-319, SC-307). The operator API's `--api-address` and its credential rule are unchanged (FR-303).

**Restart.** Before either listener opens, deliveries still accepted and not handed off are marked
dropped (FR-315) — the same moment `serve` already marks runs interrupted, for the same reason.

## Phase 0 — Resolved and open

Six questions resolved, in [research.md](./research.md): a loopback listener is hermetic here; what
`net/http` does with each misbehaving sender; that an answer can vanish past the write limit; that
`hmac.Equal` is constant time over equal lengths; that the record store, opened as it is, survives a
kill after commit and can decide "new" in one statement; and that replay and resume pass the sinks no
trigger values.

Three remain open, none of which blocks the specification:

1. **Which senders can produce the signature, and in which header?** Not investigated: the answer is
   about third-party software, and this repository's standard is that it is read off that software
   rather than recalled. The design admits a per-source header name if the answer needs it; it does
   not admit a second signing scheme, which would be a change to the owner's decision rather than to
   the plan.
2. **Does repeat detection widen with the guard's coordination backend?** A deployment running two
   hosts behind one ingress address detects a retry only on the host that took the first delivery
   (FR-319). The guard's backend is what would widen it, and it is not chosen yet. Until it is, the
   narrower reach is stated, the way the guard states its own.
3. **Power loss.** Research question 5 measured a killed process, not a lost machine. The store runs
   with FULL synchronous, which is the setting meant to cover the second; nothing here has shown it
   does on the filesystems a deployment will use.

## Complexity Tracking

| Choice | Why | Simpler alternative rejected because |
| ------ | --- | ------------------------------------ |
| Two listeners (FR-301) | The owner's decision. The operator API serves every prompt, tool call and gathered input a run saw | One listener with the webhook route added is less code, and it makes every deployment that accepts deliveries one that exposes its record to anything able to reach that address — a route table inside one listener is a policy, and a listener is a boundary |
| Answer after the durable write (FR-312) | A sender only retries what it was told failed | Answering on receipt is faster and simpler, and it loses the events that arrive while recording is failing, silently, because the sender was told they succeeded |
| Hand-off from the record, not the answer (FR-314) | A wait abandoned at its bound is not a write rolled back (research question 3) | Handing off when answering `202` is the obvious place, and it leaves a delivery recorded, deduplicated on retry, and never run |
| A short replay window (FR-317) | Repeat detection exists for retries | A long window looks safer against replay and suppresses an unchanged event deliberately re-sent — the failure the constitution's time constraint describes |
| Declared values only (FR-322, FR-323) | The runtime can refuse only against something declared | Handing the agent the whole body is one less concept for a playbook author, and makes every byte a sender writes part of every run |
| Counts, not rows, for unauthenticated refusals (FR-333) | Anyone who can reach the ingress can send them | A row per request is uniform with every other refusal, and lets a stranger fill the disk |
| Implemented after the guard | The guard's waiting slot and rate limit are what make an outside trigger survivable | Shipping first under the runtime core's discard-on-collision would lose a delivery that arrives while its playbook runs, which is the failure the durable answer exists to prevent |
