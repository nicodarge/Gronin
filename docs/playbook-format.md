# Playbook format

A playbook is a YAML file plus a prompt file. It is the unit of sharing: it
diffs, it reviews, it tests, and someone can contribute one without
understanding the runtime.

## Shape

```yaml
name: doc-check
description: Report documentation that no longer describes the code.

trigger:
  type: cron
  schedule: "0 6 * * *"

guard:
  lock: agent          # named lock; runs of playbooks sharing a name serialise
  rate_limit: 1/6h     # refuse a second run inside the window
  dedup_key: "${repo}" # wall-clock anchored, never a payload timestamp

gather:
  - run: gh issue list --repo ${repo} --state open --json number,title
    as: open-issues.json
  - run: git -C ${checkout} log --oneline -n 200
    as: recent-commits.txt

retrieve:                        # optional
  collection: incidents
  query: "documentation drift ${repo}"
  limit: 5

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
  timeout: 1800

sinks:
  - github_issues:
      repo: "${repo}"
      label: doc-check
      cap: 5
  - metrics:
      job: doc_check
```

## Fields that are refused rather than warned about

The runtime rejects a playbook at load time, before any trigger is armed, when:

- `agent.tools` contains a bare shell tool, or a write-capable tool that the
  playbook's sinks do not require.
- `agent.restricted` is set to false without the playbook stating why in its
  `description` — turning the coarsest bound off is a decision, not a default.
- `agent.allow` names a whole MCP server rather than individual tools, or
  path-scopes a file tool to a path outside the run's working directory.
- `agent.mcp` names a server that is not configured on this deployment.
- `guard.dedup_key` interpolates a timestamp originating in the trigger payload.
- A sink that creates things omits its `cap`.
- A `guard` or `retrieve` block is present while the runtime does not yet apply it. A declared
  bound the runtime ignores is worse than an absent one, so it is refused rather than dropped.

A refusal is loud and names the field. The failure mode being prevented is a
playbook that looks bounded, reads as bounded in review, and is not — which is
why the check probes the values it is meant to refuse, not only the ones it is
meant to accept.

## Interpolation

`${...}` resolves against the trigger payload and the deployment's own
configuration. It never resolves against the process environment: exposing a
whole environment to a template hands the deployment's own database password to
every playbook.

## The prompt file

Plain text, next to the YAML. It receives the gathered inputs by path and the
retrieved context by reference. It says what to produce and what would make the
answer wrong — a prompt that only describes the happy path produces confident
reports about machines nobody is going to look at.
