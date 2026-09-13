# Playbook format

A playbook is a YAML file plus a prompt file. It is the unit of sharing: it
diffs, it reviews, it tests, and someone can contribute one without
understanding the runtime.

The schema an editor should resolve is published at
<https://nicodarge.github.io/Gronin/playbook.schema.json>. It is the shape layer
and it is not the gate: a document it accepts may still be refused at load, by
design — the refusals that matter are semantic and no schema can express them.
`gronin validate` is what answers whether a playbook will actually be armed.

## Shape

```yaml playbook
name: doc-check
description: Report documentation that no longer describes the code.

trigger:
  type: cron
  schedule: "0 6 * * *"

gather:
  - run: git -C ${config.checkout} log --oneline -n 200
    as: recent-commits.txt
  - run: git -C ${config.checkout} ls-files 'docs/*.md' '*.md'
    as: documents.txt

agent:
  model: claude-sonnet-5
  prompt_file: doc-check.prompt
  restricted: true
  tools: [Read, Grep, Glob]
  allow:
    - "Read(./**)"
    - "Grep(./**)"
    - "Glob(./**)"
  output_schema:
    type: object
    required: [findings]
    properties:
      findings:
        type: array
        items:
          type: object
          required: [title, body]
          properties:
            title: {type: string}
            body: {type: string}
  timeout: 30m

sinks:
  - github:
      repo: ${config.repo}
      cap: 5
```

This is [`examples/doc-check.yaml`](../examples/doc-check.yaml) verbatim, and a test pins the two
together. It is also driven through the load gate rather than only through the schema above, which
is a different check: the schema is the shape layer, and it accepted an example carrying a sink type
this deployment does not implement, a `label` field the GitHub sink never reads, and an
`output_schema` no run could compile. An example is the first thing a reader copies, so what it is
checked against is the gate that arms a playbook. The `guard` block a later feature will add is
absent for the same reason — see the refusal list below.

## Interpolation is namespaced by source

`${config.<key>}` resolves against the deployment's own configuration; `${trigger.<path>}` resolves
against the payload that fired the run. The prefix is required, and a bare `${name}` is refused.

That is a bound rather than a style preference. Without it, a name resolves against whichever source
happens to hold it, so a field in a trigger payload — which for a webhook is written by whoever
sends the request — can shadow a deployment configuration value. Naming the source removes the
question.

Neither namespace reaches the process environment. That is FR-008, and it is why the deployment's
own database password is not addressable from a playbook.

A reference that resolves to nothing is refused at load, before anything is armed — so `gronin
validate` needs the deployment's configuration keys to exist. The values may be placeholders; what
it checks is that a name resolves, not what it resolves to.

### A gather step resolves through its environment, not into its text

A gather step is a shell line, and substituting a value into a shell line is command injection by
construction: a configuration value of `x; rm -rf ~` would become a second command, and nothing the
playbook author writes can prevent that, because the value belongs to the deployment and the
playbook cannot see it.

So `${config.checkout}` in a gather step becomes `"$GRONIN_CONFIG_CHECKOUT"`, and the value reaches
the step through its environment. A double-quoted expansion is not re-parsed by the shell, so the
value arrives as exactly one word whatever bytes it holds.

That containment only holds where the substitution controls its own quoting, so a reference inside
quotes is refused rather than bound:

```yaml
gather:
  - run: git ls-files ${config.glob}        # bound
    as: files.txt
  - run: git ls-files '${config.glob}'      # refused
    as: quoted.txt
```

Inside single quotes the expansion would not happen at all and the step would silently receive the
literal text; inside double quotes it would nest and split on whitespace. Both are quiet, which is
why they are refused instead. A value is one word: a reference is not a way to pass several
arguments.

### A creating sink counts against its own label

The `github` sink puts a label on every issue it opens, and counts the open ones carrying that
label to decide whether there is room under the `cap`. The label is therefore what the cap is
measured against, not decoration: a playbook naming none gets `gronin`, and two playbooks opening
issues on one repository want two labels, or they share a cap and the busier of them silences the
other.

```yaml
sinks:
  - github:
      repo: ${config.repo}
      label: doc-drift
      cap: 5
```

A label naming `${trigger.…}` is refused. Whatever names the label chooses the bucket the ceiling
applies to, so a trigger naming it would make the cap per-trigger rather than per-repository: a
payload varying the label would mint a fresh empty bucket every run, each respecting its own
ceiling while the repository filled up. `${config.…}` is fine — the deployment names it.

A label holding a comma is refused. GitHub reads `labels=` as a list, so `a,b` would count the
issues carrying *both* while creating issues whose single label is the literal `a,b` — the count
and the creation would name different things, and the cap would be measured against a set the sink
never adds to. A label declared as empty is refused for the neighbouring reason: it drops the
filter and counts every open issue in the repository.

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
- A creating sink's `label` holds a comma, resolves to nothing, or names `${trigger.…}` — see above.
- A `guard` block is present while the runtime does not yet apply it. A declared bound the
  runtime ignores is worse than an absent one, so it is refused rather than dropped.
- An interpolation omits its namespace. `${repo}` is refused; `${config.repo}` is not.
- A `${config.x}` names a key this deployment does not hold. Refused at load rather than at trigger
  time, which is the difference between finding out now and finding out at six in the morning.
- A gather step puts a reference inside quotes — see above.
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
