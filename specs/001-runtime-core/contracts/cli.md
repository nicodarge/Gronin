# Contract — Operator command surface

One executable. `gronin serve` is the daemon; every other command is a client of the API that
daemon serves. Nothing reads the record store directly, so there is exactly one implementation of
every rule and no second path that can drift from it.

## Commands

| Command | What it does | Exit code |
| ------- | ------------ | --------- |
| `gronin serve` | Loads and validates every playbook, verifies the credential and the agent version, arms the schedules, serves the local API | Non-zero and no listener if any playbook is refused |
| `gronin validate [dir]` | Runs the load gate and exits. Never arms anything, never needs a credential | Non-zero on the first refusal, listing every one |
| `gronin run <playbook>` | Invokes a playbook immediately | Non-zero if the run did not succeed |
| `gronin runs` | Lists runs, most recent first, with status, trigger kind, duration and cost | |
| `gronin show <run>` | Prints one run's record | Non-zero if unknown |
| `gronin replay <run>` | Re-runs the agent stage against the recorded inputs | Non-zero if the replay did not succeed |
| `gronin resume <run>` | Re-runs only the sinks against the recorded report | Non-zero if a sink failed |
| `gronin config set <key>` | Sets a deployment configuration value playbooks interpolate against, read from standard input | Non-zero on an invalid key, or on a value given as a second argument |
| `gronin config list` | Lists configuration keys and values, secrets redacted | |
| `gronin mcp list` | Lists the MCP servers this deployment provides, credentials never resolved | |
| `gronin version` | Prints its own version and the agent version it found | Non-zero if the agent is below the floor |

`gronin config` exists so a playbook can name a destination without containing one. A playbook
holds `${config.discord_webhook}`; the value lives here, on the deployment. That separation is what
makes a playbook committable and shareable, so the command that maintains it is part of the
contract rather than a convenience.

`config set` never takes the value as an argument: a command line is journalled and shipped to log
aggregation, where the value then sits for the whole retention window. It reads the value from
standard input instead — `printf '%s' "$VALUE" | gronin config set discord_webhook` or
`gronin config set discord_webhook < file` — prompting without echo when standard input is a
terminal. A value given as a second argument is refused.

`gronin mcp` is read-only. The catalogue an operator maintains — which server a playbook's
`agent.mcp` may name, and how to reach it — lives in a file beside the deployment configuration,
never in a playbook and never in this repository; `list` is here so it can be inspected without
opening it by hand. Only an entry's env and header values are references resolved at run time and
hidden by `list`; its command, arguments and url are printed as written, so a credential must never
be one of them.

`gronin validate` exists because the load gate is the thing most worth running in CI, and requiring
a credential to check a playbook's shape would put it out of reach there. It is the same code path
`serve` runs, not a second implementation — a validator that can disagree with the runtime is worse
than none.

## Refusal output

A refusal names the playbook, the field, what was found and what would be accepted. It lists every
refusal rather than stopping at the first, because fixing them one round-trip at a time is how a
gate gets switched off.

```text
$ gronin validate ./playbooks
refused: drift-check.yaml
  agent.tools[2]: "Bash" is an unrestricted shell
    accepted: Read, Grep, Glob, WebFetch, or an MCP tool named in full
  sinks[0].cap: missing
    accepted: an integer; a sink that creates things must declare its ceiling

refused: doc-check.yaml
  agent.allow[0]: "mcp__grafana" names a whole server
    accepted: an individual tool, e.g. mcp__grafana__query_prometheus

2 playbooks refused, 4 accepted. Nothing was armed.
```

The last line is deliberate. A gate that refuses two playbooks out of six and starts anyway is the
failure this design exists to prevent, so the output says plainly that nothing started.

## Local API

Served by `serve` on a loopback address by default. The commands above map to it one for one. It is
the surface a web dashboard will consume in a later feature; building it now is what lets that
dashboard be added without reopening the runtime.

Its authentication is out of scope while it binds to loopback. Binding it anywhere else is a
configuration change that MUST require a credential to be configured first — an unauthenticated
listener that can invoke playbooks is a remote execution surface, and the default must not be one
step away.
