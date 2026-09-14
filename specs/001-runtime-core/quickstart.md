# Phase 1 — Quickstart

The path SC-001 is measured against: someone who has never seen this takes a shipped example,
changes two things, and gets a report delivered. Under thirty minutes, without reading the source.

This has been followed end to end against the real agent, which is what T063 asked for and what
every earlier test had stubbed — including step 1, from the v0.1.0 release: downloaded, checksum
verified against the signed SHA256SUMS, confirmed statically linked. It is a description of working
software rather than a target.

## 1. Install

```bash
curl -sSLO https://github.com/nicodarge/Gronin/releases/latest/download/gronin-linux-amd64
curl -sSLO https://github.com/nicodarge/Gronin/releases/latest/download/SHA256SUMS
sha256sum --ignore-missing -c SHA256SUMS &&
  chmod +x gronin-linux-amd64 && sudo mv gronin-linux-amd64 /usr/local/bin/gronin
gronin version
```

One file. No runtime to install first, no package manager, no virtual environment. This is the step
SC-007 exists to protect, and it is the one most likely to lose a stranger.

`gronin version` prints its own version and the agent version it found. If the agent is missing or
below the floor it says so here rather than at the first scheduled run.

## 2. Point it at a directory

```bash
mkdir -p "$(gronin state-dir)/playbooks"
cp examples/doc-check.{yaml,prompt} "$(gronin state-dir)/playbooks/"
```

## 3. Change two things

In `doc-check.yaml`, the schedule, and the destination — replacing the `sinks:` block the
example ships with rather than adding a second sink beside it:

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
printf '%s' 'https://discord.com/api/webhooks/REPLACE_ME' | gronin config set discord_webhook
printf '%s' "$HOME/some-repository" | gronin config set checkout
```

The value is read from standard input, never from the command line: a command line is journalled
and shipped to log aggregation, where the value would then sit for the whole retention window.

The second one is the repository the example reads. Both are refused at load if they are unset, so
a value you forget is found below rather than at six in the morning.

## 4. Check it before arming it

```bash
gronin validate
```

This is the load gate. If it refuses, it names the playbook, the field, what it found and what
would be accepted, for every problem at once.

It needs no credential, so it also belongs in CI — but it does need the configuration keys to
exist, because a reference resolving to nothing is one of the things it refuses. In CI, set them to
placeholders: what the gate checks is that a name resolves, never what it resolves to.

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
