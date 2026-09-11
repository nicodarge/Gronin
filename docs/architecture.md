# Architecture

## The problem this replaces

Unattended agent systems converge on the same six stages, and they get written
twice: once as a visual workflow (webhook node, a dozen Redis nodes, an SSH
node shelling out to an LLM CLI, parsing nodes, notification nodes) and once as
shell scripts behind a CI runner (a `flock`, a gather script, a prompt, a
structured-output flag, a call to the issue tracker).

Neither is reviewable. A workflow graph does not diff. A shell script that
assembles a prompt from three files cannot be tested without a runner, an
account and a fleet. Both encode real operational knowledge — an iteration cap,
a per-alert throttle, a deferral queue — in a place where nobody can read it.

Gronin writes those six stages once, as a program, and makes the workflow graph
and the shell script both unnecessary.

## The pipeline

```text
  trigger ──▶ guard ──▶ gather ──▶ retrieve ──▶ agent ──▶ sink
     │          │          │           │          │         │
  webhook    lock       shell      embed +     Claude    chat
  cron       ratelimit  http       vector      Agent     kb
  manual     dedup      files      search      SDK       issues
                                                          metrics
```

Every stage is a plugin point. That is the whole reason someone with no Puppet,
no NetBox and no Prometheus can still install this and get value from it: they
bring their own `gather` commands and their own sinks.

### `trigger`

Webhook (an alerting system, a chat platform, a forge), cron, or a manual
invocation. One process owns all three, so a playbook does not care which one
woke it.

### `guard`

Runs before any tokens are spent. Holds a lock so two runs of the same playbook
do not overlap, applies a rate limit, and deduplicates.

Deduplication anchors on the runtime's own clock. An alerting system freezes an alert's
start time at first activation and re-sends it unchanged on every
re-notification, so a continuously-firing alert reports a start time that is not
"now". Ageing a re-fire against it makes every one of them look seconds old,
which suppresses the investigation permanently and silently — a symptom that
mutated into a different failure keeps being treated as the original one.

### `gather`

Shell commands, HTTP calls and file reads, written into the run's working directory, which is
what the agent is pointed at. This is the stage that carries a deployment's specifics,
and the reason a playbook can be generic while its inputs are not.

### `retrieve`

Embeds a query and searches a vector store for what was learned last time.
Optional; a playbook that omits it simply runs without prior context.

### `agent`

One agent run, driven as a child process over the Claude Code command-line contract — the same
contract a language-specific agent SDK wraps. Not an SSH call to a machine that happens to have
the CLI installed: the runtime owns the process, which is what makes streaming, timeouts,
cancellation, per-run cost and a complete transcript possible at all.

The tool set is declared and enforced. Three separate mechanisms are needed to
bound an agent, and only one of them is obvious:

| Mechanism | What it actually does |
| --------- | -------------------- |
| Restricted execution | Removes the command- and code-running built-in tools from the process outright. The default here, and the only one a playbook cannot widen by accident |
| The tool set | Replaces the built-in tools, so a name that is absent is absent rather than merely unpermitted |
| Strict MCP configuration | Loads only the declared servers; an empty configuration removes them outright |
| The allowlist | Path-scopes file tools and names individual tools inside a loaded server |

The allowlist alone is a grant, not a bound: it adds to what the surrounding
policy already permits, and an entry naming a command prefix constrains the
start of a command line, not the whole of it — chaining, substitution and
redirection all ride through an approved prefix. A playbook is validated against
this at load time.

### `sink`

The agent reports; the sink acts. Chat message, knowledge-base document, issue,
metric. Capped: an unbounded issue machine gets muted within a month, and the
useful part is lost along with the noise.

## Observability

Every run is recorded: events, tool calls, timings, cost. A visual workflow tool
shows you where a run stopped and with what data; a service that does not
replace that capability is a downgrade, however much cleaner its code is. This
is not a phase-3 nicety, it ships with the runtime.

Two things can be done with a record, and they are different operations with different costs.
**Replay** re-runs the agent stage against the recorded inputs — what you reach for after
changing a prompt. **Resume** re-runs only the sinks against the report already produced — what
you reach for when a sink failed and the reasoning was fine. Neither fires a trigger.
