# Gronin

A runtime that turns infrastructure signals into bounded agent runs.

> **Status: the runtime core runs.** A playbook on a schedule gathers its inputs,
> drives one bounded agent, and delivers a report; unsafe playbooks are refused
> before anything is armed; every run is recorded, replayable and resumable. Not
> yet: the issue sink, semantic retrieval, the guard stage and webhook triggers —
> see [docs/roadmap.md](docs/roadmap.md).

## Running it in a container

The published image carries the runtime and nothing else — not the agent, which is a
separate executable with its own release cadence and its own credentials. Mount it, or
build on top of the image, and name it with `--agent`.

It is built for `linux/amd64`. The release also publishes `linux/arm64` and `darwin/arm64`
binaries; on those, run the binary rather than the image.

The state directory is `/data`, owned by the unprivileged user the image runs as. A named
volume inherits that ownership; a bind mount does not — it takes the host directory's,
which is usually root, and there is no shell in the image to fix it at runtime. Use a
named volume, or `chown 65532:65532` the host directory first.

## What it is

An alert fires, a schedule elapses, a pull request opens. Gronin picks the
signal up, gathers the context that makes it intelligible, retrieves what was
learned the last time something like it happened, hands all of it to an agent
whose reach is declared and enforced, and routes the result somewhere a human
will actually see it.

Six stages, declared in one file:

```text
trigger → guard → gather → retrieve → agent → sink
```

| Stage | What it does |
| ----- | ------------ |
| `trigger` | Webhook, cron or manual invocation |
| `guard` | Lock, rate limit, deduplication — before anything is spent |
| `gather` | Commands and HTTP calls that build the input the agent reads |
| `retrieve` | Semantic search over past incidents and documentation |
| `agent` | One agent run, on a declared model, with a declared and enforced tool set |
| `sink` | Chat, knowledge base, issue tracker, metrics |

## Why it exists

Two things already do this, badly, in most homelabs and small platform teams:
a visual workflow tool wiring an LLM call between HTTP nodes, and a pile of
shell scripts behind a CI runner. Both re-implement the same six stages, and
neither can be reviewed, tested or shared.

The stages are not the interesting part. What is interesting is that each one
is a place where unattended agents usually go wrong, and where this runtime
takes a position:

- **`guard` runs before the agent, not after.** Deduplication is anchored on
  the runtime's own clock, never on a timestamp carried in the payload — an alerting
  system re-sends a firing alert with its original start time, so ageing a
  re-fire against it makes every one of them look seconds old and silently
  suppresses the investigation.
- **`agent.tools` is a bound, not a wish.** It is validated when the playbook
  loads, and a playbook that asks for an unrestricted shell is refused at
  startup rather than discovered in production. Handing bash to a model on
  someone else's machine is the one failure here that harms more than its
  author.
- **`sink` is what acts.** The agent reasons and reports; the runtime is what
  opens the issue or posts the message. A run that goes wrong produces a bad
  report, not a bad action.

## Quickstart

Not yet. This section fills in when the runtime lands.

## Licence

MIT — see [LICENSE](LICENSE).
