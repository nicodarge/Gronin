# Roadmap

Phases are ordered by dependency, and the riskiest is deliberately last.

## Phase 0 — Foundation

This repository, its licence, its hygiene rules, and a secret-scanning hook that
runs before every commit. The repository already serves a public surface — GitHub
Pages has published the playbook schema since 2026-09-07 — and its own switch to
fully public can come at any time, so the rule that nothing from a real fleet
enters it is enforced from the first commit rather than from a date.

## Phase 1 — The deployment's MCP server catalogue

The infrastructure MCP servers will probably never be public: publishing them,
with container images and an installation guide, is dropped. What replaces it
stays inside Gronin. A playbook already names the servers it needs through
`agent.mcp`, and `agent.allow` already names individual tools rather than a
whole server; the runtime already refuses an entry that names a whole server,
and every agent run is already bounded to exactly its declared servers through
`--strict-mcp-config`. What is still missing is the deployment's own half of
that contract: a catalogue that turns a name like `postgresql` into a working
server definition, so a playbook can activate a server à la carte — a server no
playbook names is never configured and never launched.

## Phase 2 — The runtime core

One executable that is both the scheduler and the operator's client. Playbook loading and
validation, the cron trigger, the agent stage driven over the Claude Code command-line contract,
and three sinks — two messaging, one creating. Execution records, replay and resume ship in this
phase, not after it. Proven by porting the simplest existing agent command end to end.

## Phase 3 — Guard, retrieve, webhook

The lock, rate limit and deduplication layer, the semantic-retrieval stage, and
the webhook trigger. Proven by porting the small scheduled workflows — the ones
whose logic is a schedule, a command and a notification.

## Phase 4 — The agent commands become playbooks

The existing shell agent commands become playbooks, and the CI workflows that
drive them collapse into a single call to the runtime. Generic playbooks ship
here; deployment-specific ones stay where they are, as examples.

## Phase 5 — Alert intake, then retirement

The alert-intake path last. It carries the most accumulated operational
knowledge — deferral, per-alert throttling, iteration caps, re-injection — and
each piece of it answers an incident that actually happened. Porting it from a
diagram is how that knowledge gets thrown away. It goes last, after the runtime
has carried less critical traffic for several weeks. The visual workflow tool is
retired once it is done.
