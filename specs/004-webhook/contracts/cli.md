# Contract — Operator surface, what the webhook changes

A delta on the runtime core's [cli.md](../../001-runtime-core/contracts/cli.md) and the guard's
[cli.md](../../002-guard/contracts/cli.md). Commands not named here are unchanged. The ingress's own
surface is [ingress.md](./ingress.md).

## Commands

| Command | Change | Exit code |
| ------- | ------ | --------- |
| `gronin serve` | `--ingress-address`, no default. Absent, nothing listens for deliveries (FR-305). Refuses to start when a loaded playbook has a webhook trigger and no ingress address is set, and when the ingress and the API are set on one port (FR-304). Marks dropped every delivery whose process is gone, before either listener opens (FR-315). Prints the ingress address and the reach of repeat detection (FR-319) | Non-zero, nothing armed and nothing listening, on any refusal |
| `gronin sources list` | New. Every configured source: name, signature header, identity location, replay window. Never the secret, and never resolves its reference (FR-311) | |
| `gronin deliveries` | New. Deliveries, most recent first: when received, source, state — `accepted`, `waiting`, `handed_off`, `dropped` or `unbound` — repeats, and the runs each hand-off became. First marks dropped every delivery whose process is gone, hand-offs still waiting under the guard included, as `gronin refusals` does for the guard's waiting triggers | |
| `gronin deliveries show <id>` | New. One delivery in full: its identity, peer, body, and each hand-off with its outcome — the run, the guard refusal, the wait, or the value refused | Non-zero if no delivery has that identifier |
| `gronin deliveries refused` | New. Authenticated refusals one per line, then unauthenticated counts per minute, reason and source bucket (FR-332, FR-333) | |
| `gronin run <playbook>` | For a playbook with a webhook trigger, `--trigger` values are held to its declarations before anything runs, with the message a delivery would get; an undeclared name is refused (FR-327) | Non-zero, and no run, if refused |
| `gronin show <run>` | Names the delivery a webhook run came from (FR-336) | |
| `gronin runs` | `webhook` appears as a trigger kind | |
| `gronin refusals` | A refusal of a webhook trigger names its delivery (FR-328) | |

`gronin deliveries` opens the record store the way `gronin runs` does, so it works after `serve` has
been killed — which is when the drop it has to show (SC-304) is readable and nobody else is left to
write it.

The operator API gains three read-only routes, behind its existing credential rule and on its
existing listener only (FR-335): `GET /deliveries`, `GET /deliveries/{id}` and
`GET /deliveries/refused`. The ingress serves none of them.

## Output

```text
$ gronin serve --ingress-address 0.0.0.0:8443
guard: single-host — no coordination backend is configured, so two hosts running these
       playbooks would each run them
armed 2 schedule(s) of 4 playbook(s) from /var/lib/gronin/playbooks
API on 127.0.0.1:7800
ingress on [::]:8443 for sources alerts, forge; repeat detection reaches this host only —
       a retry that lands on another host behind the same address runs again
```

A webhook playbook with no ingress, and two listeners on one port, are refused before anything is
armed:

```text
$ gronin serve
refused: alert-triage.yaml has a webhook trigger, and no --ingress-address is set
Nothing was armed.
```

```text
$ gronin serve --api-address 127.0.0.1:8443 --ingress-address 0.0.0.0:8443
refused: the API (127.0.0.1:8443) and the ingress (0.0.0.0:8443) are on one port
    accepted: two different ports; port 0 picks a free one for each
Nothing was armed.
```

```text
$ gronin sources list
alerts   header X-Grafana-Alerting-Signature   identity digest of the body   window 10m
forge    header X-Forgejo-Signature            identity /delivery_uuid       window 30m
```

```text
$ gronin deliveries
2026-09-10T06:35:12Z  alerts  waiting     repeats 0  alert-triage (waiting under the guard)
2026-09-10T06:33:40Z  forge   unbound     repeats 0  no playbook is bound to forge
2026-09-10T06:31:02Z  alerts  dropped     repeats 0  alert-triage (not reached)
2026-09-10T06:12:14Z  alerts  handed_off  repeats 1  alert-triage → 20260910T061214Z-a41c09e7b6f2
```

```text
$ gronin deliveries refused
2026-09-10T06:40:11Z  alerts  value_no_match  alertname  playbook alert-triage  from 192.0.2.10
2026-09-10T06:41:00Z  (unconfigured)  unknown_source      x10412  last from 192.0.2.7
2026-09-10T06:41:00Z  alerts          signature_mismatch  x3      last from 192.0.2.7
```

A manual invocation held to the declarations:

```text
$ gronin run alert-triage --trigger 'alertname=disk full; rm'
refused: alert-triage: alertname does not wholly match [A-Za-z0-9_.:-]+
```

## `sources.json`

In the state directory, beside `config.json`. Absent means no source is configured, and a webhook
playbook is then refused at load for naming one (FR-310).

```json
{
  "alerts": {
    "secret": "${config.alerts_hook_secret}",
    "signature_header": "X-Grafana-Alerting-Signature"
  },
  "forge": {
    "secret": "${config.forge_hook_secret}",
    "signature_header": "X-Forgejo-Signature",
    "identity": "/delivery_uuid",
    "replay_window": "30m"
  },
  "github": {
    "secret": "${config.github_hook_secret}",
    "signature_header": "X-Hub-Signature-256",
    "signature_prefix": "sha256="
  }
}
```

`secret` takes a `${config.…}` reference only, and the value it names must be marked secret. Setting
it puts nothing on a command line, which the constitution's Secrets constraint forbids:
`gronin config set` reads its value from standard input and refuses one given as an argument.

```bash
gronin config set --secret alerts_hook_secret < /path/to/alerts-hook-secret
```

Which senders can sign this way, and in which header, is research.md §7. A sender that cannot — one
that signs nothing, or signs a timestamp together with the body — needs something in front of the
ingress that verifies what it sends and re-signs the exact body under a secret this deployment holds.
Grafana fits provided its optional timestamp header is left unset.
