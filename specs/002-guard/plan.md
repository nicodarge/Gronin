# Implementation Plan: Guard

**Branch**: `add_guard_spec` | **Date**: 2026-09-08 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/002-guard/spec.md`

## Summary

The stage that decides, before any command runs and any token is spent, whether a trigger becomes a
run (FR-102). Three mechanisms, one of which the runtime core already has in a narrower form: a
claim on the playbook that holds across the hosts of one deployment (FR-101), a rate limit keyed on
the playbook name (FR-114), and a single waiting slot so that a refused trigger is deferred rather
than lost (FR-110).

Two clarifications set the technical shape, and both cost something the plan has to be honest
about.

**The guarantee crosses hosts, so a deployment gains a dependency.** The runtime core's advisory
file lock is a single-host mechanism, and was shipped saying so. Widening the guarantee means an
authority outside both processes, which means something to run beside a product that until now
installed as one static binary and nothing else. The binary itself is unchanged — still one file,
still no cgo — but "install this" stops being the whole of the deployment story for anyone who
wants FR-101 to mean what it says. FR-109 keeps the old reach available for anyone who does not.

The three mechanisms are ordered, and the order is load-bearing rather than incidental. The rate
limit is evaluated first (FR-115), because a trigger it refuses must never take a claim — taking
one would make another host wait on a run that was never going to happen. The claim is second. The
waiting slot is last, because there is nothing to wait for until a claim has been refused. A
trigger that waits resolves the playbook again when it finally runs (FR-121), so an edit made
during the wait is not silently skipped for one run — and it is judged against the rate limit again
at that moment (FR-124), because other triggers can fill the window while it waits and a limit that
bounds acceptances rather than runs is not the limit the operator declared.

**An expiring claim brings a failure mode a file lock does not have.** A file lock dies with the
process holding it; a lease does not. That gap is where a second run gets in, and closing it is
what FR-105 and FR-106 are for: the holder ends its own run when it cannot renew, and the backend
rather than either host judges when a claim has lapsed. Neither is optional, and together they are
the hardest part of this feature to test — see [research.md](./research.md) §1.

Saying those two must not hold at once does not make it so, which is why FR-122 and FR-123 exist. A
renewal that hangs is not a renewal that failed: it looks like one still in flight right up to the
moment the claim lapses, so the attempt needs its own time bound and exceeding it has to count as
failure. And the durations need a stated relationship, or a renewal interval equal to the expiry
satisfies every other requirement here while leaving no margin at all to act in. The runtime
refuses such a configuration rather than starting under it, which is what makes the relationship
checkable instead of aspirational (SC-112).

## Technical Context

**Language/Version**: Go, as the runtime core. No change.

**Primary Dependencies**: the etcd client, `go.etcd.io/etcd/client/v3`, chosen in
[research.md](./research.md) §2. The shipped binary links it and nothing else new. The etcd server,
`go.etcd.io/etcd/server/v3/embed`, is imported by tests only, so that the contract suite can run
against a real server inside the no-network namespace. Both are bound by the constraint that
already governs every dependency here, **no cgo**, and both were built with `CGO_ENABLED=0` and
passed `scripts/check-static.sh` on 2026-09-10.

**Storage**: the existing record store gains the refusal records of FR-117. Refusals are not runs
and must not be counted as runs, but they are read through the same operator surface (SC-108). Two
further writes: a waiting trigger is recorded when it is accepted (FR-127), which is what makes a
drop reconstructible after a kill, and the Run entity the runtime core defines gains the fact that
a run waited and for how long (FR-125) — a field its current description does not carry, so the
runtime core's own data model changes here rather than only this feature's. The backend holds the
claims and the rate windows and nothing else. The whole split is in [data-model.md](./data-model.md).

**Testing**: the existing suite, under the hermeticity and mutation obligations of Principle VI.
Three layers, chosen in [research.md](./research.md) §1:

- the guard's logic against an in-process fake;
- one contract suite run against the fake, the etcd adapter talking to an embedded server, and the
  file lock;
- one test driving two built binaries against an embedded server.

The clauses and their mutants are in [contracts/coordination.md](./contracts/coordination.md).

**Target Platform**: unchanged. The coordination backend is a deployment concern, not a build one.

**Project Type**: an added stage in an existing pipeline, plus one schema block that stops being
refused (FR-119).

**Performance Goals**: the guard's decision is bounded (FR-108), by default at 5 s, and exceeding it
refuses rather than passes. Every duration and its reason is in [research.md](./research.md) §3.

**Scale/Scope**: the deployments this targets run a handful of hosts for availability, not a fleet
for capacity. A coordination design that would not survive a hundred contending hosts is
acceptable; one that cannot survive two is not.

## Constitution Check

### I. Bounds Are Declared and Enforced — PASS, and FR-107 is where it bites

The `guard` block joins the declared bounds: validated at load, and refused when it names a key the
runtime does not implement (FR-119). That is the same treatment `guard` gets today, where the whole
block is refused; what changes is that some keys become implementable rather than that refusal
becomes acceptance.

The principle's sharper edge here is FR-107. A coordination backend that is configured and
unreachable must refuse the run, not fall back to the file lock. Falling back would deliver the
single-host guarantee under the cross-host name — a declared bound that nothing applies, which is
exactly what this principle refuses. FR-109 is the honest version of the same thing: a deployment
with no backend gets the narrower guarantee and is *told* that is what it has.

FR-120 is the other half. Non-concurrency is not something a playbook opts into by declaring a
block; a playbook that declares nothing is still held to it.

### II. The Agent Reports, the Runtime Acts — PASS, trivially

The guard runs before the agent stage and no agent output reaches it. Nothing here lets a report
influence whether a run happens.

### III. Every Run Is Inspectable — PASS, extended to runs that did not happen

FR-117 is this principle applied to the guard's own subject. A trigger that did not become a run
leaves no execution record today, so an operator asking "why did this not run last night" has
nothing to read. Every refusal names its playbook, its trigger, the mechanism that refused it and
when — and SC-108 requires that to be readable through the operator's existing surface rather than
only in a log.

FR-125 and SC-114 close the other half. A run that started long after its schedule is
indistinguishable from a late one unless it says it waited, and a trigger that waited and then ran
must not leave a refusal record behind — it was deferred, not refused, and FR-117 says so
explicitly rather than leaving the two entities to be conflated. SC-113 covers the clause of FR-113
that was otherwise measured by nothing: that an operator can *see* a waiting trigger was dropped
when the process was killed, which no absence-shaped criterion can establish — and which is why
FR-127 puts the record in at acceptance rather than on the way out.

FR-118 is the trap this repository has already been bitten by, stated as a requirement: anchor on
the runtime's own wall clock, never on a timestamp the trigger carried. It matters most for the
deduplication this feature does *not* specify, but the rate window (FR-114) and the waiting
trigger's expiry (FR-112) are both time comparisons, and both would be silently wrong against a
frozen payload timestamp.

### IV. Playbooks Are Portable Data — PASS, and it constrains the block's shape

The `guard` block must carry a limit and a window (FR-114), and a duration a trigger may wait
(FR-112). It must not carry a backend address, a credential or anything else naming one
deployment — those arrive through the deployment's configuration, which is what FR-107 and FR-109
are written against. A playbook declaring a rate limit stays portable to a deployment that
coordinates differently.

### V. Nothing From a Real Fleet Enters This Repository — PASS

No new surface for it. The backend is configured, never committed, and examples use
documentation-reserved values.

### VI. The Suite Is the Gate — the principle this feature is hardest against

Three obligations collide here, and Phase 0 exists mostly for this.

**Hermetic.** The suite must pass with no network reachable. A coordination backend is reached over
a network by definition, so the cross-host guarantee — SC-101, SC-103, SC-104 — cannot be proven by
talking to a deployed one in the suite. [research.md](./research.md) §1 answers it: a loopback or
unix socket to a server the test itself started is not network in Principle VI's sense, which lets
a real etcd server run inside the test process and hold the fake to the same contract.

**Deterministic.** Every mechanism in this feature is a race or a clock. A test that starts two
runs and asserts one wins is the archetype of a flake, and Principle VI forbids quarantining one.
The design has to make the interleavings injectable rather than hoped for.

**A test must be able to fail.** This is the real risk, and SC-111 states it as a criterion rather
than leaving it to discipline. Every guarantee here is that something does *not* happen, and a test
asserting a non-event is indistinguishable from a broken test until a mutant proves otherwise: a
test that asserts "no second run started" passes just as well when nothing started at all, when the
trigger never fired, and when the test's own body never executed. Each of SC-101, SC-102, SC-103,
SC-104, SC-105, SC-106, SC-107, SC-108, SC-109, SC-110, SC-112, SC-113, SC-114, SC-115 and SC-116
needs a
mutant, and the mutation harness has to report zero on an unmodified tree for any of their counts
to mean anything, SC-116 included.

Earlier drafts of this plan claimed one guarantee here escaped that — a time bound, it was argued,
fails loudly on its own, because something runs long and a test waiting on it times out. It does
not, and the reasoning is worth keeping because the mistake is an easy one. A bound is exercised
only by a subject that exceeds it. Against a backend that answers promptly, or a run that stops
when asked, an implementation enforcing no bound at all behaves identically to one that does, and
every test passes. This is why FR-122's renewal bound needed SC-112 built on a backend that holds
its response, why FR-126's stop bound needs SC-115 built on a run that will not stop, and why
FR-108's decision bound — structurally the same as both — needed SC-116, which it did not have
until this was noticed.

Some of those deserve naming for how easily they pass while asserting nothing. SC-102 waits for a
claim to lapse, so a test whose expiry is shorter than it believes passes without the recovery ever
being exercised. SC-106 asserts two absences at once — a trigger that waited too long, and one
dropped by a restart — and an assertion that no run happened is satisfied by a runtime that never
started. SC-109 asserts a refusal at load, which the runtime already produces today for the whole
`guard` block, so its test passes before the feature is written and keeps passing if the block's
shape is never actually validated: it has to distinguish an implemented key from an unimplemented
one, not merely observe a refusal. SC-112 has to hold the backend's responses rather than sever the
connection, because a severed connection fails fast and a held one is the case FR-122 exists for —
a test that cuts the link proves the easy half and leaves the hang untested. SC-113 has to kill the
process rather than stop it, because a record written on the way out satisfies a graceful stop and
is exactly the implementation FR-127 refuses. SC-105 and SC-107 are the counting ones, and a count
is the one shape here that fails honestly when it is wrong; SC-114 counts too, downward, to zero
refusal records. SC-115 is the one that needs a subject built to misbehave: a run that stops when
asked satisfies it without the enforcement in FR-126 ever executing, so the test needs a run that
does not stop on its own.

SC-110 is the hardest mutant of the set and the easiest to fake. It has to prove the *edited*
playbook ran, not that a run happened, so its test needs the edit to be observable in the run's own
output — two versions that differ in what they produce, not two versions that differ only in a
field nothing reads. A test that edits the playbook and then asserts a run occurred passes against
an implementation that resolved the playbook once, at trigger time, and never looked again.

### Operational Constraints — one is directly relevant

The constitution's wall-clock anchoring constraint is FR-118 above, and it is the reason that
requirement is stated at all rather than left as implementation detail.

## Project Structure

### Documentation (this feature)

```text
specs/002-guard/
├── spec.md          # this feature's requirements
├── plan.md          # this file
├── research.md      # Phase 0 output
├── data-model.md    # Phase 1 output
├── quickstart.md    # Phase 1 output
├── contracts/
│   ├── coordination.md    # the interface every coordinator is held to
│   ├── guard.schema.json  # the guard block, replacing the reserved property
│   └── cli.md             # the operator surface and coordination.json
└── tasks.md         # not yet written
```

### Source Code (repository root)

The shape follows the runtime core's existing layout; the guard is a stage, not a subsystem beside
one.

```text
runtime/internal/
├── guard/           # new — the decision, the Coordinator interface, the waiting slot, the stop deadline
│   ├── etcd/        # the adapter, the only package that imports the etcd client
│   └── guardtest/   # the fake, the contract suite, and the embedded server the suite runs against
├── run/             # the advisory file lock lives here today and stays, as FR-109's mechanism
├── playbook/        # the `guard` block stops being refused outright (FR-119)
└── record/          # refusal records (FR-117), waiting triggers (FR-127), the Run's new fields
```

The file lock in `run` is not replaced. It is already proven, already has a mutant, and is exactly
what FR-109 requires for a deployment that configures no backend.

## Phase 0 — Resolved

In [research.md](./research.md), 2026-09-10. The first two were answered by running the candidates
rather than reading about them; the durations are chosen thresholds, each with its reason.

1. **The hermetic proof** is three layers. The guard's logic runs against an in-process fake. One
   contract suite holds the fake and the etcd adapter to the same clauses, the adapter talking to
   an etcd server embedded in the test process over a unix socket. One test drives two built
   binaries with separate state directories against that server. A socket the test itself created
   is not network in Principle VI's sense; a fixed port, a binary found on `PATH` and a container
   are.
2. **The backend is etcd**. It met every requirement when run: server-judged expiry, a claim that
   survived the loss of the leader, the creation revision as a fencing token, a client that honours
   a context deadline under a held, severed or absent server, and no cgo in the client or the
   embedded server. The library's keep-alive helpers signal loss only at the expiry, so the runtime
   drives its own bounded renewals. Consul, Redis and PostgreSQL were run the same way and rejected
   for the reasons recorded there.
3. **The durations** are a 30 s claim expiry, renewal every 5 s, a 4 s renewal bound, a 10 s stop
   bound and a 2 s margin floor. The runtime refuses a configuration where
   `renewal interval + renewal bound + stop bound + margin floor > claim expiry`. Outside that
   inequality sit a 5 s decision bound and a 30 m default wait.
4. **A rename** cannot orphan a claim, because a claim is released through its lease. The old and
   new names are different claims. A waiting trigger whose file no longer declares its name is
   discarded rather than run under the new one.

Two findings reach back into the specification, and both are the owner's to decide rather than a
plan's.

- **FR-110 contradicts User Story 1 on two hosts**: a cron tick that waits behind the same tick's
  run on another host runs twice. The design follows the recommendation that only a trigger that
  will not recur waits; if the owner decides otherwise, one sentence of the data model changes.
- **A run shorter than the hosts' clock offset can let one tick run twice**, one host after the
  other. Recommended but not adopted: the backend records the last scheduled instant that ran. The
  coordination interface already carries that instant, so adopting it later changes an adapter.

`tasks.md` can be written against the design as it stands.

## Phase 1 — Design

- [data-model.md](./data-model.md): where each piece of state lives — backend, process memory, or
  the host's record store. It covers the claim, the rate slot, the waiting trigger and its durable
  acceptance, the refusal record and its mechanisms, and the four fields and one status the runtime
  core's Run gains.
- [contracts/coordination.md](./contracts/coordination.md): the interface the fake, the etcd
  adapter and the file lock are held to, clause by clause, each with the mutant that shows its test
  can fail; and the holder's own obligations — the stop deadline, fencing before side effects,
  enforcing the stop bound, refusing a configuration that cannot hold.
- [contracts/guard.schema.json](./contracts/guard.schema.json): the `guard` block's keys (FR-119),
  probed on the values it must refuse as well as those it must accept.
- [contracts/cli.md](./contracts/cli.md): what changes on the operator's surface, and the
  deployment's `coordination.json`.
- [quickstart.md](./quickstart.md): the validation on two real hosts, which the suite cannot be.

**Constitution Check, after design.** No gate changes verdict.

- **I** gains a refusal at startup: a configuration whose durations cannot hold.
- **III** gains the refusal records, the waiting triggers' acceptance, and a Run that says which
  guarantee it ran under.
- **IV** holds: the `guard` block carries a limit and a wait and nothing that names a deployment.
- **VI** is answered rather than open, at the costs recorded in research.md §1 — a larger test
  binary and a slower compile per mutant.

## Complexity Tracking

The first row is the departure worth stating plainly rather than burying: this feature adds a
deployment dependency to a product whose distribution story was "one static binary, nothing beside
it".

| Choice | Why | Simpler alternative rejected because |
| ------ | --- | ------------------------------------ |
| A shared coordination backend | FR-101 is worthless if it holds only within one host, and a deployment run twice for availability doubles every scheduled run — doubling side effects, not just cost | Keeping the file lock on a shared filesystem was considered and refused: advisory locking over network filesystems is not reliably honoured, so the guarantee would hold on some deployments and silently not on others, which is worse than the narrow guarantee honestly stated |
| Waiting is local to one process (FR-113) | A trigger that waits across hosts needs durable state and an ownership question of its own — a larger feature than the one it serves | Putting the waiting slot in the backend is the obvious symmetry, and it makes a process's death silently promote another host's waiting trigger into a run nobody is watching |
| Queue depth of exactly one (FR-111) | A burst must not become a backlog of runs against a world that has since changed | An unbounded queue is simpler to implement and turns a misbehaving trigger source into a stampede the rate limit then cannot help with, because the runs were already accepted |
| The rate limit discards rather than defers (FR-116) | Deferring replays the burst the limit exists to refuse | Treating both refusals the same is one code path instead of two, and it makes the limit a delay rather than a limit |
| The file lock is kept, not replaced | FR-109 needs it, and it is already proven with a mutant | Deleting it once the backend exists would make the backend mandatory, which forces the dependency on deployments that never needed it |
| The rate window lives in the backend when there is one | Counted per host, a deployment of two hosts allows twice the declared runs | A count of the local record store's runs is one query and no new keys, and delivers FR-114 at half its declared strength on the deployments this feature exists for |
| The runtime drives its own renewals | The client library's keep-alive signals loss at the expiry, measured at 5.001 s for a 5 s claim, which leaves FR-105 no time to act | The library's `KeepAlive` and `concurrency.Session` are a few lines each, and would satisfy every requirement except the one the lease exists for |
| The process exits when a run outlives the stop bound | Cancelling a context cannot end work that ignores it, and a run still going when the claim lapses breaks FR-101. The cost is every other run in that `serve` process, and its schedules until it restarts | Logging the overrun and carrying on is what every other stop path does, and makes FR-126's bound a measurement rather than a bound |
| The waiting slot is counted in the record store | `gronin run` is a process of its own, so a slot counted in memory would let every manual invocation wait | A slot in memory needs no transaction, and makes FR-111 true only for triggers that arrive through `serve` |
