# Contract — Operator surface, what the guard changes

A delta on the runtime core's [cli.md](../../001-runtime-core/contracts/cli.md). Commands not named
here are unchanged.

## Commands

| Command | Change | Exit code |
| ------- | ------ | --------- |
| `gronin serve` | Reads `coordination.json`, refuses a configuration whose durations cannot hold, and prints the reach of its guarantee before arming anything | Non-zero, nothing armed, if the configuration is refused |
| `gronin run <playbook>` | Waits when the playbook is already running, up to its `guard.wait`, saying so; otherwise refused with the mechanism named | Non-zero if refused, or if the run that finally started did not succeed |
| `gronin refusals` | New. Lists refusal records, most recent first, and first marks as dropped every waiting trigger whose process is gone | |
| `gronin show <run>` | Adds whether the run waited and for how long, and the guarantee it ran under | |
| `gronin runs` | `claim_lost` appears as a status | |

`gronin refusals` is SC-108's surface. It opens the record store the way `gronin runs` does, so it
works after `serve` has been killed. That is precisely when the drop it has to show (SC-113) is
readable and nobody else is left to write it.

## Output

The reach is the first thing `serve` says about the guard (FR-109):

```text
$ gronin serve
guard: cross-host, etcd at 3 endpoints under prefix gronin/, claim expiry 30s
...
```

```text
$ gronin serve
guard: single-host — no coordination backend is configured, so two hosts running these
       playbooks would each run them
...
```

A configuration whose durations cannot hold is refused before anything is armed (US1 scenario 6):

```text
$ gronin serve
refused: coordination.json
  renew_every 10s + renew_bound 8s + stop_bound 10s + margin 2s = 30s exceeds claim_expiry 20s
    accepted: durations whose sum, with the 2s margin, fits within claim_expiry
Nothing was armed.
```

A backend that is configured but cannot be reached when `serve` starts does not stop it: the
backend may be back before the first trigger. It is said, and every trigger until then is refused
naming it (FR-107):

```text
guard: cross-host, etcd at 3 endpoints — NOT reachable at startup (context deadline exceeded
       after 5s); every trigger will be refused until it answers
```

A manual invocation that finds its playbook running:

```text
$ gronin run doc-check
doc-check is running (run 20260910T060000Z-3f9a1c0b2e4d on host-b.example.com, process 8f2c…); waiting up to 30m
20260910T061214Z-a41c09e7b6f2 succeeded (waited 12m14s)
```

```text
$ gronin refusals
2026-09-10T06:00:00Z  doc-check  schedule  claim_held          held by run 20260910T060000Z-3f9a1c0b2e4d on host-b.example.com
2026-09-10T06:03:11Z  doc-check  manual    waiting_slot_full   a trigger accepted at 06:01:02Z is already waiting
2026-09-10T06:05:00Z  doc-check  schedule  tick_already_ran    tick 06:05:00Z ran as 20260910T060500Z-7b21e4c09d3a on host-b.example.com
2026-09-10T07:30:00Z  doc-check  manual    dropped             accepted at 07:12:40Z; process 8f2c… ended before it ran
```

## `coordination.json`

In the state directory. Absent means a single-host deployment. Present, every key is checked at
startup, and an unknown key is refused like an unknown playbook key.

```json
{
  "etcd": {
    "endpoints": [
      "https://etcd-1.example.com:2379",
      "https://etcd-2.example.com:2379",
      "https://etcd-3.example.com:2379"
    ],
    "prefix": "gronin/",
    "username": "${config.etcd_username}",
    "password": "${config.etcd_password}",
    "tls": {
      "ca": "/etc/gronin/etcd-ca.pem",
      "cert": "/etc/gronin/etcd-client.pem",
      "key": "/etc/gronin/etcd-client-key.pem"
    }
  },
  "claim_expiry": "30s",
  "renew_every": "5s",
  "renew_bound": "4s",
  "stop_bound": "10s",
  "decision_bound": "5s"
}
```

Every duration is optional and defaults to the value above, which is the set chosen in
[research.md](../research.md) §3. The credential fields take `${config.…}` references only, never
a literal: a credential written into this file would sit beside the deployment's other state in
clear, and one resolved from a secret configuration value is known to the redactor.

A deployment authenticates to etcd with a username and password, with a TLS client certificate, or
with both; which one is the etcd deployment's choice, not a constraint of this runtime's.
`gronin config set` reads its value from standard input and refuses one given as an argument, so
setting the password puts no secret on a command line, which the constitution's Secrets constraint
forbids. `--secret` marks it for the redactor:

```bash
gronin config set etcd_username < /path/to/etcd-username
gronin config set --secret etcd_password < /path/to/etcd-password
```
