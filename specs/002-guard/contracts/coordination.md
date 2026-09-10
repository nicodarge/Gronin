# Contract — Coordination

What the guard asks of whatever holds its claims. Three implementations are held to it:

- **the fake**, in memory, which the guard's own tests run against;
- **the etcd adapter**, which a deployment configures;
- **the file lock** the runtime core already ships, for a deployment that configures no backend
  (FR-109).

One contract suite runs every clause against every implementation it applies to. Its etcd run talks
to a server embedded in the test process over a unix socket, which [research.md](../research.md)
§1 establishes is not network. A clause that the fake satisfies and etcd does not therefore fails
inside the gate, rather than in a deployment.

Each clause states how a test of it fails. Every guarantee here is a non-event, and a test asserting
one passes against a subject that did nothing; the mutant named is what shows the test can tell the
difference, and it goes into `runtime/testdata/mutations.json` with the test.

## The interface

```go
package guard

// Coordinator holds claims on playbook names for one deployment.
type Coordinator interface {
    // Acquire takes the claim on req.Name, and one of its rate slots when req.Rate is set,
    // together or not at all. It returns within ctx's deadline.
    Acquire(ctx context.Context, req AcquireRequest) (Claim, error)

    // Released returns once the claim on name has been observed free, or when ctx ends. It
    // may return early; the caller acquires again rather than trusting it.
    Released(ctx context.Context, name string) error

    // Reach is "cross-host" or "single-host", printed by `serve` at startup (FR-109).
    Reach() string
}

type AcquireRequest struct {
    Name    string
    Holder  Holder        // host, process instance, run identifier: what a refusal names
    Expiry  time.Duration // whole seconds
    Rate    *RateLimit    // nil when the playbook declares none
    Trigger TriggerRef    // kind, and for a scheduled trigger the instant it was due
}

type Claim interface {
    Token() int64                   // the fencing token
    Renew(ctx context.Context) error // one attempt, bounded by ctx
    Fence(ctx context.Context) error // is this still the current claim on its name?
    Release(ctx context.Context) error
}

type Holder struct{ Host, Instance, RunID string }

type RateLimit struct {
    Runs int
    Per  time.Duration
}

type TriggerRef struct {
    Kind  string    // "schedule" or "manual"
    DueAt time.Time // the instant the schedule computed; zero for any other kind
}

var (
    ErrHeld        = errors.New("claim held")        // wrapped with the holder
    ErrRateLimited = errors.New("rate limit reached") // wrapped with the limit
    ErrUnavailable = errors.New("backend unavailable")
    ErrLost        = errors.New("claim lost")
)
```

`Trigger` carries the scheduled instant so that the second finding in
[research.md](../research.md) — the backend refusing a scheduled occurrence that already ran —
can be adopted inside an adapter without changing this interface. No clause below uses it yet.

## Clauses

| # | Clause | Fake | etcd | File lock |
| - | ------ | ---- | ---- | --------- |
| C1 | Exclusion | yes | yes | yes |
| C2 | Expiry is the backend's | yes | yes | by process death |
| C3 | No host clock judges a claim | yes | yes | n/a |
| C4 | Every call is bounded | yes | yes | yes |
| C5 | Loss is definitive | yes | yes | n/a |
| C6 | Fencing | yes | yes | n/a |
| C7 | Release | yes | yes | yes |
| C8 | The rate slot and the claim are one step | yes | yes | yes, against the record store |
| C9 | The rate window is the backend's | yes | yes | host clock |
| C10 | Released | yes | yes | yes |
| C11 | Unavailable is not held | yes | yes | yes |
| C12 | The granted expiry | yes | yes | n/a |

**C1 — Exclusion.** While a claim on a name is held, `Acquire` for that name by anyone else returns
`ErrHeld`, naming the holder. A different name is not affected.
*Fails when*: the adapter's transaction stops comparing the key's creation revision with zero, or
the fake stops consulting its map. The second acquisition then succeeds.

**C2 — Expiry is the backend's.** Two halves:

- A claim whose holder keeps renewing is never acquirable by another caller.
- A claim whose holder stops renewing becomes acquirable within its expiry plus a stated slack
  after the last renewal.

The file lock's equivalent is the holder's death: the kernel releases the lock, and
`TestALockHeldByAKilledProcessIsAcquirable` already proves it.
*Fails when*: the claim key is written without its lease, so it never lapses; or the expiry is sent
in the wrong unit, so it lapses a thousand times late. Both break the second half. A claim that
lapses while renewed breaks the first. The test waits with a deadline and polls; it never sleeps a
fixed time and then asserts, because a sleep shorter than the expiry passes without the recovery
ever happening (the SC-102 trap the plan names).

**C3 — No host clock judges a claim.** No timestamp sent to the backend or read back from it takes
part in deciding whether a claim is held. The holder's "when taken" is written for a reader, and
nothing compares it with anything.
*Fails when*: in the fake — the implementation where the runtime's clock and the backend's are
separate and both injectable — the runtime's clock is moved forward by hours while the fake's is
not. A claim another holder still renews must stay held. The mutant makes the fake's expiry read the
runtime's clock.

**C4 — Every call is bounded.** `Acquire`, `Renew`, `Fence`, `Release` and `Released` return by
their context's deadline, whether the backend refuses, is severed, or holds its responses.
*Fails when*: a call uses a context of its own instead of the one passed in. The test holds the
backend — the fake's hold switch; for etcd, a proxy that keeps the connection open and forwards
nothing — and asserts from a watchdog of its own that the call returned within the deadline plus
250 ms. It does not rely on `go test`'s own timeout, which would take ten minutes to kill each
mutant. Severing is tested separately and is not a substitute:
a severed fake fails at once, so it would pass an unbounded call (SC-112).

**C5 — Loss is definitive.** `Renew` or `Fence` on a claim that has lapsed or been superseded returns
`ErrLost`, never a retryable error. For etcd this is `requested lease not found` from `Renew`, and a
failed comparison from `Fence`.
*Fails when*: the adapter maps a missing lease to `ErrUnavailable`. The runtime would then keep
retrying until its own deadline instead of stopping at once.

**C6 — Fencing.** A claim's token is greater than that of every earlier claim on the same name.
`Fence` succeeds only while the claim holding that token is the current one.
*Fails when*: `Fence` checks that the key exists rather than that its creation revision equals the
token. The test is A acquires, A's claim lapses, B acquires. Then B's token must exceed A's, A's
fence must return `ErrLost`, and B's must succeed; the mutant lets A's fence pass.

**C7 — Release.** `Release` makes the claim acquirable at once, and releasing twice is not an error.
Safety never depends on a release happening — expiry does that.
*Fails when*: `Release` does nothing. The next acquisition is then refused until the claim lapses,
and the test gives it far less time than the expiry.

**C8 — The rate slot and the claim are one step.** When a rate limit is set, `Acquire` takes the
claim only if it also takes a free slot, in the same transaction. A refusal for rate leaves the claim
exactly as it was (FR-115).
*Fails when*: the adapter takes the claim first and checks the slots afterwards. The test fills
every slot, calls `Acquire`, and then checks that a second contender can still take the claim with
the rate limit lifted; under the mutant the claim is left held by the refused call.

**C9 — The rate window is the backend's.** A slot frees once `per` has passed since the run that took
it started, judged the way C2 judges expiry. Nothing frees it early: a released claim does not
release its slot.
*Fails when*: `Release` also deletes the slot, which turns a limit on runs into a limit on
concurrent runs. The test takes N runs, releases each claim, and asserts the next acquisition is
refused for rate.

**C10 — Released.** `Released` returns once the claim it watches is released or lapses, well before
its context ends.
*Fails when*: it only ever returns at the context's deadline. A waiting trigger would then start
late by exactly its whole waiting expiry. The test releases the claim and asserts that `Released`
returned less than half its deadline later.

**C11 — Unavailable is not held.** A backend that cannot be reached, or does not answer in time, is
`ErrUnavailable`, never `ErrHeld`.
*Fails when*: a timeout is classified as contention. A trigger would then wait on a claim nobody
holds instead of being refused with the backend named (FR-107). For the file lock this is the
existing mutant `a filesystem failure is reported as a concurrent run`, which the contract suite
inherits rather than duplicates.

**C12 — The granted expiry.** An `Acquire` granted a shorter expiry than it asked for fails with
`ErrUnavailable` and revokes what it was granted. A longer one is accepted, and the claim reports
the expiry actually granted.
*Fails when*: the grant's reply is not compared with the request. The test runs against the fake
told to grant less than asked.

## The holder's side

Obligations on the runtime rather than on an implementation, tested against the fake with the
runtime's clock and timers injected. The durations are in [research.md](../research.md) §3.

**R1 — The stop deadline.** The runtime records `sent`, the instant each renewal that later succeeds
was sent — for a new claim, the instant the grant was sent. It decides to stop the run at
`sent + expiry − stop bound − margin floor` if no later renewal has succeeded by then.
*Fails when*: `sent` is taken from when the reply arrived, which moves the deadline later by a round
trip; or the deadline omits the stop bound. The test holds every renewal and asserts the stop
decision at the computed instant on the injected clock, to the tick.

**R2 — Loss stops the run at once.** `ErrLost` from `Renew` or `Fence` stops the run without waiting
for the deadline.
*Fails when*: `ErrLost` is treated as one more failed attempt.

**R3 — A fence before every side effect.** The runtime fences before the agent stage starts and
before each sink delivers. A fence that fails for any reason stops the run and the side effect does
not happen. For the file lock a fence is a no-op, because the lock cannot be lost while its holder
lives.
*Fails when*: the sink loop does not fence. The test expires the claim between the agent stage and
the first sink, and asserts the sink's endpoint received nothing.

**R4 — The stop bound is enforced.** From the stop decision the run's context is cancelled, which
kills the child process group as `proc.Isolate` already does. If the run is not over within the stop
bound, the runtime process exits. Cancelling a context cannot end work that ignores it, and exiting
is the only thing left that can.
*Fails when*: the exit is removed. The test (SC-115) runs `gronin` as a child process with a
sink that ignores its context, and asserts the process is gone within the stop bound plus slack. A
run that stops when asked would pass without the enforcement ever executing, which is why the
subject is built not to.

**R5 — Configuration that cannot hold is refused.** At startup the runtime refuses durations where
`renewal interval + renewal bound + stop bound + margin floor > claim expiry`. It also refuses a
renewal bound not below the renewal interval, and a claim expiry that is not a whole number of
seconds.
*Fails when*: the comparison is inverted or its stop-bound term dropped. The test must include a
configuration refused *only* because of the stop bound; one refused on the other terms alone
passes the mutant (SC-112). The refusal names every duration and the expiry they exceed.

## Keys in the backend

Under a prefix the deployment configures, so two deployments can share one etcd without sharing
claims:

| Key | Value | Lease |
| --- | ----- | ----- |
| `<prefix>/claims/<name>` | the holder, as JSON: host, process instance, run identifier, when taken | the claim's, renewed by the holder |
| `<prefix>/rate/<name>/<i>`, `0 ≤ i < runs` | the run identifier that took the slot | one of its own, of `per`, never renewed |

The token is the creation revision of the claim key, read from the reply to the transaction that
created it.
