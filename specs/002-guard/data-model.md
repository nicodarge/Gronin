# Phase 1 — Data Model

Three places hold this feature's state, and which one holds what is most of the design.

- **The coordination backend** holds what every host must agree on: who holds a claim, and which
  runs occupy a rate window. Nothing else — a trigger that waits is local to one process
  (FR-113), and a record is local to one host.
- **Process memory** holds the waiting trigger itself and the live claim handle. Both die with the
  process, deliberately.
- **The record store** of the host that handled a trigger holds everything an operator reads
  afterwards: refusals, the acceptance of every waiting trigger, and the runs. Each host reads its
  own; there is no cross-host view.

A deployment with no backend configured keeps all of it on one host. There, the file lock stands in
for the claim, and the record store's runs stand in for the rate window.

## Entities

### Guard declaration (in the playbook)

The `guard` block, validated at load (FR-119); its schema is
[contracts/guard.schema.json](./contracts/guard.schema.json). A playbook with no block is still
held to non-concurrency (FR-120) and gets the default waiting expiry.

| Field | Type | Notes |
| ----- | ---- | ----- |
| `rate.runs` | integer ≥ 1 | At most this many runs start within any window of `rate.per` (FR-114) |
| `rate.per` | duration, minutes or hours | Keyed on the playbook name alone |
| `wait` | duration | How long a waiting trigger may wait (FR-112). Default 30 m, per [research.md](./research.md) §3 |

No backend address, credential or duration of the claim set appears here. Those belong to the
deployment (Principle IV), and a playbook declaring a rate limit stays portable to a deployment that
coordinates differently.

### Coordination configuration (in the deployment)

`coordination.json` in the state directory, beside `config.json` and `mcp_servers.json`. Its
absence is the single-host deployment of FR-109, and `serve` says which one it is at startup. Its
presence with a backend that cannot be reached is a refusal, never a fallback (FR-107). The keys
are in [contracts/cli.md](./contracts/cli.md).

| Field | Type | Notes |
| ----- | ---- | ----- |
| `etcd.endpoints` | list of URLs | |
| `etcd.prefix` | string | Every key this deployment writes is under it |
| `etcd.username`, `etcd.password` | reference | `${config.…}` references, resolved like the MCP catalogue's; a value marked secret seeds the redactor |
| `etcd.tls` | CA, certificate and key paths | Optional |
| `claim_expiry`, `renew_every`, `renew_bound`, `stop_bound`, `decision_bound` | duration | Defaults in [research.md](./research.md) §3 |

The margin floor is not a field. It is the one term of FR-123's inequality a deployment cannot
configure away.

**Validation at startup** (US1 scenario 6, R5 of the coordination contract): refused when
`renew_every + renew_bound + stop_bound + margin floor > claim_expiry`; when `renew_bound` is not
below `renew_every`; when `claim_expiry` is not a whole number of seconds. The refusal names every
duration and the expiry they exceed.

### Claim (in the backend)

The right to run one named playbook, held by one run at a time across the deployment.

| Field | Where | Notes |
| ----- | ----- | ----- |
| key | backend | `<prefix>/claims/<playbook name>` — the name as it stood when the claim was taken |
| holder | backend, as the key's value | Host, process instance, run identifier, when taken. Read back only to name the holder in a refusal |
| lease | backend | Carries the expiry; the server judges it (FR-106) |
| token | backend | The key's creation revision, strictly increasing across claims on one name |
| sent | holder's memory | When the last renewal that succeeded was sent, on the monotonic clock |
| deadline | holder's memory | `sent + claim_expiry − stop_bound − margin floor` |

```text
acquired ──renew ok──▶ acquired
    │  └─ deadline passed, or Renew/Fence says lost ──▶ stopping ──▶ over (run status claim_lost)
    ├─ run ends ──▶ released
    └─ holder dies ──▶ lapsed (by the backend, within claim_expiry)
```

The run releases through its lease, never by looking the name up again, so a rename during the run
cannot orphan the claim ([research.md](./research.md) §4).

### Rate slot (in the backend)

`<prefix>/rate/<playbook name>/<i>` for `0 ≤ i < rate.runs`, each on a lease of its own lasting
`rate.per`, never renewed. A run takes a free slot in the same transaction as its claim (FR-115),
and the slot frees when its lease lapses — not when the run ends, because the limit bounds runs,
not concurrency.

On a single-host deployment the rate window is the record store's own count of runs of that
playbook, triggered by a schedule or by hand, started within the last `rate.per` on the host clock.

**Why the window is in the backend at all**: counted per host, two hosts would each allow
`rate.runs`, and the deployment twice the limit it declared.

### Waiting trigger (in process memory, accepted durably)

At most one per playbook per state directory (FR-111). The trigger itself is in memory and dies
with the process that accepted it (FR-113). Its acceptance is written to the record store at the
moment it is accepted (FR-127), so a kill cannot erase it.

The slot is counted in the record store, not in memory, because `gronin run` executes in a process
of its own: counted per process, every manual invocation would find an empty slot and three
invocations during one run would all wait. Acceptance is one write transaction that inserts the row
only if no live `waiting` row exists for the playbook. Across hosts each record store counts its
own, so a deployment of several hosts can hold one waiting trigger per host; each re-contends for
the claim when it frees, and only one of them gets it at a time.

Only a trigger that will not come again waits — a manual invocation now, a webhook delivery later.
A scheduled trigger is refused instead. That follows the first finding in
[research.md](./research.md), which is the owner's to confirm; if it is overturned, this sentence
is what changes.

| Field | Type | Notes |
| ----- | ---- | ----- |
| `id` | identifier | |
| `playbook_name` | string | The name it was accepted under |
| `playbook_path` | path | The file it came from. FR-121 re-reads this file when the trigger runs |
| `trigger_kind` | enum | `manual` |
| `trigger_ref` | blob ref, nullable | The trigger's values, redacted at the write boundary like every other record |
| `accepted_at`, `expires_at` | timestamp | Host clock, UTC (FR-118). `expires_at` is `accepted_at + wait` |
| `instance` | identifier | The accepting process, whose liveness decides whether a `waiting` row is really waiting |
| `outcome` | enum | `waiting`, `ran`, `wait_expired`, `rate_limited`, `playbook_changed`, `backend_unavailable`, `dropped` |
| `outcome_at` | timestamp, nullable | |
| `run_id` | identifier, nullable | Set when `outcome` is `ran` |

```text
waiting ─ claim freed, rate slot free, file unchanged ─▶ ran
        ─ expires_at passed ─────────────────────────▶ wait_expired
        ─ rate window full when it tries (FR-124) ───▶ rate_limited
        ─ file gone, renamed, or refused at load ────▶ playbook_changed
        ─ backend unreachable when it tries ─────────▶ backend_unavailable
        ─ accepting process gone ────────────────────▶ dropped
```

**Which rows are dropped.** Every process that can accept a waiting trigger holds an exclusive
advisory lock on `instances/<instance>.lock` in the state directory for its whole life; the kernel
releases it when the process dies, however it dies — the property the runtime core's crash test
already proves for the playbook lock. A `waiting` row whose instance lock can be taken belongs to a
dead process. `serve` at startup, and `gronin refusals` when it reads, mark such rows `dropped` and
write their refusal records. That is what makes SC-113 readable after a kill without anything
having been written on the way out. Two processes on one state directory — a `serve` and a
`gronin run` by hand — each hold their own instance lock, so neither drops the other's live
triggers.

### Refusal record (in the record store)

A trigger that did not become a run, terminally (FR-117). Not a run, never counted as one, and never
written for a trigger that waited and then ran (SC-114).

| Field | Type | Notes |
| ----- | ---- | ----- |
| `id` | identifier | |
| `playbook_name` | string | |
| `trigger_kind` | enum | `schedule`, `manual`, `replay`, `resume` |
| `due_at` | timestamp, nullable | For a scheduled trigger, the instant its schedule computed |
| `waiting_trigger_id` | identifier, nullable | Set when the refusal ended a wait |
| `mechanism` | enum | See below |
| `detail` | text | The holder, the limit, the backend and its error, or what the file now declares |
| `refused_at` | timestamp | Host clock, UTC |

| `mechanism` | Written when |
| ----------- | ------------ |
| `claim_held` | A trigger that does not wait found its playbook running: a scheduled one, a replay, a resume. `detail` names the holder |
| `waiting_slot_full` | A trigger that would wait found one already waiting (FR-111) |
| `wait_expired` | A waiting trigger passed `expires_at` (FR-112) |
| `rate_limited` | The rate window refused it, at arrival or when a waiting trigger tried to run (FR-116, FR-124). `detail` names the limit |
| `backend_unavailable` | The backend refused, was unreachable, or did not decide within `decision_bound` (FR-107, FR-108). `detail` names the backend |
| `playbook_changed` | A waiting trigger's file was gone, renamed or refused at load when it came to run (FR-121) |
| `dropped` | A waiting trigger's process ended before it ran (FR-113) |

**Relation to missed occurrences.** The runtime core's missed-occurrence record keeps its meaning:
an occurrence that never fired because the runtime was not running, and the catch-up overflow. A
scheduled trigger refused by the guard is recorded as a refusal and not also as a missed
occurrence; the scheduler's fire function returns no error once the guard has written the refusal,
because two records of one event tell an operator it happened twice.

**Replay and resume** take the claim like any run, because a resume delivers to the same sinks a
live run would. They are refused rather than made to wait, and they take no rate slot: the limit
bounds a trigger source, and a replay is an operator acting on one recorded run.

### Run (runtime core, changed)

The runtime core's Run entity
([specs/001-runtime-core/data-model.md](../001-runtime-core/data-model.md)) gains four fields and one
status. That document is updated when this feature's tasks land. It is recorded here so the
change is not discovered in a migration.

| Field | Type | Notes |
| ----- | ---- | ----- |
| `waiting_trigger_id` | identifier, nullable | The waiting trigger it started from (FR-125) |
| `waited_ms` | integer, nullable | From `accepted_at` to `started_at`, both host clock. Null for a run that did not wait |
| `claim_reach` | enum | `cross-host` or `single-host` — which guarantee this run actually ran under |
| `claim_token` | integer, nullable | The fencing token it held. Null on a single-host deployment |

`status` gains `claim_lost`: the run was stopped because its claim could no longer be proven held
(FR-105). It is not `failed` — nothing in the run went wrong, the deployment lost its authority —
and it is not `interrupted`, which the runtime core reserves for a run found still `running` by the
next process to start.
