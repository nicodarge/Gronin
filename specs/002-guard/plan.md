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
the hardest part of this feature to test — see Phase 0.

Saying those two must not hold at once does not make it so, which is why FR-122 and FR-123 exist. A
renewal that hangs is not a renewal that failed: it looks like one still in flight right up to the
moment the claim lapses, so the attempt needs its own time bound and exceeding it has to count as
failure. And the durations need a stated relationship, or a renewal interval equal to the expiry
satisfies every other requirement here while leaving no margin at all to act in. The runtime
refuses such a configuration rather than starting under it, which is what makes the relationship
checkable instead of aspirational (SC-112).

## Technical Context

**Language/Version**: Go, as the runtime core. No change.

**Primary Dependencies**: one new dependency, the client for whichever coordination backend Phase 0
selects. It is bound by the constraint that already governs every dependency in this repository:
**no cgo**, because `scripts/check-static.sh` refuses a binary that is not statically linked, and
that check is not negotiable for a client library.

**Storage**: the existing record store gains the refusal records of FR-117. Refusals are not runs
and must not be counted as runs, but they are read through the same operator surface (SC-108). Two
further writes: a waiting trigger is recorded when it is accepted (FR-127), which is what makes a
drop reconstructible after a kill, and the Run entity the runtime core defines gains the fact that
a run waited and for how long (FR-125) — a field its current description does not carry, so the
runtime core's own data model changes here rather than only this feature's.

**Testing**: the existing suite, under the hermeticity and mutation obligations of Principle VI.
This is the feature's hardest constraint and it is unresolved — see Phase 0.

**Target Platform**: unchanged. The coordination backend is a deployment concern, not a build one.

**Project Type**: an added stage in an existing pipeline, plus one schema block that stops being
refused (FR-119).

**Performance Goals**: the guard's decision is bounded (FR-108). The bound's value is a Phase 0
output; what the specification fixes is that exceeding it refuses rather than passes.

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
talking to a real one in the suite. What can be tested hermetically is the *contract*: claim,
renew, expire, fence. Whether that is enough, and what proves the chosen backend honours the
contract, is Phase 0's question and is not answered here.

**Deterministic.** Every mechanism in this feature is a race or a clock. A test that starts two
runs and asserts one wins is the archetype of a flake, and Principle VI forbids quarantining one.
The design has to make the interleavings injectable rather than hoped for.

**A test must be able to fail.** This is the real risk, and SC-111 states it as a criterion rather
than leaving it to discipline. Every guarantee here is that something does *not* happen, and a test
asserting a non-event is indistinguishable from a broken test until a mutant proves otherwise: a
test that asserts "no second run started" passes just as well when nothing started at all, when the
trigger never fired, and when the test's own body never executed. Each of SC-101, SC-102, SC-103,
SC-104, SC-105, SC-106, SC-107, SC-108, SC-109, SC-110, SC-112, SC-113, SC-114 and SC-115 needs a
mutant, and the mutation harness has to report zero on an unmodified tree for any of their counts
to mean anything. The two bounds are the exception: FR-108's decision bound and FR-126's stop bound
both fail loudly on their own, because an unenforced bound lets something run long and a test
waiting on it times out rather than passing quietly.

Five of those deserve naming for how easily they pass while asserting nothing. SC-102 waits for a
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
├── research.md      # Phase 0 output — not yet written
└── tasks.md         # not yet written
```

### Source Code (repository root)

The shape follows the runtime core's existing layout; the guard is a stage, not a subsystem beside
one.

```text
runtime/internal/
├── guard/           # new — the decision, the claim contract, the waiting slot, the rate window
├── run/             # the advisory file lock lives here today and stays, as FR-109's mechanism
├── playbook/        # the `guard` block stops being refused outright (FR-119)
└── record/          # refusal records (FR-117)
```

The file lock in `run` is not replaced. It is already proven, already has a mutant, and is exactly
what FR-109 requires for a deployment that configures no backend.

## Phase 0 — Open

Not started. Four questions, and the first two gate the rest.

1. **What proves the cross-host guarantee in a hermetic suite?** SC-101, SC-103 and SC-104 assert
   behaviour across two processes and a shared authority, and Principle VI forbids reaching a
   network to demonstrate it. The candidate answers — a coordination interface with an in-process
   implementation that can inject partition and expiry, a backend that runs locally without a
   network, or a contract test the real client is separately held to — have different costs and
   different blind spots. The blind spot to name explicitly: a contract test proves the runtime
   uses the backend correctly, and proves nothing about whether the backend behaves as assumed.
   That is exactly the "the stub disagreed with the real thing" failure the runtime core's own
   walkthrough found four times, recorded in issue #16.

2. **Which backend, and what does it have to guarantee?** Not a preference question. FR-106
   requires expiry judged by the backend rather than by a host, which rules out anything whose
   expiry is a client-side comparison. FR-105 requires the holder to learn it has lost the claim in
   time to stop, which is a property of the client's failure signalling, not of the store. And the
   no-cgo constraint rules out any client that needs a C toolchain.

3. **What are the durations?** Two disjoint sets, and conflating them is how the margin gets
   mis-computed.

   The first set is bound together by FR-123: the renewal interval (FR-103), the bound on a single
   renewal attempt (FR-122), and the bound on stopping a run (FR-126) must fit inside the claim's
   expiry (FR-104) with margin. Phase 0 picks these four as a set rather than one at a time,
   because a value that is reasonable alone can be impossible alongside the others, and the runtime
   refuses a set that does not fit.

   The second set is independent of that arithmetic: the guard's own decision bound (FR-108) and
   how long a trigger may wait (FR-112). Neither enters FR-123's margin.

   Every one of them is a chosen threshold rather than a derived one, so each gets a stated reason
   and a date, not a number that looks measured.

4. **What happens across a rename?** The spec's edge cases raise it: the playbook name is the
   claim's identity, so renaming a playbook mid-run leaves a claim nobody will release. The load
   stage already refuses two playbooks sharing a name, which bounds the problem but does not answer
   it.

## Complexity Tracking

One deliberate complexity, and it is a departure worth stating plainly rather than burying: this
feature adds a deployment dependency to a product whose distribution story was "one static binary,
nothing beside it".

| Choice | Why | Simpler alternative rejected because |
| ------ | --- | ------------------------------------ |
| A shared coordination backend | FR-101 is worthless if it holds only within one host, and a deployment run twice for availability doubles every scheduled run — doubling side effects, not just cost | Keeping the file lock on a shared filesystem was considered and refused: advisory locking over network filesystems is not reliably honoured, so the guarantee would hold on some deployments and silently not on others, which is worse than the narrow guarantee honestly stated |
| Waiting is local to one process (FR-113) | A trigger that waits across hosts needs durable state and an ownership question of its own — a larger feature than the one it serves | Putting the waiting slot in the backend is the obvious symmetry, and it makes a process's death silently promote another host's waiting trigger into a run nobody is watching |
| Queue depth of exactly one (FR-111) | A burst must not become a backlog of runs against a world that has since changed | An unbounded queue is simpler to implement and turns a misbehaving trigger source into a stampede the rate limit then cannot help with, because the runs were already accepted |
| The rate limit discards rather than defers (FR-116) | Deferring replays the burst the limit exists to refuse | Treating both refusals the same is one code path instead of two, and it makes the limit a delay rather than a limit |
| The file lock is kept, not replaced | FR-109 needs it, and it is already proven with a mutant | Deleting it once the backend exists would make the backend mandatory, which forces the dependency on deployments that never needed it |
