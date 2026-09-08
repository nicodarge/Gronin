# Feature Specification: Guard

**Feature Branch**: `add_guard_spec`

**Created**: 2026-09-08

**Status**: Draft

**Input**: User description: "The guard stage: the decision, taken before any command runs and any
token is spent, of whether a trigger becomes a run. A lock that holds across the hosts of one
deployment, a rate limit keyed on the playbook, and a refused trigger that waits rather than being
lost."

## Clarifications

### Session 2026-09-08

- Q: How far does the non-concurrency guarantee reach? → A: across the hosts of one deployment,
  through a shared coordination backend. The advisory file lock the runtime core ships is a
  single-host mechanism and was stated as one; this feature replaces the guarantee rather than
  widening the mechanism. The cost is accepted deliberately: a product that installs as one static
  binary with nothing beside it gains a deployment dependency, and a lease that can expire brings
  the failure mode a file lock does not have — a host that loses the backend while still running.
- Q: What happens to a trigger the guard refuses? → A: it waits, and at most one waits per
  playbook. A trigger that arrives while one is already waiting is refused outright rather than
  queued behind it.
- Q: Does waiting apply to every refusal? → A: no. A trigger refused because the playbook is
  already running waits, because the run it collides with will end. A trigger refused by the rate
  limit is discarded, because making it wait would replay the burst the limit exists to refuse.
- Q: What does the runtime do when a coordination backend is configured and cannot be reached? →
  A: it refuses to run the playbook. Falling back to the local lock would deliver the single-host
  guarantee under the name of the cross-host one, which is the failure this repository's first
  principle is written against.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - One playbook, two hosts, one run (Priority: P1)

An operator runs the same deployment on two hosts, for availability rather than for capacity: both
carry the same playbooks and both schedulers are armed. A playbook's cron expression comes due. It
executes once, on one of the two hosts, and the other host records that it did not run it and why.

**Why this priority**: it is the whole reason this feature exists. Everything else here refines
what happens to the trigger that did not become a run; without this, the runtime cannot be deployed
more than once without silently doubling every scheduled run — and doubling an agent run means
doubling its side effects, not just its cost.

**Independent Test**: run two runtime processes against one coordination backend, on two hosts or
in two containers, with one playbook whose schedule is due in a minute and whose agent stage
appends a line to a shared file. Confirm the file gains exactly one line. No other user story needs
to exist.

**Acceptance Scenarios**:

1. **Given** two runtime processes sharing a coordination backend and the same playbook, **When**
   its trigger fires on both at the same time, **Then** exactly one run executes and the other
   process records a refusal naming the lock.
2. **Given** a run holding a claim, **When** the process holding it is killed without releasing it,
   **Then** the playbook becomes eligible again no later than the claim's expiry, and no operator
   action is required.
3. **Given** a run holding a claim, **When** the process can no longer reach the coordination
   backend, **Then** it stops its own run before the claim expires rather than continuing on a
   claim it can no longer prove it holds.
4. **Given** a deployment configured with a coordination backend, **When** the backend cannot be
   reached as a trigger fires, **Then** the playbook does not run and the refusal names the
   backend, rather than the run proceeding under the local lock.
5. **Given** a deployment configured with no coordination backend, **When** a trigger fires,
   **Then** the run proceeds under a single-host guarantee, and the deployment's own status output
   says that is the guarantee it has.
6. **Given** a deployment whose renewal interval, renewal bound and stop bound do not fit inside
   its claim expiry, **When** the runtime starts, **Then** it refuses to start and names the
   durations it rejected, rather than running with a margin that cannot hold.
7. **Given** a run that has been told to stop, **When** it does not end on its own, **Then** the
   runtime ends it no later than the declared stop bound. A bound the runtime only measures is not
   a bound the margin can be computed from.

---

### User Story 2 - A refused trigger is not lost (Priority: P2)

A playbook is running when its next trigger arrives. Rather than dropping it, the runtime holds it
until the run in progress ends, then runs it once. If a third trigger arrives while one is already
waiting, it is refused — the queue is one deep, deliberately, so that a burst cannot become a
backlog of runs against a world that has since changed.

**Why this priority**: the runtime core discards a colliding trigger. That is defensible for a cron
playbook, whose next tick comes anyway, and indefensible for a trigger that will not come again.
This story is what makes the guard's refusal survivable, and it is what the webhook specification
will build on — but the lock has to exist before there is anything to wait for.

**Independent Test**: start a playbook whose agent stage sleeps, invoke it manually while it runs,
and confirm a second run starts once the first ends. Invoke it twice more during the first run and
confirm exactly one further run happens, with the extra invocation recorded as refused.

**Acceptance Scenarios**:

1. **Given** a playbook already running, **When** a trigger fires, **Then** no second run starts
   concurrently, and one run starts after the first ends.
2. **Given** a playbook already running with a trigger already waiting, **When** a further trigger
   fires, **Then** it is refused and recorded, and the waiting trigger is the one that eventually
   runs.
3. **Given** a trigger that has waited longer than the playbook's declared limit on waiting,
   **When** the run in progress ends, **Then** no run starts from it and its expiry is recorded.
4. **Given** a trigger waiting for a run to end, **When** the runtime process stops — gracefully
   or killed outright — **Then** nothing runs from that trigger when the process starts again, and
   the operator can see it was dropped rather than silently forgotten.

---

### User Story 3 - A playbook that fires too often is held back (Priority: P3)

An operator gives a playbook a limit: at most so many runs in a window. Once the limit is reached,
further triggers are refused until the window moves, and each refusal is recorded with the limit it
hit.

**Why this priority**: it bounds cost and blast radius when a trigger source misbehaves, which is a
real risk once triggers come from outside — but every trigger source in the runtime today is a cron
expression the operator wrote, and a cron expression that fires too often is fixed by editing it.
The limit earns its priority when the webhook lands.

**Independent Test**: declare a limit of two runs per minute on a playbook with a short agent
stage, invoke it four times inside one minute, and confirm two runs happened and two refusals were
recorded.

**Acceptance Scenarios**:

1. **Given** a playbook that has reached its declared limit, **When** a trigger fires, **Then** no
   run starts, the refusal is recorded naming the limit, and no claim is taken.
2. **Given** a playbook that has reached its declared limit, **When** a trigger is refused by it,
   **Then** that trigger does not wait — it is discarded.
3. **Given** a playbook whose window has moved past its earlier runs, **When** a trigger fires,
   **Then** it runs.
4. **Given** a playbook declaring no limit, **When** triggers fire, **Then** none is refused for
   rate, and the non-concurrency guarantee still applies.
5. **Given** a trigger waiting for a run to end, **When** other triggers fill the playbook's rate
   window while it waits, **Then** it is discarded when the run ends rather than run, and the
   discard is recorded — the limit bounds runs, not acceptances.

---

### Edge Cases

- **A claim outlives the run that took it.** The process died between finishing the run and
  releasing the claim. Expiry is what recovers it, which is why an expiry exists at all; the
  question the design has to answer is how long an operator waits.
- **Two hosts whose clocks disagree.** Expiry is judged by the backend, not by either host, so a
  host with a skewed clock cannot take a claim another host still holds.
- **A run longer than the claim's expiry.** Renewal is what covers it, and renewal failing is what
  triggers scenario 3 of User Story 1. A run that cannot renew must end; a claim that is not
  renewed must expire. Saying the two must not hold at once is not enough to make it so — the
  durations have to be in a stated relationship, which is FR-123, and a renewal that hangs has to
  count as one that failed, which is FR-122. Without both, an implementation can satisfy every
  other requirement here and still leave the window open.
- **The backend is reachable but slow.** A trigger cannot wait indefinitely for a decision about
  whether it may run — the decision itself needs a bound, and exceeding it is a refusal, not a
  pass.
- **A playbook renamed between the claim and the release.** The name is the identity, so a rename
  during a run means the claim cannot be found. The load stage already refuses two playbooks
  sharing a name; what happens across a rename has to be stated rather than discovered.
- **A trigger waiting while the playbook is edited underneath it.** The run that eventually starts
  must be the playbook as it stands when it starts, or the operator's edit is silently ignored for
  one run.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-101**: The runtime MUST NOT execute two runs of the same playbook concurrently, including
  from separate processes on separate hosts sharing one coordination backend.
- **FR-102**: The guard MUST reach its decision before the gather stage, so that a refused trigger
  runs no command and spends no token.
- **FR-103**: A run MUST hold a claim for the whole of its execution, and MUST renew that claim
  while it runs.
- **FR-104**: A claim MUST expire, so that a process that dies without releasing one does not
  block its playbook indefinitely.
- **FR-105**: A run that cannot renew its claim MUST end itself before that claim can expire. A
  run continuing on a claim it can no longer prove it holds makes FR-101 false while reporting it
  as held.
- **FR-106**: Claim expiry MUST be judged by the coordination backend rather than by any
  participating host, so that two hosts whose clocks disagree cannot both hold one claim.
- **FR-107**: The runtime MUST refuse to run a playbook when a coordination backend is configured
  and cannot be reached, rather than falling back to the single-host mechanism. A weaker guarantee
  delivered under the name of a stronger one is the failure this repository refuses by principle.
- **FR-108**: The guard's own decision MUST be bounded in time, and exceeding that bound MUST be a
  refusal rather than a pass.
- **FR-109**: A deployment with no coordination backend configured MUST still enforce FR-101
  within one host, and MUST state that single-host reach in its own status output rather than
  leaving the operator to infer which guarantee they have.
- **FR-110**: A trigger refused because its playbook is already running MUST wait for that run to
  end and then run once, unless FR-112 or FR-124 has discarded it in the meantime.
- **FR-111**: At most one trigger per playbook MUST wait at a time. A trigger arriving while one
  is already waiting MUST be refused rather than queued behind it.
- **FR-112**: A waiting trigger MUST expire after a declared duration, and MUST NOT produce a run
  once it has. A run that starts long after the event that caused it works against a world that
  has changed.
- **FR-113**: A waiting trigger MUST NOT survive the process holding it. Waiting is local to the
  process that accepted the trigger, and the operator MUST be able to see that a waiting trigger
  was dropped when the process stopped — whether it stopped gracefully or was killed.
- **FR-127**: Accepting a trigger into the waiting slot MUST be recorded durably at the moment it
  is accepted, not when it runs and not when it is dropped. The wait itself is in memory and dies
  with the process by FR-113; a record written only on the way out is written by a process that may
  not get the chance, which would leave FR-113's drop visible after a graceful stop and invisible
  after exactly the kill an operator is trying to understand.
- **FR-122**: A renewal attempt MUST be bounded in time, and exceeding that bound MUST count as a
  failed renewal rather than as one still outstanding. A renewal that hangs is indistinguishable
  from one that succeeded until the claim lapses, which is precisely the window FR-105 exists to
  close.
- **FR-126**: The runtime MUST have a declared bound on how long stopping a run may take, counted
  from the decision to stop it to the run being over, and MUST enforce it when it terminates a run.
  Without a stated stop bound, FR-123's margin has no third term and cannot be computed at all.
- **FR-123**: The renewal interval, plus FR-122's bound, plus FR-126's stop bound, MUST fit within
  the claim's expiry with margin left over, and the runtime MUST refuse a configuration in which
  they do not, naming the durations it rejected and the expiry they exceed. Without this, FR-103 and FR-104 are both satisfied by setting the renewal
  interval equal to the expiry, which leaves a single missed renewal no time at all to act on and
  makes FR-105 unsatisfiable while every stated requirement reads as met.
- **FR-114**: A playbook MUST be able to declare a rate limit: at most a stated number of runs
  within a stated window, keyed on the playbook name alone.
- **FR-115**: The rate limit MUST be evaluated before a claim is taken, so that a trigger the
  limit refuses never makes another host wait on a claim.
- **FR-116**: A trigger refused by the rate limit MUST be discarded rather than made to wait.
  Waiting would replay the burst the limit exists to refuse.
- **FR-124**: A waiting trigger MUST be judged against the rate limit again at the moment it
  attempts to run, and MUST be discarded under FR-116 if the limit refuses it then. The limit
  bounds runs, not acceptances: other triggers can consume the window while one waits, and a
  waiting trigger admitted on the strength of a check made before the wait would carry the playbook
  past its declared limit.
- **FR-117**: Every refusal MUST produce a record naming the playbook, the trigger that was
  refused, which mechanism refused it, and when. A refusal here means a terminal one — discarded,
  expired while waiting, or rate-limited. A trigger that collides with a run and then runs after
  waiting is not refused and MUST NOT be recorded as one.
- **FR-125**: A run started from a trigger that waited MUST record that it waited and how long. An
  operator reading a run that started well after its schedule has otherwise no way to tell a
  deferred run from a late one.
- **FR-118**: Every time recorded or compared by the guard MUST be anchored on the runtime's own
  wall clock, never on a timestamp carried by the trigger. A payload timestamp can be frozen at an
  event's first activation and resent unchanged, which makes every repeat look new — or, worse,
  makes every repeat look already handled.
- **FR-119**: The runtime MUST accept a `guard` block in a playbook, validate its shape when the
  playbook loads, and refuse a `guard` block naming a key it does not implement — on the same
  terms as every other declared bound.
- **FR-120**: A playbook declaring no `guard` block MUST still be subject to FR-101. Non-concurrency
  is the runtime's guarantee, not an option the playbook elects.
- **FR-121**: A trigger that waited MUST run the playbook as it stands when the run starts, not as
  it stood when the trigger arrived.

### Key Entities

- **Claim**: the right to run one named playbook, held by exactly one run at a time across the
  deployment. Has a holder, an expiry judged by the backend, and a renewal that is itself bounded
  (FR-122) and spaced far enough beneath the expiry to leave room to act (FR-123).
- **Waiting trigger**: at most one per playbook, held by a single process, carrying what invoked it
  and when it arrived, and expiring on its own.
- **Rate window**: the record of a playbook's recent runs against which a declared limit is judged,
  keyed on the playbook name and nothing else.
- **Refusal record**: what the operator reads to learn why a trigger did not become a run.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-101**: Two runtime processes sharing one coordination backend, triggered for the same
  playbook at the same time, produce exactly one run. Demonstrated by an effect the run has, not by
  a log line claiming it held the claim — satisfying FR-101 and FR-103.
- **SC-102**: A process killed mid-run leaves its playbook runnable again within the claim's
  expiry, with no operator action — FR-104.
- **SC-103**: A process that loses its coordination backend mid-run ends that run before its claim
  expires, and a second host does not start a run before the first has ended — FR-105 and FR-106.
- **SC-104**: With a coordination backend configured and unreachable, no run starts and the
  refusal names the backend — FR-107. With none configured, runs proceed and the deployment
  reports the single-host reach of its guarantee — FR-109.
- **SC-105**: A trigger arriving during a run produces exactly one run after it ends; two further
  triggers during that same run produce no additional run and two refusal records — FR-110,
  FR-111 and FR-102.
- **SC-106**: A trigger that waits past its declared expiry produces no run, and a waiting trigger
  produces no run after a restart — FR-112 and FR-113.
- **SC-107**: A playbook limited to N runs per window, triggered N+2 times inside one window,
  produces N runs and two refusals, none of which waits — FR-114, FR-115 and FR-116. A trigger
  that waits while the window fills produces no run either — FR-124.
- **SC-108**: Every refusal in the scenarios above is readable afterwards through the operator's
  own command surface, naming its mechanism — FR-117 and FR-118.
- **SC-109**: A playbook declaring a `guard` key the runtime does not implement is refused at load,
  and one declaring no `guard` block is still held to non-concurrency — FR-119 and FR-120.
- **SC-110**: A playbook edited while a trigger waits runs in its edited form — FR-121.
- **SC-116**: A trigger whose guard decision cannot be reached in time is refused within the
  decision bound — FR-108. Measured against a backend that holds its response rather than one that
  refuses promptly, for the same reason SC-112 is: a backend that answers quickly satisfies the
  criterion whether or not any bound is enforced.
- **SC-115**: A run that does not end when told to is over within the declared stop bound, ended by
  the runtime rather than by itself — FR-126. Measured against a run built not to stop, because a
  run that stops promptly satisfies the criterion without the enforcement ever running.
- **SC-112**: A deployment whose renewal interval, renewal bound and stop bound do not fit inside
  its claim expiry is refused at startup, naming the three durations and the expiry they exceed —
  FR-123. And a run whose renewal attempts hang rather than fail ends before its claim can lapse,
  demonstrated by holding the backend's responses rather than by severing it — FR-122.
- **SC-113**: After a process is killed while a trigger waits, the drop is readable through the
  operator's own command surface, naming the trigger and when it arrived — FR-113 and FR-127.
  Measured by reading the record, not by observing that no run happened: the absence is already
  SC-106's, and an absence is satisfied by a runtime that never started. The kill is what makes it
  meaningful — a graceful stop would pass against a record written on the way out, which is the
  implementation FR-127 exists to refuse.
- **SC-114**: A trigger that waits and then runs produces no refusal record, and its run says it
  waited and for how long — FR-117 and FR-125.
- **SC-111**: Each of the above has at least one test that fails when the behaviour it asserts is
  removed, shown by the mutation harness rather than by the suite passing. A test that never
  executes its own body passes forever, and this feature's guarantees are all of the kind that look
  satisfied when nothing is happening. The guard's whole job is to make something *not* happen, so
  a test asserting one is indistinguishable from a broken test until a mutant proves otherwise.
  There is no exception to this, the two time bounds included: a bound is only exercised by a
  subject that exceeds it, so against a prompt one an unenforced bound is invisible.

## Assumptions

- **Deduplication is out of scope, and not by preference.** It needs an event identity, and a cron
  tick has none: two ticks of the same expression are the same event in every respect but their
  time. It is specified with the webhook trigger, which is what first supplies an identity. The
  rate limit does not need one, which is why it is here — it keys on the playbook name alone,
  per FR-114.
- **Semantic retrieval and the webhook trigger are out of scope**, each specified separately. None
  of the three needs the others to ship.
- **The coordination backend is not named here.** Whether it is a database the deployment already
  runs, a key-value store, or something else is a plan decision. What the specification fixes is
  what the guarantee has to be worth: FR-106's judgement of expiry, and FR-107's refusal to
  degrade quietly, are the properties any candidate has to provide.
- **The single static binary is preserved as the artifact, not as the deployment.** The runtime
  still ships as one binary with no cgo; what changes is that a deployment wanting the cross-host
  guarantee has something to run beside it. A deployment that wants neither keeps what it has
  today, under FR-109.
- **Waiting is local to one process, deliberately.** A trigger that waits across hosts would need
  its own durable state and its own ownership question, which is a larger feature than the one it
  would serve. FR-113 states the limit rather than leaving it to be discovered.
- **The runtime's existing advisory file lock remains** as the mechanism behind FR-109, rather than
  being replaced. It is already proven and already has a mutant.
