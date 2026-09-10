# Phase 1 — Quickstart

How an operator checks, on real hosts, that the guard does what the suite says it does. This is
the one place in [research.md](./research.md) §1's strategy where "hosts" means separate kernels.
It is a validation rather than a gate, and nothing in it has been run: it is written before the
code it describes.

## Prerequisites

- Two hosts, **A** and **B**, each set up as in the runtime core's
  [quickstart](../001-runtime-core/quickstart.md) §1: `gronin` installed, the agent authenticated.
- An etcd server both can reach, below at `https://etcd.example.com:2379`. One member is enough to
  validate the guarantee; three are needed for it to survive losing a member.
- An HTTP endpoint that logs every POST it receives, below at `https://receiver.example.com/hook`.
  Every run delivers to it once, so the number of deliveries is the number of runs — the effect
  SC-101 asks for rather than a log line claiming a claim was held.

On both hosts, the same playbook, differing from `examples/doc-check.yaml` in three places: a gather
step that sleeps, so a run lasts long enough to collide with; a Discord-shaped sink pointed at the
receiver; and a `guard` block with a shorter wait than the default.

```yaml
gather:
  - run: sleep 60
    as: pause.txt

sinks:
  - discord:
      webhook: ${config.receiver}

guard:
  wait: 5m
```

```bash
gronin config set receiver 'https://receiver.example.com/hook'
```

And `coordination.json` in each state directory, as in [contracts/cli.md](./contracts/cli.md),
with the same `prefix` on both hosts, authenticating with a TLS client certificate rather than a
password (the contract says why).

## 1. The reach is stated (FR-109)

Start `gronin serve` on A with no `coordination.json` and then with one. The first says
`guard: single-host`, the second `guard: cross-host, etcd at 1 endpoint`. A deployment is never
left to infer which guarantee it has.

## 2. A configuration that cannot hold is refused (SC-112)

Set `claim_expiry` to `20s` and leave the other durations at their defaults: 5 + 4 + 10 + 2 = 21 s.
`gronin serve` exits non-zero before arming anything and names all four durations and the expiry.
Restore it.

## 3. One trigger, two hosts, one run (SC-101)

Set the playbook's schedule to the next minute on both hosts, and run `gronin serve` on both.

**Expected**: the receiver logs exactly one delivery for that minute. On the host that did not run
it, `gronin refusals` shows one `claim_held` line naming the other host's run.

## 4. A killed holder does not block the playbook (SC-102)

Invoke the playbook with `gronin run` on A. While its gather step sleeps, `kill -9` A's process.
Within 30 s — the claim expiry — invoke it on B.

**Expected**: B's run starts no later than 30 s after the kill, without anyone touching etcd. On A,
the next `gronin serve` marks the killed run `interrupted`, as the runtime core already does.

## 5. A holder cut off from the backend stops itself (SC-103)

Invoke the playbook on A. During its gather step, drop A's traffic to etcd without rejecting it, so
that A's renewals hang rather than fail:

```bash
sudo iptables -A OUTPUT -p tcp -d etcd.example.com --dport 2379 -j DROP
```

Invoke it on B straight away, and remove the rule with `-D` once A's run has ended.

**Expected**:

- A's run ends with status `claim_lost` about 18 s after its last successful renewal: the deadline
  in [research.md](./research.md) §3.
- A's run is over before 30 s have passed since that renewal.
- B waits: a manual invocation is a trigger that waits.
- B's run starts only after A's claim has lapsed.
- The receiver logs one delivery, from B — A stopped before its sink.

## 6. A configured backend that is down refuses rather than degrades (SC-104)

Stop etcd and invoke the playbook on A.

**Expected**: `gronin run` exits non-zero within the decision bound (5 s), and `gronin refusals`
shows a `backend_unavailable` line naming the endpoint. The receiver logs nothing.

## 7. Waiting, one deep (SC-105)

Invoke the playbook on A. While it runs, invoke it on A three more times.

**Expected**:

- The first of the three waits, and `gronin run` prints that it is waiting.
- The other two are refused with `waiting_slot_full`.
- The waiting one starts after the first run ends, and `gronin show` on it says it waited and for
  how long.
- `gronin refusals` shows no line for it (SC-114).

## 8. The rate limit (SC-107)

Add `rate: {runs: 2, per: 10m}` to the `guard` block on both hosts and restart `serve`. Invoke the
playbook four times, each after the previous one ended, inside ten minutes, alternating hosts.

**Expected**: two runs and two `rate_limited` refusals. Neither refused trigger waits. The window is
the deployment's, not each host's: counted per host, alternating would have allowed four.

## 9. A killed process's waiting trigger is visible (SC-113)

Invoke the playbook on A, then invoke it again on A so that one waits. `kill -9` the process
holding the waiting one.

**Expected**: `gronin refusals` on A shows a `dropped` line naming when the trigger was accepted,
and no run ever comes from it — including after `gronin serve` is started again.

## Renaming a playbook

The name is the claim's identity. A rolling restart that loads a renamed playbook on B while A still
runs it under the old name can run it on both at once, and its rate window starts empty under the
new name ([research.md](./research.md) §4). Let the old name's runs finish on every host before
restarting any of them with the new name.
