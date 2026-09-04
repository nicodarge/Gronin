# Phase 1 — Quickstart

The path SC-001 is measured against: someone who has never seen this takes a shipped example,
changes two things, and gets a report delivered. Under thirty minutes, without reading the source.

Everything below is the target experience, not a description of working software.

## 1. Install

```bash
curl -sSLo gronin https://github.com/nicodarge/Gronin/releases/latest/download/gronin-linux-amd64
chmod +x gronin && sudo mv gronin /usr/local/bin/
gronin version
```

One file. No runtime to install first, no package manager, no virtual environment. This is the step
SC-007 exists to protect, and it is the one most likely to lose a stranger.

`gronin version` prints its own version and the agent version it found. If the agent is missing or
below the floor it says so here rather than at the first scheduled run.

## 2. Point it at a directory

```bash
mkdir -p ~/.gronin/playbooks
cp examples/doc-check.{yaml,prompt} ~/.gronin/playbooks/
```

## 3. Change two things

In `doc-check.yaml`, the schedule and the destination:

```yaml
trigger:
  type: cron
  schedule: "0 6 * * 1"        # was daily; now Monday mornings

sinks:
  - discord:
      webhook: ${config.discord_webhook}
```

The destination is a reference, not a value, and the `config.` prefix names where it resolves from.
Secrets and endpoints live in the deployment's configuration, never in a playbook — which is what
lets you commit the playbook and share it. The other namespace is `${trigger.…}`, for what fired the
run; a bare `${name}` is refused, so a payload can never shadow a configuration value.

```bash
gronin config set discord_webhook 'https://discord.com/api/webhooks/REPLACE_ME'
```

## 4. Check it before arming it

```bash
gronin validate ~/.gronin/playbooks
```

This is the load gate, and it needs no credential — so it also belongs in CI. If it refuses, it
names the playbook, the field, what it found and what would be accepted, for every problem at once.

## 5. Run it once, by hand

```bash
gronin run doc-check
```

Immediately, ignoring the schedule. This is what you use while iterating on a prompt.

## 6. Look at what happened

```bash
gronin runs
gronin show <run-id>
```

`show` prints what each gather step returned, the prompt as sent, every tool call with its input and
output, what it cost, what each sink did — and anything the agent reached for that its bounds
refused. That last section is usually empty. When it is not, the playbook's tool set is wrong or its
prompt is steering somewhere it should not.

## 7. Iterate without paying twice

```bash
gronin replay <run-id>     # re-runs the agent against the same inputs, after a prompt change
gronin resume <run-id>     # re-runs only the sinks, when delivery failed and the reasoning was fine
```

Two verbs because they cost differently. `replay` spends tokens and `resume` spends none.

## 8. Leave it running

```bash
gronin serve
```

Loads and validates everything, verifies the credential and the agent version, arms the schedules.
If any playbook is refused, nothing is armed and the process exits non-zero — a partial start would
be the failure this whole design exists to prevent.

## What you did not have to do

No workflow to draw. No node to wire to another node. No separate machine for the agent to be
reached on. The playbook you edited is a file you can commit, diff, review, and hand to someone
else who runs none of your infrastructure.
