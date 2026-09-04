# Phase 0 — Research

Three questions, all answerable by reading the shipped artifact rather than by argument. Each was
resolved against Claude Code 2.1.261 on 2026-09-04, by running it, not by reading documentation
about it.

## 1. The containment flags exist, and they are the ones the constitution names

**Question**: which execution flags are real on the shipped CLI, and which are a bound rather than
a grant?

**Method**: enumerated against `claude --help`.

**Answer**: all of them are present — `--tools`, `--mcp-config`, `--strict-mcp-config`,
`--setting-sources`, `--allowedTools`, `--disallowedTools`, `--permission-mode`, `--json-schema`,
and `--restricted`.

`--restricted` is described by the CLI itself as removing the built-in tools that run commands or
code. It is coarser than the tool set and coarser than the allowlist, and it is the only one of the
four that a playbook cannot narrow its way out of by mistake. That is why FR-013 makes it the
default rather than an option.

**Consequence for the design**: the agent stage builds its argument vector from four independent
fields, applied in decreasing coarseness. None of them is optional in the sense of being omitted —
a playbook that declares no MCP servers still gets `--strict-mcp-config` with an empty
configuration, because omitting the flag means inheriting the machine's, which is the failure the
constitution describes.

**Version floor**: the runtime requires a CLI at or above the version these flags were verified on,
and checks it at startup. A missing flag on an older CLI does not error — it is ignored — which
would leave a run unbounded while looking bounded.

## 2. The event stream carries everything the record needs, and one thing better than that

**Question**: what does `--output-format stream-json` actually emit, and what are the field names?

**Method**: ran one minimal prompt with `--restricted --tools ''` and collected the distinct event
shapes.

**Answer**: eight event types. The two that matter most:

`system` / `init`, emitted first, carries `session_id`, `model`, `permissionMode`, `apiKeySource`,
`mcp_servers`, `claude_code_version` and — decisively — **`tools`: the list the child process
actually ended up with**.

`result` / `success`, emitted last, carries `total_cost_usd`, `usage`, `modelUsage`, `num_turns`,
`duration_ms`, `duration_api_ms`, `is_error`, `stop_reason`, `terminal_reason`, `result`, and
**`permission_denials`**.

Between them: `assistant` events carrying the full message, `stream_event/*` mirroring the
underlying API stream block by block, and `rate_limit_event`.

**Consequence for the design — this is the important one.** The runtime does not have to trust the
flags it passed. `system/init` is a **receipt**: the child reports the tool set and MCP servers it
was actually left with, and the runtime can assert that receipt against what the playbook declared,
aborting the run before the first token if they differ. Principle I demands that a bound be tested
rather than described; this makes the bound verifiable at runtime as well as at load, which no
amount of care in constructing the argument vector could do on its own.

`permission_denials` is the second gift: every time an agent reached for something the bounds
refused is recorded, per run. That is the signal that a playbook's declared tool set is wrong —
either too narrow for the job, or evidence the prompt is steering somewhere it should not. It goes
in the record and it is worth surfacing.

**Two things this research did not establish**: the shape of the stream on a failing run (only a
successful one was observed), and whether the event set is stable across CLI versions. Both are
handled the same way — the decoder ignores unknown event types and unknown fields rather than
failing, and the version floor above is what protects the fields it does depend on.

## 3. Credential sources

**Question**: which credential sources does the CLI consult, so FR-030 can name them without
inventing one?

**Method**: read off `claude --help`, plus the `apiKeySource` field observed in `system/init`.

**Answer, partial and marked as such**: the CLI documents that under `--bare`, "Anthropic auth is
strictly `ANTHROPIC_API_KEY` or `apiKeyHelper` via `--settings` (OAuth and keychain are never
read)", and that third-party providers (Bedrock, Vertex, Foundry) use their own credentials. So the
sources are at least: `ANTHROPIC_API_KEY`, an `apiKeyHelper` command in settings, an OAuth session,
a keychain entry, and per-provider credentials for the three cloud backends.

**What is not established**: the resolution order between them, and the set of values `apiKeySource`
can take. Neither is guessed.

**Consequence for the design**: the runtime does not reimplement resolution. It supports its own
explicit configuration, passes what it has to the child through the environment — never through
`argv`, per the constitution — and otherwise lets the CLI resolve. FR-030's "report which source it
used" is then satisfied by reading `apiKeySource` off the first `system/init` of the startup
verification run rather than by the runtime asserting anything it has not observed.

**Open, carried into implementation**: enumerate the `apiKeySource` values by running the
verification under each configured source. Cheap, and it belongs where the credentials are, not
here.

## What changed in the plan because of this

- The agent stage gains a receipt check between `system/init` and the first token, and a run aborts
  when the received tool set does not match the declared one. This is a new obligation on the
  implementation that the specification did not anticipate.
- `permission_denials` becomes part of the run record.
- The startup credential verification reports the source rather than asserting it.
- The runtime enforces a CLI version floor, because a bound expressed as a flag fails open on a CLI
  that does not know the flag.
