---

description: "Task list for the guard"
---

# Tasks: Guard

**Input**: Design documents from `/specs/002-guard/`

**Prerequisites**: [plan.md](./plan.md), [spec.md](./spec.md), [research.md](./research.md),
[data-model.md](./data-model.md), [contracts/](./contracts/), [quickstart.md](./quickstart.md)

**Tests**: included, and not optional here. SC-111 makes a test that fails when its behaviour is
removed a success criterion in its own right, and every guarantee this feature makes is that
something does *not* happen — which a broken test asserts just as well as a working one. So every
success criterion below has a test task and a mutant registered with `scripts/check-mutation.py`,
and a mutant counts only once it is declared in `runtime/testdata/mutations.json` and killed.

**Organization**: grouped by user story, in the specification's priority order. US1 is the
feature; US2 and US3 refine what happens to the trigger that did not become a run. Each phase
leaves the runtime shippable: a key of the `guard` block whose story has not landed stays refused
at load (T010), because a declared bound nothing applies is what Principle I refuses.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an incomplete task)
- **[Story]**: the user story the task serves

## Path Conventions

Paths follow [plan.md](./plan.md)'s structure, under the runtime core's module at `runtime/`:

- `internal/guard` — the decision, the `Coordinator` interface, the waiting slot, the holder's
  renewal loop and stop deadline. It imports `record` and `playbook`, never `run`.
- `internal/guard/etcd` — the adapter, the only package that imports the etcd client.
- `internal/guard/guardtest` — the fake, the contract suite, the embedded server and the holding
  proxy. Imported by `_test.go` files only; T003 is what notices if that stops being true.
- `internal/run` — keeps the advisory file lock, and gains the single-host `Coordinator` over it
  in `filelock.go`. It lives here rather than in `guard` because the lock does, and because `run`
  already has to import `guard` to fence before side effects (R3); the reverse import would be a
  cycle.
- `internal/record`, `internal/playbook`, `cmd/gronin` — as named in plan.md.

Two files the plan does not name, and why: `internal/guard/config.go` parses `coordination.json`,
because R5's refusal is about the claim set's durations and belongs beside the holder that relies
on them; `cmd/gronin/coordination.go` chooses the coordinator, beside `deployment.go`, where every
other piece of deployment state is opened.

## Mutants

Every mutant in this file is declared in `runtime/testdata/mutations.json` with `tree: ".."`, the
file it mutates relative to `runtime/`, and a command. Where another test in the same package would
kill the mutant too, the command is scoped with `-run` to the test the task names — otherwise the
harness shows that the package can fail, not that this test can (the `-run TestOnlyAnOccurrence`
entry is the precedent), and the name a mutant task gives to `-run` is the name the test task it
cites gives its test. The find text is written when the code exists, and the harness refuses one
it cannot find exactly once, so a mutant whose target later moves fails the mutation job rather
than passing silently.

---

## Phase 1: Setup

- [x] T001 Add `go.etcd.io/etcd/client/v3` and `go.etcd.io/etcd/server/v3` to `runtime/go.mod`
      and `runtime/go.sum`, the server imported only from `internal/guard/guardtest` and
      `_test.go` files. Build `CGO_ENABLED=0 go build ./cmd/gronin` and run
      `scripts/check-static.sh` on it locally; the CI `build` job already runs the same check on
      every target, so no workflow change is needed. Confirm the `suite` and `mutation` jobs'
      `go mod download` brings the new modules into the cache the no-network run needs, since both
      run with `GOPROXY=off`
- [x] T002 [P] `runtime/internal/bintest/bintest.go`: `Start` — a built `gronin` held open, with its
      standard output readable line by line, `Signal` (SIGSTOP, SIGCONT, SIGKILL, SIGTERM), a
      `Wait` bounded by a deadline, and a cleanup that kills it. Every test that keeps `serve` up,
      freezes a process or kills one mid-run goes through it rather than rolling its own
- [x] T003 [P] `TestTheBinaryLinksNoEtcdServer` in `runtime/cmd/gronin/binary_test.go`: the built executable's module list, read
      with `debug/buildinfo.ReadFile` on `bintest.Build(t)`, holds no `go.etcd.io/etcd/server`
      module. It also asserts one module that must be there — `modernc.org/sqlite` now, the etcd
      client once T053 lands — because a check on an empty list passes. Nothing else notices a
      test-support import reaching a shipped package: the binary still builds, statically, and
      every other test passes
- [x] T004 Register T003's mutant, `the shipped binary links the embedded etcd server`: a blank
      import of `go.etcd.io/etcd/server/v3/embed` added to `cmd/gronin/main.go`; command
      `go test ./cmd/gronin -count=1 -run TestTheBinaryLinksNoEtcdServer`

**Checkpoint**: the dependencies are in, the binary is still static, and a test says which of them
it links.

---

## Phase 2: Foundational (blocks every story)

### The record store

- [x] T005 `runtime/internal/record/migrations/0002_guard.sql`: the `refusals`, `waiting_triggers`
      and `last_ticks` tables, and the `runs` columns `waiting_trigger_id`, `waited_ms`,
      `claim_reach`, `claim_token`, per [data-model.md](./data-model.md). A new file rather than an
      edit of `0001_initial.sql`: `schema.go` applies migrations by name, and an edited one is
      skipped on every store that already applied it
- [x] T006 `runtime/internal/record/store.go` and `runtime/internal/record/runs.go`: the Run's four
      fields, the `claim_lost` status, and the refusal `mechanism` type with every value
      data-model.md lists — read and written by `CreateRun`, `FinishRun` and `scanRun`
- [x] T007 [P] `runtime/internal/record/refusals.go`: write a refusal record and list them most
      recent first. `detail` passes the redactor at the write boundary like every other record, and
      `refused_at` and `due_at` are stored in UTC
- [x] T008 [P] `runtime/internal/record/ticks.go`: read and write one playbook's last tick — the
      single-host form of FR-128, used only under the file lock
- [x] T009 Test in `runtime/internal/record/guard_test.go`: a store created under the runtime core's
      schema migrates, and its existing runs read back with the new fields empty; a refusal, a last
      tick and a run's new fields round-trip; a configured secret written into a refusal's detail
      does not reach the database file (the `testsecret` sentinel, as the leak test uses)

### The `guard` block

- [x] T010 [P] `runtime/internal/playbook/playbook.go` gains the `Guard` type (`rate.runs`,
      `rate.per`, `wait`). The reserved `guard` property of
      `specs/001-runtime-core/contracts/playbook.schema.json` is replaced by the content of
      [contracts/guard.schema.json](./contracts/guard.schema.json), and
      `runtime/internal/playbook/playbook.schema.json` with it — `parse_test.go` holds the two
      identical. In `validate.go`, `validateReserved` keeps only `retrieve`, and a new
      `validateGuard` refuses `rate` and `wait` by field name as declared but not yet applied,
      until T076 and T094 lift each one. Fixtures:
      `runtime/internal/playbook/testdata/schema/refused/guard-block.yaml` becomes
      `guard-unknown-key.yaml`, beside refused `guard-rate-per-seconds.yaml` and
      `guard-rate-zero-runs.yaml`; `runtime/testdata/playbooks/hostile/guard-block.yaml` becomes
      `guard-unknown-key.yaml`; the tables in `parse_test.go` and `validate_test.go` follow. The
      existing mutant `the gate accepts a reserved block` keeps its target and is now killed by the
      `retrieve` case alone — confirm it still is
- [x] T011 SC-109, the load half, `TestGuardBlock…` in `runtime/internal/playbook/parse_test.go`
      and `validate_test.go`: the shape layer probed on what it must refuse — an unknown key, `runs: 0`,
      `per: 30s`, `per: 0m`, `wait: 5`, a `rate` without `per` — and on what it must accept, a
      shape-valid `rate` and `wait`. The gate's refusal of a not-yet-applied key is asserted by
      field name, so that lifting it in a later phase is a one-line change to the table

### The interface, the fake and the contract

- [x] T012 [P] `runtime/internal/guard/doc.go` and `runtime/internal/guard/coordinator.go`: the
      interface in [contracts/coordination.md](./contracts/coordination.md) as written — the
      `Coordinator` and `Claim` interfaces, `AcquireRequest`, `Holder`, `RateLimit`, `TriggerRef`
      and the sentinel errors — plus `Claim.Expiry()`, which C12 requires and the interface block
      omits; the same change adds it to contracts/coordination.md. And the C13 seam: an option the
      etcd adapter and the fake call between reading the last tick and sending the transaction, nil
      outside the contract suite
- [x] T013 `runtime/internal/guard/clock.go` and `runtime/internal/guard/guardtest/clock.go`: the
      runtime's clock as an interface with its two readings kept apart — monotonic for durations and
      deadlines, wall for recorded timestamps (FR-118) — and its timers; the system implementation,
      and a fake the test advances, whose wall reading can be stepped backwards without moving the
      monotonic one
- [x] T014 [P] `runtime/internal/guard/guardtest/etcdserver.go` and
      `runtime/internal/guard/guardtest/proxy.go`: an etcd server embedded in the test process, its
      client and peer URLs unix sockets under a short temporary directory — a socket path longer
      than the kernel's `sun_path` limit fails to bind, and `t.TempDir()` names grow with the test's
      name — stopped at cleanup; and a unix-socket proxy between client and server with a switch that
      holds (stops forwarding without closing) and one that severs. Research.md §1 is why this is
      not network in Principle VI's sense, and why a fixed port or a binary found on `PATH` would be
- [x] T015 Test in `runtime/internal/guard/guardtest/etcdserver_test.go`: every listener the embedded
      server opened is a unix socket, read from the server's own listeners. Not from
      `/proc/net/tcp`: the whole suite shares one network namespace, and other packages' loopback
      servers would show there
- [x] T016 `runtime/internal/guard/guardtest/fake.go`: the fake `Coordinator`. It judges expiry on a
      clock of its own, separate from the runtime's (C3), and can be told, per handle, to hold every
      response or to sever — per handle, so that one holder loses the backend while a contender still
      reaches it (SC-103). It can expire a claim now, grant less than it was asked (C12), and call the
      C13 seam. A held call returns only when its context ends and never completes late: that is
      what makes a hang distinguishable from a slow success
- [x] T017 `runtime/internal/guard/guardtest/contract.go`: `Contract(t, subject)` running every clause
      of contracts/coordination.md except C8 and C9, which T085 adds with the rate slots. A clause is
      skipped for a subject only where the contract's table says n/a. C4 asserts from a watchdog of
      its own, never from `go test`'s timeout; C2 polls with a deadline and never sleeps a fixed time;
      C13's interleaving cases hold the second caller at the seam.
      *Deviation*: C13's interleaving cases are skipped for the file lock, which has no seam to hold
      a caller at — it reads and writes the tick under the lock it has already taken, so no
      interleaving puts a second caller between the two; the suite says so where it skips them
- [x] T018 `runtime/internal/guard/guardtest/fake_contract_test.go`: the contract against the fake
- [x] T019 `runtime/internal/guard/etcd/etcd.go`: the adapter. `Acquire` grants a lease, refuses a
      grant shorter than asked (C12), reads the last tick, and sends one transaction comparing the
      claim key's creation revision with zero and the tick key's modification revision with the one
      it read, writing the claim and the new tick; an overtaken transaction reads and decides again
      inside the same deadline (C13) — a failed transaction does not say which comparison lost, so
      whether the refusal is `ErrHeld` or `ErrTickRan` is decided from the fresh read, never from the
      failure itself; the token is the claim key's creation revision (C6); a
      decision that times out revokes the lease it was granted on the way out, and otherwise leaves
      it to lapse (research.md §3). `Renew` is one `KeepAliveOnce` per call, `requested lease not
      found` mapped to `ErrLost` (C5) and every other error to a failed attempt. `Fence` is a
      transaction on the creation revision; `Release` revokes and tolerates a second call; `Released`
      watches the key. Keys as in the contract, under the configured prefix. The library's
      `KeepAlive` and `concurrency.Session` are not used: they signal loss at the expiry
- [x] T020 `TestEtcdContract` in `runtime/internal/guard/etcd/contract_test.go`: the contract against the adapter talking to
      the embedded server through the proxy, which is what holds it for C4
- [x] T021 `TestGrantedExpiry` in `runtime/internal/guard/etcd/grant_test.go`: the adapter's comparison of granted
      and requested expiry, as a table — shorter refused, equal and longer accepted and reported. The
      embedded server can only grant longer than asked, so C12's refusing half cannot be reached
      through it; the contract exercises that half on the fake, and this is where the adapter's own
      comparison is shown able to fail
- [x] T022 `runtime/internal/run/filelock.go`: the single-host `Coordinator` over the existing
      advisory lock — `Acquire` takes the in-process claim and the flock through the functions
      already in `run.go`, which stay where they are so the existing lock mutants keep their target;
      `Renew` and `Fence` are no-ops, since the lock cannot be lost while its holder lives; `Released`
      returns once the lock can be taken or the context ends; the last tick is read and written
      through `record/ticks.go` while the lock is held (C13); `Reach` is `single-host`.
      `Manager.Begin` stops taking the lock itself and is handed the claim the guard took.
      *Deviation*: the in-process claim map is gone rather than kept. C1 requires the refusal to name
      the holder, which across processes only the lock file can carry; once it does, the map excluded
      nothing the flock did not and named nothing the file did not, so its mutant `two runs of one
      playbook are allowed at once` had become unkillable and goes with the state it mutated.
      `acquireLock`, `releaseLock` and the name check stay in `run.go` and keep their mutants.
      *Deviation*: until the guard stage lands (T045, T047), `Execute`, `Replay` and `Resume` take the
      claim themselves through `Executor.Coordinator`, which defaults to the file lock, and pass no
      trigger kind — so no run consults the last tick until the scheduler's occurrence reaches it
- [x] T023 `TestFileLockContract` in `runtime/internal/run/filelock_contract_test.go`: the contract against the file lock where
      the contract's table applies. C2's equivalent stays
      `TestALockHeldByAKilledProcessIsAcquirable`, re-pointed at `filelock.go`; C11 inherits the
      existing mutant `a filesystem failure is reported as a concurrent run`. The existing tests that
      reached the lock through `Manager.Begin` go through the file lock instead, and every existing
      lock mutant is confirmed still killed.
      *Deviation*: `finishing a run does not release its cross-process lock` is re-pointed at
      `Finish`'s release of the claim, where that release now happens; it is the same mutation and is
      still killed by `TestASecondManagerOverTheSameStateDirIsRefused`

### Mutants for the foundation

- [x] T024 Register the fake's and the embedded server's mutants, each with command
      `go test ./internal/guard/guardtest -count=1`, all in `internal/guard/guardtest/fake.go` unless
      named: C1 `the fake stops consulting its claims`; C3 `the fake's expiry reads the runtime's
      clock`; C4 `a held fake call ignores its context`; C13 `the fake lets a tick equal to the
      recorded one through`; C14 `the fake records its own clock as the tick`; and
      `the embedded server listens on TCP`, a client URL on loopback, in
      `internal/guard/guardtest/etcdserver.go` (T015)
- [x] T025 Register the etcd adapter's mutants, in `internal/guard/etcd/etcd.go`, command
      `go test ./internal/guard/etcd -count=1 -run TestEtcdContract` unless named. One per "fails
      when" of contracts/coordination.md: C1 `the claim transaction stops comparing the creation
      revision`; C2 `the claim key is written without its lease` and `the expiry is sent in
      milliseconds`; C4 `a renewal uses a context of its own`; C5 `a missing lease is reported as
      unavailable`; C6 `a fence checks that the key exists rather than its revision`; C7 `release
      does nothing`; C10 `released returns only at its deadline`; C11 `a timeout is classified as
      contention`; C12 `the granted expiry is not compared`, scoped to
      `-run TestGrantedExpiry` (T021); C13 `the last tick is not written`, `a tick equal to the
      recorded one is let through`, `every tick is refused once one is recorded`, `the tick's
      revision is not compared`, `an absent tick record is not compared`; C14 `the adapter records
      its clock as the tick`, `the adapter compares its clock with the recorded tick`
- [x] T026 Register the file lock's mutants, in `internal/run/filelock.go`, command
      `go test ./internal/run -count=1 -run TestFileLockContract`: C7 `the file lock's release does
      nothing`, C10 `the file lock's released returns only at its deadline`, C13 `the single-host
      last tick is not written`

**Checkpoint**: the fake, the etcd adapter and the file lock are held to one contract, inside the
no-network namespace, and the record store has somewhere to put what the guard decides. No trigger
consults any of it yet.

---

## Phase 3: User Story 1 — One playbook, two hosts, one run (P1) 🎯 MVP

**Goal**: a trigger becomes a run only while its playbook's claim is held, across every host sharing
one backend; a holder that cannot prove its claim stops before the claim can lapse; and a scheduled
tick runs at most once across the deployment.

**Independent Test**: two `gronin serve` processes against one embedded etcd, each with its own
state directory, one playbook due at the next minute whose run appends a line to a file the test
owns. The file gains exactly one line, and the host that did not run it records a refusal naming
the lock.

A trigger that collides in this phase is refused as `claim_held` whatever its kind, as the runtime
core does today. Waiting lands in US2.

### Tests for User Story 1

- [x] T027 [P] [US1] SC-101 in `runtime/cmd/gronin/guard_binary_test.go`,
      `TestTwoServesRunOneTickOnce`: an embedded etcd; two `serve` processes started with
      `bintest.Start`, each with its own state directory — sharing one would let the file lock give
      the right answer with the backend disconnected — and `--api-address 127.0.0.1:0`, since the
      default is a fixed port the second would fail to bind. The playbook's gather step sleeps
      long enough for both ticks to land inside the run, then appends a line to a file under the
      test's directory; the line is the run's effect rather than a log line claiming a claim, and a
      gather step runs only after the guard admits (FR-102). Exactly one line; one `claim_held`
      refusal in the other host's `gronin refusals`, naming the run that holds it
- [x] T028 [P] [US1] SC-117 in `runtime/cmd/gronin/tick_binary_test.go`,
      `TestADelayedHostDoesNotRunATickAgain`: as T027, with a run shorter than the gap between the
      two deliveries — a run that outlasts it is refused by FR-101 alone and passes with FR-128
      removed. B is frozen with SIGSTOP before tick T and resumed after A's run of T has ended; B's
      catch-up fires T, which is refused as `tick_already_ran` naming the tick and A's run. The next
      tick, with both live, produces exactly one further line — which is what fails an
      implementation that refuses every tick once one is recorded. B is delayed by being stopped, not
      by having its clock set behind, which a test cannot do to one process (research.md §5)
- [x] T029 [P] [US1] SC-102 in `runtime/internal/guard/etcd/crash_test.go`,
      `TestAKilledHoldersClaimLapses`: a re-execution of the test binary takes a claim through the
      adapter against the embedded server and says so, and is killed with SIGKILL. An `Acquire` made
      at once is refused with `ErrHeld` — so the claim existed and was not released — and polling
      `Acquire` then succeeds within the claim expiry plus a stated slack. The pattern is
      `TestALockHeldByAKilledProcessIsAcquirable`'s; a fixed sleep shorter than the expiry would pass
      without the recovery ever running
- [x] T030 [P] [US1] SC-103, `TestHolder…` in `runtime/internal/guard/holder_test.go`: holder A and contender B on
      one fake, the runtime's clock injected. A's handle is severed; A decides to stop at
      `sent + claim expiry − stop bound − margin floor` to the tick (R1), with `sent` the instant the
      last successful renewal was sent, not answered — the fake answers a round trip later on the
      injected clock, which is what separates the two. A's run is over before its claim lapses on
      the fake's clock, B acquires only after both, and A's run is recorded `claim_lost`. An
      `ErrLost` from `Renew` or `Fence` stops A at once rather than at the deadline (R2). A backward
      step of the wall reading mid-run leaves the deadline where it was, because it is computed on
      the monotonic reading (FR-118)
- [x] T031 [P] [US1] R3, `TestFence…` in `runtime/internal/run/fence_test.go`: the fake expires the claim between the
      agent stage and the first sink; the sink's endpoint, an `httptest` server, receives nothing, and
      the run ends `claim_lost` rather than `failed`
- [x] T032 [P] [US1] SC-104 in `runtime/cmd/gronin/coordination_cmd_test.go`. `TestAnUnreachableBackendRefuses`: with
      `coordination.json` naming a unix socket nothing listens on, `gronin run` exits non-zero within
      the decision bound plus slack, its gather step leaves no trace, and `gronin refusals` shows
      `backend_unavailable` naming the endpoint; `gronin serve` in that state prints that the backend
      is not reachable and stays up rather than exiting. `TestTheReachIsStated`: with no `coordination.json`, `serve`'s first
      guard line says `single-host`, and a run through `gronin run` succeeds and `gronin show` says it
      ran single-host
- [x] T033 [P] [US1] SC-112, the configuration half, `TestConfig…` in `runtime/internal/guard/config_test.go`: the
      defaults are accepted; a configuration refused *only* because of the stop bound (research.md
      §3: 5 + 4 + 10 + 2 against an expiry of 20) — one refused on the other terms alone passes the
      mutant that drops the stop-bound term; a renewal bound not below the interval; an expiry that is
      not a whole number of seconds. The refusal names every duration and the expiry they exceed.
      Beside them, the refusals contracts/cli.md states for the file: an unknown key, and a credential
      given as a literal rather than a `${config.…}` reference
- [x] T034 [P] [US1] SC-112, the hang half, `TestRenew…` in `runtime/internal/guard/renew_test.go`: with every
      renewal held rather than severed, the run ends before its claim can lapse. And with only the
      first renewal held and the later ones answered, the run survives — which is only possible if the
      held attempt was abandoned at the renewal bound and the next one sent. The second case is what
      makes FR-122 testable at all: R1 anchors the deadline on the last successful send, so an
      unbounded attempt cannot extend the claim, and a test that only ever holds every renewal passes
      with the bound removed
- [x] T035 [P] [US1] SC-112, the operator's surface, `TestServeRefusesDurationsThatCannotHold` in
      `runtime/cmd/gronin/coordination_refusal_test.go`:
      `gronin serve` with a `coordination.json` whose durations cannot hold exits non-zero before
      arming anything, prints `Nothing was armed.`, and names the durations and the expiry, as
      contracts/cli.md shows
- [x] T036 [P] [US1] SC-115, `TestTheStopBound` in `runtime/internal/guard/stopbound_test.go`: a re-execution of the test
      binary runs a holder on an in-process fake whose run ignores its context, and loses its claim;
      the test asserts the child is gone within the stop bound plus slack after it reports its stop
      decision. The subject is built not to stop, because one that stops when asked passes without the
      enforcement ever running (R4)
- [x] T037 [P] [US1] SC-116, `TestTheDecisionBound` in `runtime/internal/guard/decision_test.go`: with the fake holding every
      response, a trigger is refused within the decision bound plus slack, measured by the test's own
      watchdog, and the refusal is `backend_unavailable` naming the backend. Against a backend that
      refuses promptly the criterion passes with no bound enforced, which is why it holds
- [x] T038 [P] [US1] SC-118, `TestATickIsJudgedByItsSchedule` in `runtime/internal/guard/tick_test.go`: two guards on one fake, each on
      an injected clock. The first takes tick T while its clock reads past T+1. With the second's clock
      well behind, T+1 runs; with it well ahead, T is refused. The first half fails an implementation
      that records or compares a clock reading, the second one that lets too much through, and each
      passes against the mutant the other catches (R6)
- [x] T039 [P] [US1] FR-101, FR-102 and FR-120, `TestGuard…` in `runtime/internal/run/guard_test.go`: a playbook with
      no `guard` block, running; a scheduled trigger for it is refused `claim_held`, its gather step
      leaves no trace and the stub agent is never started; a replay and a resume of an earlier run are
      refused `claim_held` too, rather than run beside it
- [x] T040 [P] [US1] `TestAGuardRefusalIsNotAMissedOccurrence` in `runtime/cmd/gronin/serve_cmd_test.go`: a scheduled trigger the guard refused
      is not also recorded as a missed occurrence — two records of one event tell an operator it
      happened twice — and the instant handed to the guard is the `dueAt` the scheduler fired for.
      The runtime core's test of a tick colliding with a run changes with it: that collision is now a
      refusal record, not a missed occurrence
- [x] T041 [P] [US1] SC-108 for this story's mechanisms, in `runtime/cmd/gronin/refusals_cmd_test.go`:
      refusal records of each kind US1 writes — `claim_held`, `tick_already_ran`,
      `backend_unavailable` — seeded through `record`, listed by the built `gronin refusals` most
      recent first, each line naming its time in UTC, the playbook, the trigger kind, the mechanism and
      the detail (`TestRefusals…`). And `TestARefusalIsDatedByTheRuntime` in
      `runtime/internal/guard/refusal_test.go`: a scheduled trigger refused while
      the injected clock reads well past its due instant records `refused_at` from the clock and
      `due_at` from the tick, and a `--trigger` value shaped like a timestamp becomes neither (FR-118)
- [x] T042 [P] [US1] `TestCredentials…` in `runtime/internal/guard/etcd/client_test.go`: the adapter authenticates to the
      embedded server with a TLS client certificate, and to one with authentication enabled with a
      username and password. The certificates are generated in the test, never committed; the server
      certificate names the socket's file, which is what the client verifies over a unix socket
      (research.md §2, *Credentials, embedded*). A password marked secret reaches neither the record
      nor the log

### Implementation for User Story 1

- [x] T043 [US1] `runtime/internal/guard/config.go`: parse `coordination.json` as contracts/cli.md
      specifies — endpoints (a `unix://` endpoint included), prefix, credentials as `${config.…}`
      references resolved like the MCP catalogue's and seeding the redactor when secret, TLS paths,
      and the durations with research.md §3's defaults. Refuse per R5, naming every duration and the
      expiry; the margin floor is a constant, not a key
- [x] T044 [US1] `runtime/internal/guard/etcd/client.go`: an etcd client from that configuration, every
      call made under the context it is handed
- [x] T045 [US1] `runtime/internal/guard/guard.go`: the decision. Bounded by the decision bound
      (FR-108); `Acquire` with `TriggerRef.DueAt` set from the instant the scheduler fired for, and
      only for a scheduled trigger (R6, FR-129); `ErrHeld`, `ErrTickRan` and `ErrUnavailable` — or
      the bound exceeded — become `claim_held`, `tick_already_ran` and `backend_unavailable`
      refusals, written with `refused_at` on the wall reading (FR-117, FR-118). The claim is taken
      for every playbook, with or without a `guard` block (FR-120)
- [x] T046 [US1] `runtime/internal/guard/holder.go`: the renewal loop — one attempt per renewal
      interval, each bounded by the renewal bound, never two in flight (FR-122); the stop deadline on
      the monotonic reading, anchored on when the last successful renewal was sent (R1); `ErrLost`
      stops at once (R2); from the stop decision the run's context is cancelled, and if the run is not
      over within the stop bound the process exits through a function the test can replace (R4,
      FR-126)
- [x] T047 [US1] `runtime/internal/run/execute.go`, `runtime/internal/run/run.go` and
      `runtime/internal/run/replay.go`: the run identifier is minted before the guard decides, so the
      claim can name it; `Execute`, `Replay` and `Resume` go through the guard before `Begin`, and so
      before gather (FR-102); the run records `claim_reach` and `claim_token`; a fence before the agent
      stage and before each sink delivers (R3) — through a per-sink gate in
      `runtime/internal/sink/sink.go` if `DeliverAll` has to take one; a run stopped under FR-105
      finishes `claim_lost`. Replay and resume are refused rather than made to wait
- [x] T048 [US1] `runtime/cmd/gronin/coordination.go` and `runtime/cmd/gronin/deployment.go`: read
      `coordination.json` from the state directory; the etcd adapter when it is present, the file lock
      when it is absent — and never the file lock because the backend failed to answer (FR-107); the
      host name and a per-process instance identifier for the claim's holder
- [x] T049 [US1] `runtime/cmd/gronin/serve_cmd.go`: the guard's reach is the first thing `serve` says
      about the guard (FR-109); a configuration R5 refuses stops it before anything is armed; a backend
      not reachable at startup is said and does not stop it; the fire function hands the guard its
      `dueAt` and returns no error once the guard has written a refusal
- [x] T050 [US1] `runtime/cmd/gronin/refusals_cmd.go` and `runtime/cmd/gronin/root.go`: `gronin refusals`,
      opening the record store the way `gronin runs` does, so it works after `serve` has been killed
- [x] T051 [US1] `runtime/cmd/gronin/run_cmd.go` and `runtime/cmd/gronin/records_cmd.go`: a refused
      `gronin run` exits non-zero naming the mechanism; `gronin show` says which guarantee the run ran
      under
- [x] T052 [US1] `runtime/cmd/gronin/records_cmd.go`: `gronin runs` lists `claim_lost` like any other
      status (contracts/cli.md). It prints the stored status string and should need no branch of its
      own; T031's `claim_lost` run is read back through `gronin runs` to show it rather than assume it.
      *Deviation*: the `claim_lost` run `TestRunsListsAClaimLostRun` reads back is seeded through
      `record` rather than being T031's own, which lives in another package's test process and cannot
      be handed to the built executable; the listing prints the stored status string, so what the
      seeded row exercises is the same path
- [x] T053 [US1] Switch T003's positive control from `modernc.org/sqlite` to
      `go.etcd.io/etcd/client/v3`, now that the binary links it

### Mutants for User Story 1

- [x] T054 [US1] SC-101: `a playbook without a guard block takes no claim` in
      `internal/guard/guard.go`, command `go test ./internal/run -count=1 -run TestGuard`; `the guard
      decides after the gather stage` in `internal/run/execute.go`, same command; `a resume takes no
      claim` in `internal/run/replay.go`, same command; `a guard refusal is recorded as a missed
      occurrence` in `cmd/gronin/serve_cmd.go`, command
      `go test ./cmd/gronin -count=1 -run TestAGuardRefusalIsNotAMissedOccurrence`; and C1's etcd
      mutant from T025 declared a second time with command
      `go test ./cmd/gronin -count=1 -run TestTwoServesRunOneTickOnce`, so that the test driving the
      built binaries is shown to fail on its own
- [x] T055 [US1] SC-102: C2's two etcd mutants from T025, declared again with command
      `go test ./internal/guard/etcd -count=1 -run TestAKilledHoldersClaimLapses`
- [x] T056 [US1] SC-103, in `internal/guard/holder.go`, command
      `go test ./internal/guard -count=1 -run TestHolder`: `sent is taken when the reply arrives`,
      `the stop deadline omits the stop bound`, `a lost claim is one more failed attempt`, `the stop
      deadline is read from the wall clock`; in `internal/run/replay.go`, `the sink loop does not
      fence`, and in `internal/run/execute.go`, `a run that lost its claim is recorded failed`, both
      with command `go test ./internal/run -count=1 -run TestFence`
- [x] T057 [US1] SC-104: `an unavailable backend admits the run` in `internal/guard/guard.go`, and
      `an unreachable backend falls back to the file lock` in `cmd/gronin/coordination.go`, both with
      command `go test ./cmd/gronin -count=1 -run TestAnUnreachableBackendRefuses`; `serve does not
      state its reach` and `serve exits when the backend is unreachable at startup` in
      `cmd/gronin/serve_cmd.go`, and `a single-host run records a cross-host reach` in
      `internal/run/filelock.go`, each with command
      `go test ./cmd/gronin -count=1 -run TestTheReachIsStated`
- [x] T058 [US1] SC-112: `the duration comparison is inverted`, `the stop bound is left out of the
      sum`, `coordination.json accepts an unknown key`, `a literal credential is accepted`, all in
      `internal/guard/config.go`, command `go test ./internal/guard -count=1 -run TestConfig`; `a
      renewal attempt is not bounded` and `an attempt counts as renewed when it is sent`, in
      `internal/guard/holder.go`, command `go test ./internal/guard -count=1 -run TestRenew`; and
      `a refused configuration still arms` in `cmd/gronin/serve_cmd.go`, command
      `go test ./cmd/gronin -count=1 -run TestServeRefusesDurationsThatCannotHold`
- [x] T059 [US1] SC-115: `a run that outlives the stop bound does not end the process`, in
      `internal/guard/holder.go`, command `go test ./internal/guard -count=1 -run TestTheStopBound`
- [x] T060 [US1] SC-116: `the decision is made under the caller's context`, in
      `internal/guard/guard.go`, command `go test ./internal/guard -count=1 -run TestTheDecisionBound`
- [x] T061 [US1] SC-117: `serve hands the guard its own clock instead of the tick`, in
      `cmd/gronin/serve_cmd.go`; and C13's `the last tick is not written`, `a tick equal to the
      recorded one is let through` and `every tick is refused once one is recorded` from T025,
      declared again. All with command `go test ./cmd/gronin -count=1 -run
      TestADelayedHostDoesNotRunATickAgain`. The two C13 mutants about the transaction stay killed
      by the contract alone, which chooses the interleaving: two deliveries in sequence cannot show
      that the tick is written in the step that takes the claim
- [x] T062 [US1] SC-118: `the guard hands over its own clock as the tick`, in `internal/guard/guard.go`,
      command `go test ./internal/guard -count=1 -run TestATickIsJudgedByItsSchedule`
- [x] T063 [US1] SC-108, this story's half: `gronin refusals drops the mechanism`, in
      `cmd/gronin/refusals_cmd.go`, command `go test ./cmd/gronin -count=1 -run TestRefusals`; and
      `a refusal is dated by its tick`, in `internal/guard/guard.go`, command
      `go test ./internal/guard -count=1 -run TestARefusalIsDatedByTheRuntime`
- [x] T064 [US1] The credential mutants for T042: `the client certificate is not presented` and
      `the password is not sent`, in `internal/guard/etcd/client.go`, command
      `go test ./internal/guard/etcd -count=1 -run TestCredentials`

**Checkpoint**: the MVP. A deployment of two hosts runs each scheduled tick once, and one with no
backend says it has the single-host guarantee. Shippable on its own: nothing waits yet, and a
colliding manual invocation is refused as the runtime core refuses it today.

---

## Phase 4: User Story 2 — A refused trigger is not lost (P2)

**Goal**: a trigger that will not come again — a manual invocation now — waits for the run it
collided with, one deep per state directory, and every way it can end is readable afterwards.

**Independent Test**: a playbook whose run sleeps, invoked by hand while it runs; a second run
starts once the first ends. Invoked twice more during the first run, exactly one further run
happens, and the extra invocation is recorded as refused.

### Tests for User Story 2

- [x] T065 [P] [US2] SC-105, `TestOneDeep` in `runtime/internal/guard/wait_test.go`: guards with distinct instances
      sharing one state directory and one fake — each manual invocation is a process of its own, and
      the slot has to hold across them (FR-111). While a run holds the claim, the playbook's
      scheduled tick fires and then three manual invocations arrive. The tick is refused `claim_held`
      and leaves the slot empty; the first invocation waits; the other two are refused
      `waiting_slot_full`; when the run ends, one run starts, and its `waiting_trigger_id` is the
      first invocation's. Counts alone cannot tell this from an implementation that lets the tick wait
      — it too produces one run and three refusals — so the test reads what each record refers to.
      No refused trigger leaves a gather trace (FR-102)
- [x] T066 [P] [US2] SC-106, the expiry half, `TestTheWaitExpires…` in `runtime/internal/guard/wait_expiry_test.go`: a
      waiting trigger passes its declared wait on the injected monotonic clock and produces no run and
      a `wait_expired` refusal; `wait: 0s` still enters the slot and expires at once; a backward step
      of the wall reading during the wait does not lengthen it (FR-118)
- [x] T067 [P] [US2] SC-113 and SC-106's restart half, `TestAKilledWaitIsDropped` in
      `runtime/cmd/gronin/wait_binary_test.go`,
      single-host: a first `gronin run` whose gather step sleeps, and a second that prints that it is
      waiting, both through `bintest.Start`. `gronin refusals` read now shows no `dropped` line — a
      live waiting trigger is not dropped. The second is killed with SIGKILL, not stopped: a record
      written on the way out would pass a graceful stop, and that is the implementation FR-127 refuses.
      `gronin refusals` then shows `dropped`, naming the trigger and when it was accepted. `serve` is
      started again after the first run ends: no run comes from the dropped trigger, and a further
      manual invocation finds the slot free. The absence alone is satisfied by a runtime that never
      started, so the positive assertions are what give this half a mutant
- [x] T068 [P] [US2] SC-110 and SC-114's surface, `TestAWaitingTriggerRunsTheEditedPlaybook` in
      `runtime/cmd/gronin/wait_edit_test.go`, single-host:
      while the second invocation waits, the playbook's gather step is rewritten to produce a
      different line; the run that starts from the waiting trigger records the edited line in its
      gathered input — the edit observable in the run's own output, not in a field nothing reads.
      `gronin show` on that run says it waited and for how long. A second case renames the playbook
      while a trigger waits: no run, and a `playbook_changed` refusal naming what the file now
      declares
- [x] T069 [P] [US2] SC-114, `TestAWaitedRun…` in `runtime/internal/guard/waited_test.go`: a trigger that waits and then
      runs leaves no refusal record, and its run's `waited_ms` is the monotonic interval from
      acceptance to start on the injected clock
- [x] T070 [P] [US2] `TestAWaitMeetsAnUnavailableBackend` in `runtime/internal/guard/wait_backend_test.go`: a waiting trigger that finds the
      backend unreachable when the claim frees is refused `backend_unavailable` and produces no run
      (FR-107)
- [x] T071 [P] [US2] In `runtime/internal/record/waiting_test.go`: two stores opened on one directory,
      as two processes would, cannot both accept a waiting row for one playbook, and accepting writes
      the row before returning (FR-127)
- [x] T072 [P] [US2] SC-109, the implemented half for `wait`, `TestGuardBlock…` in
      `runtime/internal/playbook/validate_test.go`: a playbook declaring `guard.wait` loads, one
      declaring an unknown `guard` key is still refused — the test tells an implemented key from an
      unimplemented one rather than merely observing a refusal
- [x] T073 [P] [US2] SC-108 for this story's mechanisms: `waiting_slot_full`, `wait_expired`,
      `playbook_changed` and `dropped` join the table in `runtime/cmd/gronin/refusals_cmd_test.go`

### Implementation for User Story 2

- [x] T074 [US2] `runtime/internal/record/waiting.go`: accept a waiting trigger in one write
      transaction that inserts the row only if no live `waiting` row exists for the playbook; set its
      outcome. Its `trigger_ref` is stored under the waiting trigger's own identifier, since no run
      exists yet to key it by
- [x] T075 [US2] `runtime/internal/guard/instance.go`: an exclusive advisory lock on
      `instances/<instance>.lock` in the state directory for the life of every process that can accept
      a waiting trigger; and the reconciliation that marks `dropped` every `waiting` row whose instance
      lock can be taken, writing its refusal record
- [x] T076 [US2] `runtime/internal/playbook/validate.go` and `runtime/internal/playbook/playbook.go`: lift
      `validateGuard`'s refusal of `wait`; the default is 30 minutes when the block or the key is absent
- [x] T077 [US2] `runtime/internal/guard/wait.go`: only a manual trigger refused with `ErrHeld` waits
      (FR-110); acceptance is recorded durably before the wait begins (FR-127); the wait ends on
      `Released` or on its expiry, enforced on the monotonic reading (FR-112); the trigger then
      re-reads its playbook from the file it was accepted from, through the load gate, and is refused
      `playbook_changed` if the file is gone, declares another name, or is refused (FR-121); it then
      acquires again, and runs, or ends with the matching outcome and refusal. A trigger that waits and
      runs writes no refusal (FR-117)
- [x] T078 [US2] `runtime/internal/run/execute.go`: a run started from a waiting trigger records
      `waiting_trigger_id` and `waited_ms` (FR-125)
- [x] T079 [US2] `runtime/cmd/gronin/run_cmd.go`, `runtime/cmd/gronin/serve_cmd.go`,
      `runtime/cmd/gronin/refusals_cmd.go` and `runtime/cmd/gronin/records_cmd.go`: `gronin run` says it
      is waiting, for whom and for how long at most, as contracts/cli.md shows; `serve` at startup and
      `gronin refusals` before it reads both run the reconciliation; `gronin show` prints how long a run
      waited

### Mutants for User Story 2

- [x] T080 [US2] SC-105: `a scheduled trigger waits like a manual one`, in `internal/guard/wait.go`,
      command `go test ./internal/guard -count=1 -run TestOneDeep`; `a second waiting row is accepted
      beside a live one`, in `internal/record/waiting.go`, same command
- [x] T081 [US2] SC-106: `a waiting trigger never expires` and `the wait is measured on the wall
      clock`, in `internal/guard/wait.go`, command `go test ./internal/guard -count=1 -run
      TestTheWaitExpires`; `a dead process's waiting row stays waiting`, in `internal/guard/instance.go`,
      command `go test ./cmd/gronin -count=1 -run TestAKilledWaitIsDropped`
- [x] T082 [US2] SC-110: `a waiting trigger runs the playbook as it stood on arrival` and `a waiting
      trigger follows a rename`, in `internal/guard/wait.go`, command
      `go test ./cmd/gronin -count=1 -run TestAWaitingTriggerRunsTheEditedPlaybook`
- [x] T083 [US2] SC-113: `acceptance is recorded only when the wait ends`, in `internal/guard/wait.go`;
      `refusals reads without reconciling`, in `cmd/gronin/refusals_cmd.go`; `a live process's waiting
      trigger is read as dropped`, in `internal/guard/instance.go`. All with command
      `go test ./cmd/gronin -count=1 -run TestAKilledWaitIsDropped`
- [x] T084 [US2] SC-114: `a trigger that waited and ran is also recorded as refused`, in
      `internal/guard/wait.go`, command `go test ./internal/guard -count=1 -run TestAWaitedRun`; `the
      time waited is not recorded`, in `internal/run/execute.go`, and `show omits the wait`, in
      `cmd/gronin/records_cmd.go`, both with command
      `go test ./cmd/gronin -count=1 -run TestAWaitingTriggerRunsTheEditedPlaybook`
- [x] T085 [US2] SC-109, SC-104 and SC-108 for this story: `the guard block refuses wait` in
      `internal/playbook/validate.go`, and `the guard block accepts an unknown key` in
      `internal/playbook/playbook.schema.json`, both with command
      `go test ./internal/playbook -count=1 -run TestGuardBlock` — scoped, because the test holding
      the two schema copies identical would otherwise kill the second for the wrong reason; `a waiting
      trigger that meets an unavailable backend keeps waiting`, in `internal/guard/wait.go`, command
      `go test ./internal/guard -count=1 -run TestAWaitMeetsAnUnavailableBackend`; and T063's
      `gronin refusals drops the mechanism`, confirmed still killed now that T073 has widened its table
      *After review*: T065's FR-102 clause is asserted where a gather step exists, in
      `TestAKilledWaitIsDropped`, whose third invocation is refused `waiting_slot_full` and leaves the
      trace where it was; `the guard decides after the gather stage` is declared a second time against
      it. `a second waiting row is accepted beside a live one` is renamed `a full slot is reported as an
      error, not a refusal`, which is what it shows (data-model.md, *The slot's two layers*). Added:
      `the file lock's waiter takes the lock to look` (`internal/run/filelock.go`,
      `TestTheWaitersPollCannotRefuseATick`); `a waiting trigger's playbook is read again without the
      load gate` (`cmd/gronin/deployment.go`, `TestAWaitingTriggerRunsTheEditedPlaybook/refused`); `a
      file read alone accepts a name another file declares` (`internal/playbook/load.go`,
      `TestLoadFileRefusesANameAnotherFileDeclares`); `a changed playbook keeps the claim the wait took`
      (`internal/guard/wait.go`, `TestAChangedPlaybookGivesTheClaimBack`); `a run that starts from a
      waiting trigger leaves its wait open` (`internal/record/runs.go`,
      `TestARunFromAWaitingTriggerEndsItsWaitInTheSameStep`); `a waiting trigger that ran is read as
      dropped` (`internal/guard/instance.go`, `TestReconcileDoesNotDropATriggerThatRan`). T084's `a
      trigger that waited and ran is also recorded as refused` moves to `internal/record/runs.go`, where
      the run and the end of its wait are now one transaction, and `every system clock reads from an
      origin of its own` is declared against the binary as well, with
      `TestAWaitingTriggerRunsTheEditedPlaybook/edited` and `TestAWaitExpiresOnTheSystemClock`.
      *After the second review*: the second declaration of `the guard decides after the gather stage`
      against `TestAKilledWaitIsDropped` is withdrawn — its early gather blocks the first invocation, so
      the test failed before reaching T065's FR-102 line — and replaced by `a trigger the guard refuses
      runs its gather step` (`internal/run/execute.go`, `TestAKilledWaitIsDropped/killed`), which fails
      on that line. Added: `a waiting trigger takes the claim before it reads its file`
      (`internal/guard/wait.go`, `TestATickDuringTheRereadIsNotRefusedByTheWaiter`); `a duration is not
      rounded to the second` (`internal/guard/duration.go`, `TestHumanDuration`); `etcd's released
      returns while the claim is held` (`internal/guard/etcd/etcd.go`, `TestEtcdContract/C10`, the
      half of C10 a watching backend owes). `a file read alone accepts a name another file declares`
      becomes `a file read alone accepts what its directory refuses`, and `a trigger that waited and ran
      is also recorded as refused` follows CreateRun's reworded error

**Checkpoint**: a manual invocation that collides is deferred rather than lost, one deep, and every
way a wait ends — ran, expired, changed, unreachable, dropped — is readable.

---

## Phase 5: User Story 3 — A playbook that fires too often is held back (P3)

**Goal**: a declared limit of runs per window, judged across the deployment before any claim is
taken, discarding rather than deferring what it refuses.

**Independent Test**: a limit of two runs per minute on a playbook with a short run, invoked four
times inside one minute: two runs and two refusals.

### Tests for User Story 3

- [x] T086 [P] [US3] C8 and C9 join `runtime/internal/guard/guardtest/contract.go`, for the fake, the
      etcd adapter and the file lock. C8 fills every slot, calls `Acquire`, and checks that a second
      contender can still take the claim with the limit lifted; C9 takes the limit's runs, releases
      each claim, and asserts the next `Acquire` is refused for rate
- [x] T087 [P] [US3] SC-107, `TestTheRateLimit…` in `runtime/internal/guard/rate_test.go`: a limit of N runs per window,
      triggered N+2 times inside it, produces N runs and two `rate_limited` refusals naming the limit,
      and neither refused trigger enters the waiting slot, a manual one included (FR-116); once the
      fake's clock moves the window past the earlier runs, a trigger runs; with no limit declared, none
      is refused for rate and non-concurrency still holds; a trigger waiting while other triggers fill
      the window is refused `rate_limited` when the claim frees (FR-124). Two guards with separate
      record stores and one fake share one window: counted per host, the deployment would allow twice
      the declared runs
- [x] T088 [P] [US3] `TestFileLockRate…` in `runtime/internal/run/filelock_rate_test.go`: the single-host window is the
      record store's count of scheduled and manual runs of the playbook started within `rate.per`, on
      the wall reading of an injected clock; replays and resumes are not counted and take no slot
- [x] T089 [P] [US3] SC-109, the implemented half for `rate`, `TestGuardBlock…` in
      `runtime/internal/playbook/validate_test.go`: `guard.rate` now loads; `per: 30s` is still refused
- [x] T090 [P] [US3] SC-108 for this story: `rate_limited` joins the table in
      `runtime/cmd/gronin/refusals_cmd_test.go`, its detail naming the limit

### Implementation for User Story 3

- [x] T091 [US3] `runtime/internal/guard/guardtest/fake.go`: rate slots, taken with the claim or not at
      all, each freeing after `per` on the fake's clock and not on release
- [x] T092 [US3] `runtime/internal/guard/etcd/etcd.go`: slots at `<prefix>/rate/<name>/<i>`, each on a
      lease of its own lasting `per` and never renewed, taken in the same transaction as the claim and
      the last tick. When the tick is refused and the window is full as well, the refusal is
      `ErrTickRan` (C13)
- [x] T093 [US3] `runtime/internal/run/filelock.go`: the window counted in the record store under the
      lock, per T088
- [x] T094 [US3] `runtime/internal/guard/guard.go` and `runtime/internal/guard/wait.go`: the playbook's
      limit is passed to every `Acquire`, a waiting trigger's second attempt included; `ErrRateLimited`
      is a `rate_limited` refusal and never a wait; replay and resume pass no limit. And
      `runtime/internal/playbook/validate.go`: lift `validateGuard`'s refusal of `rate`

### Mutants for User Story 3

- [x] T095 [US3] C8 and C9, command scoped to each subject's contract test: `the claim is taken before
      the slots are checked` and `release frees the rate slot`, in `internal/guard/guardtest/fake.go`,
      `internal/guard/etcd/etcd.go`, and — for the second, as `the window counts only runs still in
      flight` — `internal/run/filelock.go`
- [x] T096 [US3] SC-107: `a trigger refused for rate waits`, in `internal/guard/guard.go`; `a waiting
      trigger is not judged against the limit again`, in `internal/guard/wait.go`; `the window is counted
      on this host even with a backend`, in `internal/guard/guard.go` — all with command
      `go test ./internal/guard -count=1 -run TestTheRateLimit`; `the single-host window ignores its
      duration` and `a replay takes a rate slot`, in `internal/run/filelock.go`, command
      `go test ./internal/run -count=1 -run TestFileLockRate`
- [x] T097 [US3] SC-109 and SC-108 for this story: `the guard block refuses rate`, in
      `internal/playbook/validate.go`, command `go test ./internal/playbook -count=1 -run
      TestGuardBlock`; and T063's `gronin refusals drops the mechanism`, confirmed still killed with
      T090's `rate_limited` line in its table

**Checkpoint**: every mechanism of the guard is in place, and each refusal it can write is readable
through `gronin refusals`.

---

## Phase 6: Polish

- [ ] T098 [P] `specs/001-runtime-core/data-model.md`: the Run gains `waiting_trigger_id`,
      `waited_ms`, `claim_reach`, `claim_token` and the status `claim_lost`, as this feature's
      data-model.md records it will when these tasks land
- [ ] T099 [P] `docs/playbook-format.md` documents the `guard` block, and `docs/architecture.md`'s guard
      section describes the stage as built — the coordinator, the file lock's single-host reach, the
      waiting slot — rather than as planned
- [ ] T100 [P] `README.md` and `runtime/README.md`: the guard is no longer refused, and a deployment
      wanting the cross-host guarantee runs etcd beside the binary — stated as plainly as the plan's
      complexity table states it, with `coordination.json` pointing at contracts/cli.md rather than
      restating it
- [ ] T101 SC-111: `scripts/check-mutation.py --self-test`, then `scripts/check-mutation.py`, report
      zero survivors with every mutant above declared; and each success criterion from SC-101 to
      SC-118 has at least one declared mutant whose command is scoped to that criterion's own test, per
      the table below. Run the suite through `scripts/run-suite.sh` under its declared timeout — the
      binary tests of T027 and T028 wait on real minute boundaries — and let the repeat workflow run it
      against the unchanged tree, since every mechanism here is a race or a clock and a flake is a
      failure
- [ ] T102 Follow [quickstart.md](./quickstart.md) on two real hosts against an etcd that is not
      embedded. It is the one place where hosts means separate kernels, a validation rather than a
      gate, and what it finds that the suite could not becomes a task here rather than a note

---

## Success criteria coverage

| Criterion | Test tasks | Mutant tasks |
| --------- | ---------- | ------------ |
| SC-101 | T027, T039, T040, T018, T020, T023 | T054, T024, T025, T026 |
| SC-102 | T029, T020 | T055, T025 |
| SC-103 | T030, T031, T020 | T056, T024, T025 |
| SC-104 | T032, T070 | T057, T085 |
| SC-105 | T065, T071 | T080 |
| SC-106 | T066, T067 | T081 |
| SC-107 | T086, T087, T088 | T095, T096 |
| SC-108 | T041, T073, T090 | T063, T085, T097 |
| SC-109 | T011, T039, T072, T089 | T054, T085, T097 |
| SC-110 | T068 | T082 |
| SC-111 | T101 | every task in the column above, and T004 |
| SC-112 | T033, T034, T035 | T058 |
| SC-113 | T067 | T083 |
| SC-114 | T068, T069 | T084 |
| SC-115 | T036 | T059 |
| SC-116 | T037, T020 | T060, T025 |
| SC-117 | T028, T020 | T061, T025 |
| SC-118 | T038, T018, T020 | T062, T024, T025 |

---

## Dependencies & Execution Order

- **Setup (Phase 1)** → **Foundational (Phase 2)** → the stories. T003 lands before any guard code so
  that the first test-support import to leak into a shipped package fails where it is written.
- **Within Phase 2**: T005 → T006 → T007, T008 → T009. T012 → T013 → T016, T017 → T018. T014 → T015.
  T019 needs T012; T020 needs T014, T017 and T019; T021 needs T019. T022 needs T008 and T012; T023
  needs T017 and T022. T024-T026 come last in the phase, because a mutant is declared against code
  that exists. T010 → T011 are independent of the rest of the phase.
- **US1 (Phase 3)** needs all of Phase 2. T043 → T044; T045 needs T043; T046 needs T045; T047 needs
  T045 and T046; T048 needs T043, T044 and T047; T049-T052 need T048; T053 needs T048. The mutant
  tasks T054-T064 follow the implementation they mutate.
- **US2 (Phase 4)** needs US1: there is nothing to wait for until a claim can be refused. T074 → T077;
  T075 → T079; T076 is independent; T077 → T078 → T079.
- **US3 (Phase 5)** needs US1's decision (T045) and, for FR-124, US2's wait (T077). Without US2 it
  can still land except for T087's waiting case and T096's `a waiting trigger is not judged against
  the limit again`, which follow T077.
  Landing without US2 is not working beside it: T072 and T089 both edit
  `runtime/internal/playbook/validate_test.go`, and T073 and T090 both edit
  `runtime/cmd/gronin/refusals_cmd_test.go`, so where both stories are in flight those pairs run US2
  first.
- **Polish (Phase 6)** follows the stories it documents. T101 is last among the tasks that change the
  tree, because it is the audit of every mutant declared before it.

### Parallel opportunities

Within Phase 1, T002 and T003. Within Phase 2, T007 and T008; T010, T012 and T014 against each other.
Within each story, the tests marked `[P]` are in files of their own. The implementation tasks are not
marked: each builds on the one before it, and several touch `guard.go` or `execute.go`. **The mutant
tasks are never parallel**: every one of them edits `runtime/testdata/mutations.json`.

### Parallel example: User Story 1

```text
# Once Phase 2 is done, the tests of US1 are independent files:
T027 cmd/gronin/guard_binary_test.go      T033 internal/guard/config_test.go
T028 cmd/gronin/tick_binary_test.go       T034 internal/guard/renew_test.go
T029 internal/guard/etcd/crash_test.go    T036 internal/guard/stopbound_test.go
T030 internal/guard/holder_test.go        T037 internal/guard/decision_test.go
T031 internal/run/fence_test.go           T038 internal/guard/tick_test.go
T032 cmd/gronin/coordination_cmd_test.go  T041 cmd/gronin/refusals_cmd_test.go
```

### Parallel example: User Story 2

```text
T065 internal/guard/wait_test.go          T069 internal/guard/waited_test.go
T066 internal/guard/wait_expiry_test.go   T070 internal/guard/wait_backend_test.go
T067 cmd/gronin/wait_binary_test.go       T071 internal/record/waiting_test.go
T068 cmd/gronin/wait_edit_test.go         T072 internal/playbook/validate_test.go
```

### Parallel example: User Story 3

```text
T086 internal/guard/guardtest/contract.go  T088 internal/run/filelock_rate_test.go
T087 internal/guard/rate_test.go           T089 internal/playbook/validate_test.go
```

---

## Implementation Strategy

**The MVP is User Story 1**: Phases 1, 2 and 3. It is the whole reason the feature exists — without
it the runtime cannot be deployed twice without doubling every scheduled run, and doubling a run
doubles its side effects. It ships without US2 and US3 because nothing in it depends on them: a
colliding manual invocation is refused as it is today, and `guard.rate` and `guard.wait` stay refused
at load rather than accepted and ignored.

Then US2, which makes the refusal survivable for a trigger that will not come again and is what the
webhook specification builds on. Then US3, which earns its priority when triggers first come from
outside.

## Notes

- The effect a binary test counts is a line a gather step appends to a file the test owns. A gather
  step runs only once the guard has admitted the run (FR-102), so the line is evidence of a run, not
  of a log line.
- Every test that waits for something to lapse polls with a deadline. None sleeps a fixed time and
  then asserts: a sleep shorter than the thing it waits for passes without the thing ever happening.
- Every time bound here — the decision, the renewal, the stop — is tested against a subject that
  exceeds it. A bound is invisible against a subject that is prompt.
- Commit per task or per logical group. Stop at any checkpoint.
