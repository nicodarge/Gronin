<!--
Sync Impact Report
Version change: 1.2.0 → 1.3.0
Bump rationale: the Time constraint is materially expanded, not reworded — where it named one
reading of the host's clock it now distinguishes two, and says which one each use of time takes —
so MINOR per the versioning rule below. No principle is added, removed or redefined.
Modified principles: none.
Modified sections:
  - Operational Constraints, Time — "wall-clock time on the host" becomes the runtime's own clock:
    monotonic for durations and deadlines, wall clock for recorded timestamps, never a timestamp a
    trigger carried. Decided by the owner on 2026-09-11, on a finding of the guard feature's Phase 0
    (specs/002-guard/research.md): read literally, "wall clock" also covered the guard's stop
    deadline, which a backward step of the wall clock lengthens. The constraint also says that an
    instant the runtime computes from a schedule is its own and not a trigger's, since the guard
    compares one (specs/002-guard/spec.md, FR-129).
Added sections: none
Removed sections: none
Documents brought in line with the new wording in the same change:
  - README.md, docs/architecture.md
  - specs/001-runtime-core/plan.md
  - specs/002-guard/spec.md (FR-118), plan.md, research.md, data-model.md
  - specs/004-webhook/spec.md (FR-320 and an edge case), plan.md
Templates requiring review:
  - .specify/templates/plan-template.md — no change required by this amendment. Its Constitution
    Check gate is still the unfilled placeholder the 1.1.0 report flagged; that item stays open
  - .specify/templates/spec-template.md — no change required
  - .specify/templates/tasks-template.md — no change required
Follow-up TODOs: none
-->

# Gronin Constitution

## Core Principles

### I. Bounds Are Declared and Enforced (NON-NEGOTIABLE)

Every agent run declares the tools it may use, the MCP servers it may reach, and the paths it
may read. The runtime MUST validate that declaration when a playbook loads and MUST refuse the
playbook outright when the declaration is unsafe — before any trigger is armed, never at the
moment the tool is called.

Three mechanisms bound an agent and they are not interchangeable. The tool set replaces the
built-in tools, so a name that is absent is absent rather than merely unpermitted. A strict MCP
configuration loads only the declared servers. The allowlist path-scopes file tools and names
individual tools inside a loaded server. The allowlist alone is a grant, not a bound: it adds to
what the surrounding policy already permits, and an entry naming a command prefix constrains the
start of a command line, not the whole of it — chaining, substitution and redirection all ride
through an approved prefix.

Any code asserting a bound MUST be tested against the values it is meant to REFUSE, not only
against the values it is meant to accept. A guard probed only on its accepted values is
untested. A comment describing containment is not containment.

Rationale: this runtime hands a language model a tool set on a machine its author does not own.
It is the only failure here that harms someone other than the person who made it.

### II. The Agent Reports, the Runtime Acts

An agent stage produces a structured report against a declared schema. It MUST NOT be granted
tools that create, modify or delete anything outside the run's working directory. Every side
effect — an issue, a message, a document, a metric — is performed by a sink, after the run has
returned.

Every sink that creates things MUST declare a cap, and the runtime MUST refuse a playbook whose
creating sink omits one.

Rationale: a run that goes wrong then produces a bad report rather than a bad action, and a bad
report is recoverable. An uncapped creator gets muted within a month, and the useful signal is
lost along with the noise.

### III. Every Run Is Inspectable

The runtime MUST record, for every run: the resolved playbook, the gathered inputs, the
retrieved context, every tool call with its input and output, the timings, the token cost, and
the outcome of every sink. From that record, and without firing a trigger, a run MUST be
replayable — re-running the agent stage against the recorded inputs — and resumable —
re-running only its sinks against the report already produced.

Inspection ships with the stage it inspects. A stage merged without its execution record is
incomplete.

Rationale: the tools this replaces show an operator where a run stopped and with what data. A
system that does not match that is a downgrade however much cleaner its code is, and the people
who would adopt it debug by looking.

### IV. Playbooks Are Portable Data

A playbook is declarative data — a YAML document and a prompt file — never code. It MUST NOT
contain a hostname, an address, a credential, or any identifier belonging to one particular
deployment; those arrive through the deployment's own configuration and through the trigger
payload.

Interpolation MUST resolve only against the trigger payload and the deployment configuration,
never against the process environment. A playbook shipped in this repository MUST run against
any deployment that provides the capabilities it declares.

Rationale: the playbook is the unit of sharing. It has to diff, review, test, and be contributed
by someone who does not run the fleet it was written against. Exposing the whole process
environment to a template hands the deployment's own database password to every playbook.

### V. Nothing From a Real Fleet Enters This Repository

No hostname, address, credential, ciphertext, or identifier from any real deployment is
committed here — not in code, not in a comment, not in an example, not in a test fixture, not in
a commit message. Examples MUST use documentation-reserved values.

Automated secret scanning MUST run before every commit. It is a belt and not the rule: it
catches shapes it already knows, and an infrastructure hostname is not one of them.

Rationale: this repository already serves a public surface — GitHub Pages has published the
playbook schema since 2026-09-07 — and its own switch to fully public can come at any time, with
no date attached. The rule holds now, not from a future deadline: removing something from history
later is expensive and unreliable.

### VI. The Suite Is the Gate

Principle I ends in an obligation on a test, and Principle III is checked by one. This principle
is what makes either worth anything.

The suite MUST be hermetic. It MUST pass with no network reachable, with no credential
configured, and without spending a token: an agent stage is exercised against a stub that emits
the same event stream the real one does. A test that reaches the network is not slow, it is
conditional — it passes on the machine that has the access and fails on the machine that reviews
the change.

The suite MUST be deterministic. It runs with the race detector on and with test caching
disabled, and repeated runs of an unchanged tree MUST agree. A flake is a failure: quarantining
one teaches the next reader that red is negotiable, which is the whole of what the gate was for.

A test MUST be able to fail. Before relying on one, mutate the line it covers and confirm the
exit code flips. A test that never executes its own body passes forever, and a green run is not
evidence that it ran. A mutation harness MUST be able to report zero survivors — one that cannot
is decoration, and it will report a comfortable number for as long as nobody checks.

The gate MUST run on every merge, and it MUST run on the artifact that ships rather than on the
packages it was built from: at least one test drives the built executable through the operator's
own surface. A binary is where the linkage, the embedded schema and the argument vector are
real, and each of those is lost silently by a change that every package test still passes.

Rationale: this repository's guards are the product. A guard whose test cannot fail, cannot run
without a credential, or never touches the shipped binary is a comment describing containment,
which Principle I already refuses.

## Operational Constraints

**Time.** Every time the runtime records or compares MUST be read from the runtime's own clock —
monotonic for durations and deadlines, wall clock for recorded timestamps — and never from a
timestamp a trigger carried. The monotonic reading is the one time synchronisation cannot step
backwards, so a deadline measured on it cannot be lengthened; it does not survive the process, so a
time another process or a later start has to compare is a recorded timestamp, on the wall clock. A
timestamp carried in a trigger payload is neither: an alerting system freezes an alert's start time
at first activation and re-sends it unchanged on every re-notification, so ageing a re-fire against
it makes every one look seconds old and suppresses the investigation permanently and silently. An
instant the runtime computes itself, such as the occurrence a schedule names, is the runtime's own
and not a trigger's.

**Secrets.** A secret MUST NOT appear on a command line. Commands are journalled and shipped to
log aggregation, where the value then sits for the whole retention window. Read a secret into a
variable inside the command instead, so the command line carries the variable name.

**Configuration names.** A configuration key, environment variable or flag MUST be verified
against the shipped artifact before being used. A plausible name that does not exist fails
silently, and a silent no-op beside a real setting is worse than an obvious error — it makes a
change look narrower than it is.

**Authentication.** The runtime MUST support more than one credential source from its first
release. A credential form that only its author can obtain makes the product unstartable by
anyone else.

## Development Workflow

Work happens on a feature branch; the default branch refuses direct commits. Pull requests are
squash-merged once every required check is green — a standing approval the repository owner gave
on 2026-09-08, for this repository. `gate` is the required check and it needs all the others, so
`gh pr checks` reporting nothing is not green: it cannot distinguish runs held in
`action_required` from no run at all. It is approval to merge, not to skip the review that
precedes it.

`pre-commit run` MUST pass on every changed file before a push. Run the hook set, never the
underlying tool: a hook can load plugins the bare command does not.

Documentation states facts. It MUST NOT record a version number or a count that restates
something the repository already holds: both go stale, nothing fails when they do, and the next
reader trusts them. A measurement anchored to a named date or a named incident is not a count
and may stay.

## Governance

This constitution supersedes other practices in this repository. Where a document, a template or
a review comment conflicts with it, this file wins and the other document is corrected.

Amendments are made by pull request, which MUST state the version bump and its rationale.
Versioning is semantic: MAJOR for a removed or redefined principle, MINOR for a new principle or
materially expanded guidance, PATCH for clarification and wording.

Every pull request review MUST verify compliance with Principles I, II, V and VI, which are the
four whose violation is invisible in a passing test suite — VI most of all, since its subject is
the suite that would have to do the telling. Complexity that a principle
discourages is allowed only when the pull request states what was tried instead and why it did
not work.

**Version**: 1.3.0 | **Ratified**: 2026-09-04 | **Last Amended**: 2026-09-11
