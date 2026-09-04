# Roadmap

Phases are ordered by dependency, and the riskiest is deliberately last.

## Phase 0 — Foundation

This repository, its licence, its hygiene rules, and a secret-scanning hook that
runs before every commit. The repository is private and becomes public in phase
1, so the rule that nothing from a real fleet enters it is enforced from the
first commit rather than cleaned up before the switch.

## Phase 1 — MCP servers in public

The infrastructure MCP servers move out of their private home and into the open,
with published container images and an installation guide. They stand alone:
they are useful to anyone running Claude Code against infrastructure, with or
without the rest of this project. This is also the cheapest possible test of
whether the subject interests anyone.

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
