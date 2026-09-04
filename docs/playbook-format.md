# Playbook format

A playbook is a YAML file plus a prompt file. It is the unit of sharing: it
diffs, it reviews, it tests, and someone can contribute one without
understanding the runtime.

## Shape

```yaml playbook
name: doc-check
description: Report documentation that no longer describes the code.

trigger:
  type: cron
  schedule: "0 6 * * *"

gather:
  - run: gh issue list --repo ${config.repo} --state open --json number,title
    as: open-issues.json
  - run: git -C ${config.checkout} log --oneline -n 200
    as: recent-commits.txt

agent:
  model: sonnet
  prompt_file: doc-check.prompt
  restricted: true               # default; removes command- and code-running built-ins
  tools: [Read, Grep, Glob]      # the bound, validated at load
  mcp: []                        # declared servers only
  allow:
    - "Read(./**)"
    - "Grep(./**)"
  output_schema: issues
  timeout: 30m

sinks:
  - github_issues:
      repo: "${config.repo}"
      label: doc-check
      cap: 5
```

Everything in this example is accepted by the runtime as it stands. That is the point of showing
it: an example carrying a field the loader refuses teaches the reader something that does not work,
and it is the first thing they copy. The `guard` and `retrieve` blocks a later feature will add are
absent for exactly that reason — see the refusal list below.

## Interpolation is namespaced by source

`${config.<key>}` resolves against the deployment's own configuration; `${trigger.<path>}` resolves
against the payload that fired the run. The prefix is required, and a bare `${name}` is refused.

That is a bound rather than a style preference. Without it, a name resolves against whichever source
happens to hold it, so a field in a trigger payload — which for a webhook is written by whoever
sends the request — can shadow a deployment configuration value. Naming the source removes the
question.

Neither namespace reaches the process environment. That is FR-008, and it is why the deployment's
own database password is not addressable from a playbook.

## Fields that are refused rather than warned about

The runtime rejects a playbook at load time, before any trigger is armed, when:

- `agent.tools` contains a bare shell tool, or a write-capable tool that the
  playbook's sinks do not require.
- `agent.restricted` is set to false without the playbook stating why in its
  `description` — turning the coarsest bound off is a decision, not a default.
- `agent.allow` names a whole MCP server rather than individual tools, or
  path-scopes a file tool to a path outside the run's working directory.
- `agent.mcp` names a server that is not configured on this deployment.
- A sink that creates things omits its `cap`.
- A `guard` or `retrieve` block is present while the runtime does not yet apply it. A declared
  bound the runtime ignores is worse than an absent one, so it is refused rather than dropped.
- An interpolation omits its namespace. `${repo}` is refused; `${config.repo}` is not.
- A sink names a type this deployment does not implement. A typo in a sink name would otherwise
  survive the gate and fail at delivery, after a full agent run has been paid for.

A refusal is loud and names the field. The failure mode being prevented is a
playbook that looks bounded, reads as bounded in review, and is not — which is
why the check probes the values it is meant to refuse, not only the ones it is
meant to accept.

## The prompt file

Plain text, next to the YAML. It receives the gathered inputs by path and the
retrieved context by reference. It says what to produce and what would make the
answer wrong — a prompt that only describes the happy path produces confident
reports about machines nobody is going to look at.
