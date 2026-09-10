# Phase 0 — Research

Four questions from the plan, and two findings that reach back into the specification. Every
measurement below was produced on 2026-09-10 by running a throwaway program against the component
named, not by reading its documentation; where a statement rests on reading source or
documentation instead, it says so. Nothing here was run against a multi-host deployment: every
experiment ran on one machine, most of them inside `scripts/no-network.sh`.

What was run:

| Component | Version | How it was run |
| --------- | ------- | -------------- |
| Go | 1.27.1 | |
| etcd client and embedded server | `go.etcd.io/etcd/client/v3`, `server/v3` v3.7.1 | Server embedded in a probe binary, unix sockets only, inside `scripts/no-network.sh` |
| Consul | agent 2.0.4 (`hashicorp/consul` image), `consul/api` v1.34.5 | `consul agent -dev` in a container on loopback |
| Redis | server 8.10.1 (`redis` image), `go-redis/v9` v9.22.0 | Container on loopback |
| PostgreSQL | 17.11 (`postgres:17` image), `pgx/v5` v5.11.0 | Container on loopback |

The shared experiment for every candidate: one process takes a claim with a 5 second expiry and
renews it every second; it is frozen with `kill -STOP`; a second process polls for the claim and
reports how long after the freeze it got it; the first is resumed with `kill -CONT` and reports
what its next renewal said.

## 1. What proves the cross-host guarantee in a hermetic suite

### What counts as network

Principle VI says the suite MUST pass "with no network reachable", and gives the reason: a test
that reaches the network "passes on the machine that has the access and fails on the machine that
reviews the change". The rule is against a test whose outcome depends on the machine, and
`scripts/no-network.sh` is how the repository already enforces it: a namespace holding loopback
and nothing else, with loopback deliberately brought up because the suite binds it.
`httptest.NewServer` servers on loopback are already how the sink, API and run tests reach their
subjects.

**Decision**: a test may connect to an endpoint that the test process itself, or a child it
started from this module, created during the test — a loopback address on an ephemeral port, or a
unix socket under the test's temporary directory. Both exist identically on every machine that
runs the suite, so neither makes a test conditional. Anything else counts as network, including
two things that never leave the machine: a fixed port, which makes the outcome depend on what else
is running, and a server binary looked up on `PATH` or started through a container runtime, which
makes the outcome depend on what is installed.

### The candidates, and the blind spot of each

**An in-process coordination interface with an injectable fake.** The fake holds claims in memory,
judges expiry on its own clock rather than the runtime's, and can be told to sever, to hold every
response, or to expire a claim. Every interleaving is chosen by the test rather than hoped for,
which is what Principle VI's determinism requires of a mechanism that is all races and clocks.
**Blind spot**: the fake is the runtime's belief about the backend. A test against it proves the
runtime is correct against that belief and nothing about the backend — which is the failure the
runtime core's walkthrough found four times (issue #16).

**A real backend embedded in the test process.** The etcd server is a Go library.
Measured, 2026-09-10, all inside `scripts/no-network.sh`:

- Configured with unix-socket URLs for both its client and peer listeners, it opened no TCP
  listener: `/proc/net/tcp` and `/proc/net/tcp6` inside the namespace listed zero sockets in
  `LISTEN` state.
- Ready in 233 ms, 337 ms, 434 ms, 531 ms, 533 ms, 642 ms, 731 ms, 829 ms and 932 ms across nine
  starts.
- A race-enabled test binary holding one test that starts the server and takes a claim was
  45,236,770 bytes, against 4,652,473 for a test package with no dependencies. Three runs of that
  test binary took 1.45 s, 1.64 s and 2.03 s of wall time, namespace setup included.
- Compiling every package's tests in a freshly copied tree, as the mutation harness does for each
  mutant, with a warm build cache: 2.41 s without the server in the module and 3.95 s with it; 5.77 s
  and 6.84 s with `-race`.
- A unix-socket proxy between client and server that stops forwarding without closing — a backend
  holding its responses — made a renewal bounded at one second return `context deadline exceeded`
  after 1.001 s. The test holding it took 7.7 s in total; the remainder was the embedded server
  shutting down with the held connection still open, and was not investigated further.

**Blind spot**: one member, one kernel. It says nothing about a cluster losing its leader, and
nothing about a partition between two real hosts. Its timings are real seconds, so a test against
it can only assert an upper bound with slack, never an exact instant.

**A contract test the real client is separately held to.** One set of cases stating what claim,
renew, expire and fence mean. **Blind spot, as the plan names it**: run only against the fake, it
proves the runtime uses the interface correctly and proves nothing about whether the backend
behaves as the interface assumes.

### Decision

All three, because each covers the blind spot of another, and one piece stays outside the suite
because nothing hermetic can cover it.

1. **The guard's own logic runs against the fake.** Every success criterion about what the guard
   decides — waiting, the rate limit, refusal records, the stop decision, the time bounds — is
   tested here, with the fake holding, severing or expiring on command and the runtime's clock and
   timers injected.
2. **One contract suite runs against the fake, against the etcd adapter talking to an embedded
   server, and against the single-host file lock where a clause applies to it.** This is what
   makes the fake honest: a clause the fake satisfies and etcd does not fails in the etcd run,
   inside the gate. The clauses and how each can fail are in
   [contracts/coordination.md](./contracts/coordination.md).
3. **One test drives two built `gronin` processes against one embedded server** started by the
   test, each process with its **own** state directory. Sharing one would let the existing file
   lock produce the right answer with the backend disconnected, so a mutant that stops the guard
   from consulting the backend would survive. The effect counted is SC-101's: a line appended by
   the stub agent, exactly once.
4. **Outside the suite**: the two-container walkthrough in [quickstart.md](./quickstart.md), against
   an etcd that is not embedded. It is where "hosts" first means separate kernels. It is a
   validation, not a gate.

What none of these covers: a multi-member cluster's behaviour inside the suite (its failover was
measured in three runs, below, and is not re-tested by the gate), and a holder whose host is suspended (see
question 3).

## 2. Which backend, and what it has to guarantee

### What was required

- **Expiry judged by the backend** (FR-106). Anything whose expiry is a client comparison is out.
- **The holder learns in time** (FR-105). It learns it has lost the claim in time to stop. In a
  partition the backend can tell the holder nothing, so this is two properties: every client call
  is bounded by a deadline the runtime sets, whatever the failure looks like, and a claim that has
  already lapsed is reported as lost rather than as a failure worth retrying.
- **No cgo**, checked with the repository's own `scripts/check-static.sh`.
- A fencing token, so that a holder can check, before a side effect, that the claim it holds is
  still the current one.
- A claim that survives the backend's own failover. A backend that forgets claims when it fails
  over lets a contender in immediately, while the old holder is still inside its own deadline.

### Results

**etcd.**

- *No cgo.* The client probe, the embedded-server probe, and a `gronin` binary linking the client
  all passed `scripts/check-static.sh` ("ok (linux, CGO_ENABLED=0)"). Linking the client grew
  `gronin` from 19,672,598 to 31,186,644 bytes. Adding the client and the embedded server to the
  module took `go list -m all` from 38 modules to 144.
- *Expiry judged by the server.* Holder frozen, 5 s expiry: the contender acquired 4.999 s after
  the freeze when the holder renewed every second with `KeepAliveOnce`, and 3.813 s after it when
  the holder used the library's `KeepAlive` stream, which renews every third of the expiry.
  Reading the wire format agrees: `LeaseGrantRequest` carries a TTL in seconds and
  `LeaseKeepAliveRequest` carries only a lease ID — no client timestamp reaches the server (read
  from `etcdserverpb/rpc.proto`).
- *Failover.* Three members, the holder frozen and the leader killed with `SIGKILL` at the same
  instant, 5 s expiry: the contender acquired 7.965 s, 7.165 s and 7.432 s later in three runs. The
  claim outlived the leader and was not dropped; it lasted longer than its expiry, which is the
  safe direction.
- *How the holder learns.* This is where the library's defaults are not enough. With the server
  frozen, so that every response is held, the `KeepAlive` stream closed its channel 5.001 s after
  the last response it delivered — at the claim's nominal expiry, not before it. The library
  checks that deadline on a one-second timer (read from `lessor.deadlineLoop` in `lease.go`), so
  its signal arrives at the expiry or up to a second after it, and a run told then cannot stop
  before the claim lapses. `concurrency.Session` is built on the same stream (read from
  `session.go`). `KeepAliveOnce` under a one-second context returned `context deadline exceeded`
  at 1.000 s to 1.001 s on every attempt — with the server frozen, through the holding proxy
  above, and with the server killed outright. A killed server does not fail fast: the client's
  retry interceptor retries until the context ends, so a severed backend costs the full bound as
  well. An acquisition attempted with nothing listening, under a two-second bound, returned at
  2.001 s. A holder resumed after its claim lapsed got `etcdserver: requested lease not found` on
  its next renewal at once: a definitive loss, distinguishable from a failed attempt.
- *Fencing.* The revision at which the claim key was created is the token. A transaction
  comparing the key's creation revision to the token succeeded while the claim was held (token
  2), and failed once a contender had taken it over (the contender's token was 4).
- *Minimum expiry.* Asked for 1 s the server granted 2 s; asked for 2 s and 3 s it granted what
  was asked. The minimum follows from the server's election timeout, whose default is 1000 ms
  (read from `embed/config.go`); the client cannot know a remote server's value, so the adapter
  compares what was granted with what was asked.

**Consul.**

- The client passed `scripts/check-static.sh`.
- Holder frozen, 10 s session TTL: the contender acquired **19.56 s** after the freeze (one run).
- A TTL below 10 s is refused by the server: `Invalid Session TTL '1000000000', must be between
  [10s=24h0m0s]`.
- The resumed holder's renewal returned no session.
- The key's `LockIndex` restarted at 1 for the new holder, because a session invalidated with
  `Behavior: delete` deletes the key; `ModifyIndex` rose (19, then 586) and is the usable token.
- Embedding: `consul/sdk/testutil` v0.18.2 starts the `consul` executable it finds on `PATH` (read
  from `testutil/server.go`), which the definition above counts as network.

**Redis.**

- The client passed `scripts/check-static.sh`.
- Holder frozen, 5 s expiry set with `SET NX PX`: the contender acquired 4.51 s after the freeze.
- The resumed holder's compare-and-extend script returned 0, which is a usable loss signal.
- The reply to `SET NX` carries no token; fencing would need a second key incremented in the same
  script.
- With the server paused, a renewal under a **one-second** context returned after about **5 s**.
  `go-redis` ignores context deadlines unless `ContextTimeoutEnabled` is set, and it defaults to
  false (`options.go`, v9.22.0) — FR-122's bound is silently not applied by default.
- The embeddable server, `miniredis` v2.39.0, is a reimplementation whose TTLs "don't decrease
  automatically" (its README); it is the stub problem again, not a way out of it.

**PostgreSQL advisory locks.**

- The client passed `scripts/check-static.sh`.
- With the defaults, a frozen holder kept its lock: the contender had still not acquired after
  **20.047 s**. The holder's kernel keeps its TCP connection alive while the process is stopped, so
  nothing on the server ever expires.
- With `idle_session_timeout` set to 5 s by the holder on its own session, the contender acquired
  4.512 s after the freeze, and the resumed holder got `FATAL: terminating connection due to
  idle-session timeout (SQLSTATE 57P05)`. So a server-judged expiry exists — but it is a setting
  the holder applies to itself, and nothing on the server enforces that it did.
- No token.
- Not run here: advisory locks live in the primary's memory and are not replicated, so a failover
  forgets every claim; a transaction-mode connection pooler breaks session-scoped locks.

### Decision: etcd

Chosen because it is the only candidate that met every requirement above when run:

- Expiry judged by the server.
- A claim that survived the loss of the leader.
- A token that is part of the claim rather than bolted beside it.
- A client that honours a context deadline on every failure measured.
- A server that embeds in the test process over unix sockets, inside the no-network namespace,
  with no cgo.

**What choosing it obliges the runtime to do, from the results above:**

- **Its own renewal loop, not the library's.** One `KeepAliveOnce` per attempt, under FR-122's
  bound. The `KeepAlive` stream and `concurrency.Session` signal at the expiry, which leaves FR-105
  nothing to act in.
- **A stop decision made on the holder's own clock.** It is anchored on when the last successful
  renewal was *sent*, because the server's countdown starts no earlier than that — question 3
  states the arithmetic. It is not a client-side judgement of expiry in FR-106's sense: it never
  decides that a claim is free, only that this holder can no longer prove it holds one.
- **`requested lease not found` treated as definitive loss**, and every other error as a failed
  attempt.
- **A comparison of the granted expiry with the requested one on every grant**, refusing a shorter
  one.

**Rejected:**

- **Consul**: invalidation measured at nearly twice the TTL, a ten-second floor on the TTL, and no
  way to run the server in the test process.
- **Redis**: no token, a client that ignored the renewal bound by default, and an embeddable
  server whose expiry does not run on its own.
- **PostgreSQL**: a lock that does not expire unless the holder asks for its own connection to be
  cut, no token, and a failover that forgets every claim. It is the backend most likely to exist
  beside a deployment already, which does not make up for a lock that never expires.

**The cost the plan already names.** A deployment wanting the cross-host guarantee
runs etcd beside the binary — one member is enough for the guarantee, three for it to survive a
member's loss. A deployment whose etcd is down gets no runs at all rather than a quieter guarantee,
by FR-107.

## 3. The durations

Chosen thresholds, 2026-09-10. None of them was derived from a measurement of this runtime, which
does not exist yet; each reason says what it is chosen against.

### The claim set, bound together by FR-123

| Duration | Default | Why this value |
| -------- | ------- | -------------- |
| Claim expiry (FR-104) | 30 s | How long a killed holder blocks its playbook (SC-102), for playbooks whose runs take minutes. It has to hold the other three plus the margin, and it is a whole number of seconds, the unit of the server's TTL |
| Renewal interval (FR-103) | 5 s | Leaves room for two failed attempts inside the defaults, as worked through below |
| Renewal bound (FR-122) | 4 s | A healthy renewal is one round trip. The bound gives an attempt time to ride out a leader election (the server's default election timeout is 1 s) and treats anything longer as a hang. It is below the interval, so at most one attempt is ever in flight |
| Stop bound (FR-126) | 10 s | Must exceed `proc.WaitDelay` (5 s), which is how long the runtime already waits on a killed child's pipes before giving up on it. The rest is for a sink's in-flight request to observe its cancelled context |
| Margin floor | 2 s | Covers the holder acting late on its own deadline — a timer firing late on a starved host. It does not cover a host suspended outright, below |

**The inequality.** The runtime refuses to start when

```text
renewal interval + renewal bound + stop bound + margin floor > claim expiry
```

or when the renewal bound is not below the renewal interval, or when the claim expiry is not a
whole number of seconds. The refusal names each duration and the expiry they exceed (US1
acceptance scenario 6). The defaults give 5 + 4 + 10 + 2 = 21 s against 30 s.

**The stop deadline this protects.** Let `sent` be the holder's clock reading when the last renewal
that later succeeded was sent — for a new claim, when the grant was sent. The server's countdown
began no earlier, so the claim is held at least until `sent + expiry`. The holder decides to stop
at

```text
deadline = sent + claim expiry − stop bound − margin floor
```

and the run is over by `deadline + stop bound`, which is at least the margin floor before the
claim can lapse. With the defaults the deadline falls 18 s after `sent`. Attempts start 5 s, 10 s
and 15 s after it, and each ends by 4 s later, at the latest — at 9 s, 14 s and 19 s. So two
consecutive failed attempts are absorbed, and a third that has not succeeded by the 18 s deadline
stops the run. The inequality above is exactly the condition that an attempt using its whole
bound still ends before the deadline; without it, a renewal that succeeds slowly would stop a
healthy run.

**What the margin does not cover.** The holder's clock is Go's monotonic clock, which on Linux does
not advance while the host is suspended (a property of `CLOCK_MONOTONIC`, read, not run). A holder
on a host that sleeps for longer than the margin resumes believing it still holds a claim that has
lapsed. The fence before each side effect is what catches that — its token no longer matches — and
the window left is a suspend that falls between a fence and the side effect it admitted. No
duration closes that window; only a destination that checked the token could, and a chat webhook
or an issue tracker does not.

### The other two, outside the margin

| Duration | Default | Why this value |
| -------- | ------- | -------------- |
| Decision bound (FR-108) | 5 s | The decision is a grant, a read of the rate slots and one transaction: the renewal bound plus one more round trip. Exceeding it refuses the trigger, naming the backend |
| Waiting expiry (FR-112) | 30 m, declared per playbook as `guard.wait` | Matches the agent stage's default timeout (`agent.timeout`, 30 m), so a waiting trigger does not expire merely because the run ahead of it used its declared budget. A playbook whose runs are shorter, or whose events go stale faster, declares less |

A decision that exceeds its bound may still have taken the claim — the transaction can commit after
the client stopped listening. The lease it was granted is known before the transaction is sent, so
it is revoked on the way out, and if the revocation fails too it expires within the claim expiry.
The cost is that playbook being blocked for at most that long, and it is recorded as a refusal naming
the backend.

## 4. A playbook renamed mid-run

**Decision.**

- **A claim is released through its lease, never by looking the name up again.** A rename during a
  run therefore cannot leave a claim nobody releases. The run that took the claim under the old
  name holds it, renews it and releases it under that name, and if its process dies the claim
  expires like any other.
- **Across a rename, the old name and the new are two claims.** The name is the identity, as the
  specification states, so a process still running the old name and one that has loaded the new
  one can run the same file concurrently, and the rate window starts empty under the new name.
  `serve` loads its playbooks once, at start, so within one process the rename takes effect at
  the next start; across hosts, a rolling restart overlaps the two names. This is stated in
  [quickstart.md](./quickstart.md) as what a rename costs, rather than engineered around: the
  alternatives are an identity that is not the name — there is none that is stable across hosts
  — or a declared list of former names, which is a feature for a case an operator can avoid by
  letting the old name's runs finish before restarting.
- **A waiting trigger does not follow a rename.** FR-121 has a waiting trigger re-read its playbook
  when it finally runs, from the file it was accepted from. If that file is gone, no longer
  declares the name the trigger was accepted under, or is refused by the load gate, the trigger is
  discarded and a refusal recorded with mechanism `playbook_changed`, naming what the file now
  declares. It collided with the old name's claim and was judged against the old name's window;
  running it under the new name would skip both.

## Findings that reach back into the specification

Neither is a Phase 0 question. Both surfaced while writing the data model, and both need the
owner's decision rather than a plan's.

### FR-110 contradicts User Story 1 on a deployment of two hosts

Two hosts carry the same cron playbook. Its tick fires on both; host A takes the claim; host B's
trigger is refused because the playbook is already running. FR-110 says that trigger MUST wait and
then run once — so the tick runs twice, one after the other, which is exactly what User Story 1
says the feature exists to prevent. US1 acceptance scenario 1 says the other process "records a
refusal naming the lock"; FR-117 says a trigger that waits and then runs "MUST NOT be recorded as"
a refusal; SC-101 says exactly one run.

User Story 2's own reasoning already draws the line: discarding a colliding trigger is "defensible
for a cron playbook, whose next tick comes anyway, and indefensible for a trigger that will not
come again". Its independent test uses a manual invocation.

**Recommended, and what the Phase 1 design follows**: FR-110 applies to a trigger that does not
recur — a manual invocation now, a webhook delivery later. A scheduled trigger refused because its
playbook is already running is refused and recorded with mechanism `claim_held`, as the runtime
core does today. If the owner decides otherwise, the design changes in one place: which trigger
kinds enter the waiting slot.

### A short run can let one tick run twice, one host after the other

FR-101 forbids two runs *at once*. Hosts whose clocks differ by more than a run takes — or a run
that is refused at its gather stage in milliseconds — let the later host's tick find the claim
already released and run the same occurrence again. The specification puts deduplication out of
scope because "a cron tick has none" of the event identity it needs. A scheduled occurrence does
have one, though: the playbook name and the instant the schedule computed. That instant is derived
from the expression, not from a payload, so FR-118 does not forbid comparing it.

**Recommended, not adopted**: the backend records the last scheduled instant a run started for, in
the same transaction that takes the claim, and refuses a scheduled trigger whose instant is not
later. The coordination interface already carries the trigger's kind and instant so that adopting
this changes an adapter and not the interface. Until the owner decides, US1's "it executes once"
holds for runs longer than the hosts' clock offset and is not claimed beyond that.

## What changed in the plan because of this

- The backend is etcd. The runtime links its client and nothing else; the embedded server is a
  test dependency.
- The renewal loop is the runtime's own, with the stop deadline above. The library's keep-alive
  helpers are not used.
- The rate window moves into the backend when one is configured: a rate limit counted on one host
  would allow a deployment of two hosts twice the declared runs, which is FR-114's guarantee
  delivered at half its declared strength. Its shape is in
  [data-model.md](./data-model.md).
- Every refusal record, waiting trigger and run is written to the record store of the host that
  handled it. There is no cross-host view of refusals; each host reports its own.
- A run stopped under FR-105 ends with a status of its own, `claim_lost`, rather than `failed`.
- The runtime ends its own process when a run is not over within the stop bound, because
  cancelling a context cannot end work that ignores it, and a run still going when the claim
  lapses is FR-101 broken.
