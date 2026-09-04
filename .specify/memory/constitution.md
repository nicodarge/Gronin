<!--
Sync Impact Report
Version change: none → 1.0.0
Bump rationale: initial ratification; every principle and section is new.
Modified principles: none (no prior version)
Added sections:
  - Core Principles I through V
  - Operational Constraints
  - Development Workflow
  - Governance
Removed sections: none
Templates requiring review:
  - .specify/templates/plan-template.md — Constitution Check gate reads Principle I and II
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
tools that create, modify or delete anything outside its own scratch directory. Every side
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
the outcome of every sink. A run MUST be replayable from that record without re-triggering it.

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

Rationale: this repository is private and becomes public at a known phase. That is a deadline,
not a grace period — removing something from history later is expensive and unreliable.

## Operational Constraints

**Time.** Elapsed time MUST be computed from wall-clock time on the host. It MUST NOT be
computed from a timestamp carried in a trigger payload: an alerting system freezes an alert's
start time at first activation and re-sends it unchanged on every re-notification, so ageing a
re-fire against it makes every one look seconds old and suppresses the investigation
permanently and silently.

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
squash-merged, and only with the explicit approval of the repository owner.

`pre-commit run` MUST pass on every changed file before a push. Run the hook set, never the
underlying tool: a hook can load plugins the bare command does not.

A test MUST be able to fail. Before relying on one, mutate the line it covers and confirm the
exit code flips. A test that never executes its own body passes forever, and a green run is not
evidence that it ran.

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

Every pull request review MUST verify compliance with Principles I, II and V, which are the
three whose violation is invisible in a passing test suite. Complexity that a principle
discourages is allowed only when the pull request states what was tried instead and why it did
not work.

**Version**: 1.0.0 | **Ratified**: 2026-09-04 | **Last Amended**: 2026-09-04
