# Contract — The ingress

What a sender may send, what it is answered, and what each outcome leaves in the record. The
ingress is a listener of its own (FR-301); the operator API is unchanged except for the read-only
routes in [cli.md](./cli.md).

## The route

```text
POST /hooks/<source>
<signature_header>: <signature_prefix><64 hexadecimal characters>

<a JSON body>
```

That is the whole surface. Any other path, and any other method on this path, is answered
`404 Not Found` with the same fixed body (FR-302) — not `405`, which would confirm the path exists.
The route is matched on the path alone and the method checked in the handler, because the standard
library's method-qualified patterns answer `405` by themselves.

The signature is HMAC-SHA256 over the exact bytes of the body, under the source's secret, in
hexadecimal of either case, preceded by the source's declared prefix (FR-306, FR-307). No
`Content-Type` is required: senders differ, and the body is parsed as JSON whatever it claims.

## The order of a delivery

Load-bearing: each step refuses before the next costs anything.

| Step | Refused as | Recorded as |
| ---- | ---------- | ----------- |
| 1. Path and method | `404` | a count, `not_found` |
| 2. A slot among the deliveries in progress (FR-331) | `503`, at once | a count, `busy` |
| 3. The declared length against the body bound (FR-329) | `413`, before reading | a count, `body_too_large` |
| 4. The body, under the byte bound and the request's time bound (FR-329, FR-330) | `413` past the bound; a stall closes the connection with no status | a count, `body_too_large` or `body_timeout` |
| 5. Source and signature, together (FR-307 to FR-309) | `403`, one fixed answer for every failure | a count, by reason and source bucket |
| 6. The body as JSON, and its identity (FR-316) | `400`, naming the reason | a delivery refusal, with the body |
| 7. The acceptance, bounded by the durable step (FR-312, FR-313, FR-317, FR-318) | `503` past the bound | the delivery, if the write lands at all |
| 8. The answer | `202` whether new or a repeat | — |
| 9. The hand-off, one per bound playbook, driven by step 7 completing (FR-314) | — | decided by a run, a guard refusal or a delivery refusal; a waiting trigger leaves it `waiting` until one of those, or its drop (FR-315) |

Nothing about the body is stored or parsed before step 5 passes. Every record written at any step
takes the connection's own peer address; a forwarding header is never read (FR-334).

Headers that stall or exceed their bound are ended by the HTTP server before step 1: the sender gets
`431`, or a closed connection, and no handler runs.

## Step 5 in detail

Every request reaching step 5 computes exactly one MAC and one constant-time comparison, whatever
is wrong with it:

- a configured source: its secret is the key;
- a name the deployment does not configure: a key the process generates at startup and holds for
  nothing else (FR-309);
- the offered value is decoded only when the header appears exactly once, starts with the declared
  prefix, and the remainder decodes to exactly 32 bytes. Otherwise a 32-byte buffer that no MAC
  equals is compared instead, and the request is refused whatever the comparison says (FR-308).

The comparison is `hmac.Equal`, which is constant time over equal lengths; the length is made equal
before it runs, and is not a secret (research.md §4). The answer to every failure here is the same
status, the same headers and the same body, byte for byte, so the ingress does not tell a stranger
which source names exist.

## Answers

| Status | Body | Meaning to the sender |
| ------ | ---- | --------------------- |
| `202 Accepted` | `accepted` | Recorded durably. Not "ran": the guard decides that later |
| `503 Service Unavailable` | `not accepted; retry` or `busy; retry` | Nothing was accepted by this answer. Retry |
| `403 Forbidden` | `refused` | Unauthenticated. A retry with the same signature is refused again |
| `400 Bad Request` | `refused: <reason>` | Authenticated, and unusable: not JSON, or its declared identity is absent or not a single value. Only an authenticated sender reads a reason |
| `413 Content Too Large` | the standard library's | The body bound |
| `431 Request Header Fields Too Large` | the standard library's | The header bound |
| `404 Not Found` | `not found` | Anything else |

A delivery whose every bound playbook refuses its values is still `202`: the refusal happens after
the answer, a retry cannot fix a declaration, and the refusal is in the record for the operator who
can.

## What the answer promises

**`202` means the delivery is in the record store and survives the process being killed the moment
the answer leaves** (FR-312). The store is opened in WAL mode with `synchronous=FULL`, and research.md
§5 killed a process after commit twenty times without losing a row. It does not promise survival of
a lost machine: SQLite's own documentation makes that depend on the disk honouring the flush it is
asked for, which this repository cannot test (research.md §9). A deployment whose storage lies about
flushes can lose an accepted delivery on power loss.

**`503` after step 7 does not mean nothing was written.** The acceptance runs on a context detached
from the request, so the write may land after the answer; if it does, its hand-offs run, and the
sender's retry is then a repeat (FR-314). What `503` does guarantee is that nothing was accepted *by
that answer*, and a retry is safe.

**Accepted hand-offs are each decided once by the process that recorded them.** A hand-off is
decided when a run of it starts, or when a durable refusal of it is recorded — by the guard, at
arrival or ending a wait (`wait_expired`, `playbook_changed`, `backend_unavailable`, `rate_limited`),
or for one of its values. A hand-off the guard accepted into its waiting slot is not decided yet: the
wait lives in the accepting process and dies with it (requirement 113 of
[the guard's specification](../../002-guard/spec.md)). A process that dies with a hand-off
undecided — never handed to the guard, or waiting under it — never runs it later: the delivery is
marked dropped, the guard's drop record names it for a wait, and the next retry of its identity
inside the window is accepted as new and handed to the playbooks no earlier attempt decided
(FR-315). A retry of a delivery whose wait ended in a durable refusal is a repeat: the refusal
stands.

**A rate-limited delivery is discarded, not deferred.** The guard's rate limit discards the trigger
it refuses rather than making it wait (requirement 116 of the guard's specification), and a webhook
trigger is no exception: at arrival, or when a waiting one is judged again before it runs, the
refusal is recorded naming the delivery and the hand-off is decided. Nothing holds the delivery to
try later, and a retry inside the window is a repeat. The same event sent after the window is a new
delivery and meets the limit again.

**Who notices a dead process.** The drop is found by reading the dead process's instance lock — the
guard's — which the kernel releases however the process died. That reading happens when `serve`
starts, before either listener opens; when `gronin deliveries` reads; and inside the acceptance of a
retry whose identity points at the dead process's delivery. So when one of two `serve` processes on a
state directory dies and the other stays up, a retry reaching the survivor is judged new and runs
without waiting for anything to restart. Nothing sweeps on a timer: until one of the three happens,
the dead process's deliveries keep their last state in the table, nothing runs from them, and every
reader reconciles before it shows them. The lock is a file in the state directory, so this reach is
one host's, like repeat detection's (FR-319).

## The acceptance, and how a test of it fails

Each clause names what breaks it. A mutant of each is declared in `runtime/testdata/mutations.json`
with the test that kills it, per [tasks.md](../tasks.md).

**A1 — The answer waits for the write.** No `202` leaves before the transaction of step 7 commits.
*Fails when*: the handler answers first and writes after. The test holds the store's write lock and
asserts no answer arrives while it is held.

**A2 — The bound is below the write limit.** The durable step's bound plus the time to read the
request fits inside the listener's answer-write limit, so a refusal past the bound reaches the
sender (FR-313, research.md §3). *Fails when*: the write limit is lowered below the sum, which a test
of the constants catches, and the answer then vanishes while the handler believes it answered.

**A3 — The hand-off follows the record.** When the write lands after its `503`, the delivery's
hand-offs run. *Fails when*: the hand-off is started from the answer path, which produces no run for a
late write.

**A4 — One identity, one acceptance.** Two acceptances of one identity inside its window produce one
delivery and one set of hand-offs, across processes sharing the state directory. *Fails when*: the
identity is read outside the write transaction, which a forced interleaving — the second issued while
the first holds its transaction open — catches and a race does not.

**A5 — The window is the runtime's.** Newness is judged from `accepted_at` on the runtime clock's wall
reading, and an acceptance exactly one window old is new. *Fails when*: a timestamp from the body or a
header takes part, or the comparison is strict.

**A6 — A dropped delivery's retry is new, once.** *Fails when*: the dropped state is not consulted, so
the retry is a repeat and nothing ever runs; or the retry's hand-offs include playbooks the dropped
delivery had already reached.

**A7 — A wait is not a decision.** A delivery whose hand-off was waiting under the guard when its
process died is dropped, and its retry inside the window runs; one whose wait ended in a durable
refusal is decided, and its retry is a repeat. *Fails when*: the reconciliation counts an accepted
wait as decided, so the retry is a repeat and the event never runs; or the acceptance of a retry
reaching a surviving process does not read the dead process's instance lock, with the same result
until something restarts.

## Bounds

The values are the plan's (*Constraints*), decided on 2026-09-10 with their reasons. Tests set them
to milliseconds and bytes through the ingress's options; the defaults are asserted where A2 is.

| Bound | Enforced by |
| ----- | ----------- |
| Body | the declared length, then a byte-capped reader |
| Headers | the server's header size limit |
| Time to headers | the server's header read timeout |
| Time to the whole request | the server's read timeout |
| Answer write | the server's write timeout |
| Durable step | the handler's own timer around step 7 |
| Deliveries in progress | a slot taken without waiting at step 2 |
| Unauthenticated refusal interval | the counting table's key |
