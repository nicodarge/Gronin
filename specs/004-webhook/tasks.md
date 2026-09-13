---

description: "Task list for the webhook trigger"
---

# Tasks: Webhook Trigger

**Input**: Design documents from `/specs/004-webhook/`

**Prerequisites**: [plan.md](./plan.md), [spec.md](./spec.md), [research.md](./research.md),
[data-model.md](./data-model.md), [contracts/](./contracts/), [quickstart.md](./quickstart.md) — and
the guard, built first ([specs/002-guard/tasks.md](../002-guard/tasks.md)).

**Tests**: included, and not optional here. SC-330 makes a test that fails when its behaviour is
removed a success criterion in its own right, and most of what this feature guarantees is an absence
— no run, no stored body, not found — which an ingress that never started asserts just as well as a
working one. So every success criterion has a test task and a mutant registered with
`scripts/check-mutation.py`, a mutant counts only once it is declared in
`runtime/testdata/mutations.json` and killed, and every test asserting an absence asserts the
positive case beside it in the same run.

**Organization**: grouped by user story. US1, US2 and US3 are all P1, so among them the order is
what each needs from the others: US3 first, because its load-time rules and value checks are what make a
webhook playbook safe to load at all and need no listener to test; then US1, the ingress and the
delivery pipe; then US2, which proves and records the refusals US1's ingress already makes. US4, the
only P2, bounds the listener's cost. A webhook trigger stays refused at load until US3's last task
lifts it (T031), because a trigger accepted before its payload rules exist is the declared-but-unapplied
state Principle I refuses.

**Built on the guard.** The guard's waiting slot is what keeps a delivery that collides with a run
from being lost, and its rate limit is what bounds a sender that fires too often (spec.md,
*Assumptions*). So US1 onwards needs every story of the guard landed, not only its MVP. The guard's
tasks are cited here as **G** followed by their number — G077 is the guard's task numbered 077 — so
that `scripts/check-spec-refs.py`, which resolves task identifiers within one feature directory,
does not read them as this file's own. They are cited as they stand in
[specs/002-guard/tasks.md](../002-guard/tasks.md); if that list is renumbered, the
citations here move with it.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an incomplete task)
- **[Story]**: the user story the task serves

## Path Conventions

Paths follow [plan.md](./plan.md)'s structure, under the runtime core's module at `runtime/`:

- `internal/ingress` — the listener's options and bounds, the route, the signature, the identity,
  the acceptance's timing, the hand-off, the drop reconciliation and the refusal counting. It imports
  `record`, `playbook`, `sources` and the guard's clock, and never `api` or `run`: the hand-off
  reaches the guard and the executor through an interface `serve` fills in. T003 is what notices if
  the `api` half of that stops being true.
- `internal/ingress/ingresstest` — signing, stalling senders, a byte-counting listener and a
  store write-lock holder. Imported by `_test.go` files only.
- `internal/sources` — `sources.json`, beside `internal/mcpcatalog`, whose pattern it follows.
- `internal/record` — the acceptance transaction itself lives in `accept.go`, because it is one SQL
  transaction and the store owns its database; `ingress` owns only when to stop waiting for it.
- `internal/playbook` — the trigger's typed shape, its load-time rules, and `values.go`, the one
  function every path uses to extract and check declared values: a delivery, a manual invocation, and
  a waiting trigger coming to run.
- `internal/api`, `internal/run`, `internal/sink`, `internal/config`, `cmd/gronin` — as named in
  plan.md, plus the guard's `internal/guard` where a webhook trigger has to be one of its kinds.

One file the plan does not name: `cmd/gronin/webhook.go` holds the dispatcher `serve` hands the
ingress — the loaded playbooks, the guard and the executor behind the interface `ingress` declares —
because `cmd/gronin` is the one package allowed to know all three.

## Where this rebases over the guard

The guard is being implemented on other branches. These are the places a webhook branch will meet its
code, named so a conflict there is expected rather than discovered:

- **Migration numbering.** T005 takes the next free number in
  `runtime/internal/record/migrations/` when it lands, read from the directory then rather than
  predicted here: the guard adds one (G005) and the retrieve feature's task list adds another, and
  which of the three lands first is not knowable from here. The number matters because `schema.go`
  applies migrations by name, so a file renumbered after it has been applied somewhere is re-applied
  there and skipped elsewhere. T005 also alters two tables G005 creates, so it cannot land before it.
- **`cmd/gronin/serve_cmd.go`.** The guard adds its reach line, its duration refusal, its waiting
  reconciliation and a fire function that hands the guard its `dueAt` (G049, G079). T030 and T054
  add the webhook refusal, the ingress listener, the delivery reconciliation and the dispatcher
  after the API listener, and T075 the port check. Rebase onto the guard's version and keep its order:
  refusals, then reconciliation, then listeners.
- **The record store.** `record/store.go`, `record/runs.go` (G006), `record/refusals.go` (G007) and
  `record/waiting.go` (G074) each gain a field in T006. `runtime/testdata/mutations.json` is
  append-only on both branches; conflicts there are resolved by keeping both sides.
- **`internal/run/execute.go` and `internal/run/replay.go`.** G047 moves `Begin` behind the guard and
  mints the run identifier first. T027 and T053 edit the same functions and are written against that
  shape, not against today's `Execute`.
- **The playbook schema and gate.** G010 replaces the reserved `guard` property and adds
  `validateGuard`; T014 replaces the `trigger` property and adds `validateTrigger` beside it. Both
  edit `specs/001-runtime-core/contracts/playbook.schema.json`, its embedded copy, `validate.go`, and
  the tables in `parse_test.go` and `validate_test.go`.
- **`internal/guard`.** T052 edits `coordinator.go` (G012), `guard.go` (G045), `wait.go` (G077) and
  `internal/run/filelock.go` (G093) to make `webhook` a trigger kind the guard treats like `manual`.

## Mutants

Every mutant is declared in `runtime/testdata/mutations.json` with `tree: ".."`, the file it mutates
relative to `runtime/`, and a command. Where another test in the same package would kill the mutant
too, the command is scoped with `-run` to the test the task names, so that the harness shows *that
test* can fail; the name a mutant task gives to `-run` is the name the test task it cites gives its
test. The find text is written when the code exists, and the harness refuses one it cannot find
exactly once — so when a later task changes a mutated line, that task says so and re-declares the
mutant against the new text.

---

## Phase 1: Setup

- [x] T001 [P] `runtime/internal/ingress/doc.go` and `runtime/internal/sources/doc.go`: the two
      packages, each saying what it holds and, for `ingress`, that it serves deliveries and nothing
      else and shares only the record store with `api`
- [x] T002 [P] `runtime/internal/ingress/ingresstest/ingresstest.go`: `Sign(secret, prefix, body)`,
      the header value a correct sender would send; a raw-TCP sender that sends headers or a body a
      byte at a time and can stop partway; a `CountingListener` wrapping a `net.Listener` so a test
      reads how many bytes the server consumed from each connection; and `HoldWrites(t, recordDir)`,
      which takes the record store's write lock from a connection of its own and returns the release.
      Imported only by `_test.go` files
- [x] T003 `TestTheIngressAndTheAPIShareOnlyTheRecord` in `runtime/internal/ingress/imports_test.go`:
      parses the imports of every non-test file in `internal/ingress` and `internal/api` with
      `go/parser` and asserts neither imports the other, and that no non-test file anywhere under
      `runtime/` imports `ingresstest`. It also asserts that at least one file was parsed in each
      package and that `api`'s import of `internal/record` was seen, because a check over nothing
      passes. Two packages serving the two route sets are what keep a future change from putting
      them back behind one listener without anyone deciding to (plan, *Project Structure*)
- [x] T004 Register T003's mutant, `the ingress imports the operator API`: a blank import of
      `internal/api` added to `internal/ingress/doc.go`; command
      `go test ./internal/ingress -count=1 -run TestTheIngressAndTheAPIShareOnlyTheRecord`

**Checkpoint**: two empty packages and a test that keeps them apart.

---

## Phase 2: Foundational (blocks every story)

Needs the guard's record store changes (G005, G006, G007, G074) and its schema change (G010). Nothing
here needs the guard's decision or its wait.

### The record store

- [ ] T005 `runtime/internal/record/migrations/<next free number>_webhook.sql`: the `deliveries`,
      `delivery_identities`, `handoffs`, `delivery_refusals` and `ingress_refusal_counts` tables per
      [data-model.md](./data-model.md), with `delivery_identities` keyed on `(source, identity)` and
      `ingress_refusal_counts` on `(interval_start, reason, source_bucket)`; and `delivery_id` added to
      `runs` and to the guard's `refusals` and `waiting_triggers`. A new file rather than an edit, for
      the reason G005 gives; its number is read from the directory when it lands, never predicted here
- [ ] T006 `runtime/internal/record/store.go`, `runtime/internal/record/runs.go`,
      `runtime/internal/record/refusals.go` and `runtime/internal/record/waiting.go`: the trigger kind
      `webhook`; `DeliveryID` on the Run, the guard's refusal record and its waiting trigger, written
      and read by the functions that already write and read each
- [ ] T007 [P] `runtime/internal/record/deliveries.go`: the Delivery and HandOff types and their
      states — the delivery's `accepted`, `waiting`, `handed_off`, `dropped` and `unbound`, the
      hand-off's `pending`, `waiting`, `handed_off`, `refused` and `dropped`
      ([data-model.md](./data-model.md), *Delivery* and *Hand-off*); `GetDelivery`, `ListDeliveries`
      most recent first, `HandOffsOf`, `SetHandOffState` and `SetDeliveryState`. A
      delivery's body goes through the blob store under the delivery's identifier, so the blob
      store's own identifier check and the redactor apply unchanged
- [ ] T008 [P] `runtime/internal/record/delivery_refusals.go`: `AddDeliveryRefusal`, one row with its
      body when there is one; `CountUnauthenticated(interval, reason, bucket, peer, at)`, one upsert
      adding to the row for its key; and the two listings. Every timestamp in UTC
- [ ] T009 `TestWebhookRecord…` in `runtime/internal/record/webhook_test.go`: a store created under the
      guard's schema migrates, and its existing runs, refusals and waiting triggers read back with
      `delivery_id` empty; a delivery, its hand-offs and a refusal round-trip; counting one key twice
      leaves one row whose count is two, and a second reason a second row; the `testsecret` sentinel
      written into a refused body and a refusal's value reaches neither `record.db` nor the blob
      directory

### Sources

- [ ] T010 [P] `runtime/internal/sources/sources.go`: `Load(stateDir, cfg)` reads `sources.json` as
      [contracts/cli.md](./contracts/cli.md) specifies and refuses, all at once and naming the source
      and field: an unknown key, a name that is not a slug, a missing or malformed signature header, a
      secret that is a literal, names an unconfigured key, or names a key not marked secret, an
      identity that is not a JSON Pointer, and a replay window that does not parse or is not positive.
      `Names()`; `Summary(name)`, which never resolves the secret; and the resolved secret for the
      ingress, read only when the ingress is built
- [ ] T011 `TestSources…` in `runtime/internal/sources/sources_test.go`: every refusal T010 names,
      probed one at a time on an otherwise valid file, so each one is shown refused for its own reason;
      the three shapes in contracts/cli.md accepted, with the defaults filled in; an absent file is no
      source rather than an error; and no `Summary` contains the sentinel set as a source's secret
- [ ] T012 `runtime/cmd/gronin/sources_cmd.go`, `runtime/cmd/gronin/root.go` and
      `runtime/cmd/gronin/deployment.go`: `gronin sources list`, beside `gronin mcp list` and like it
      read-only; the deployment opens the source catalogue beside the MCP catalogue; `capabilities`
      passes the configured names as `playbook.Deployment.Sources`, a field this task adds to
      `runtime/internal/playbook/validate.go` and nothing reads until T025
- [ ] T013 SC-316, `TestSourcesAreListedWithoutTheirSecret` in `runtime/cmd/gronin/sources_cmd_test.go`:
      through the built executable, the sentinel is set with `gronin config set --secret` on standard
      input (`bintest.RunWithStdin`), `sources.json` references it, and `gronin sources list` names
      every source and holds no sentinel. The same value given as an argument is refused, which the
      runtime core already does; it is asserted here because SC-316 fails if that ever regresses, and
      `scripts/run-suite.sh`'s scan of the suite's output for the sentinel covers every other line

### The trigger's shape

- [ ] T014 [P] `runtime/internal/playbook/playbook.go` gains `Trigger.Source` and `Trigger.Values`
      (`at`, `pattern`, `max_length`). The `trigger` property of
      `specs/001-runtime-core/contracts/playbook.schema.json` is replaced by the content of
      [contracts/webhook-trigger.schema.json](./contracts/webhook-trigger.schema.json), and
      `runtime/internal/playbook/playbook.schema.json` with it — `TestTheEmbeddedSchemaIsTheContract`
      holds the two identical. `runtime/internal/playbook/testdata/schema/refused/trigger-type-unknown.yaml`
      names `webhook` as its unknown type today and moves to `carrier-pigeon`. New fixtures under
      `testdata/schema/`: accepted `webhook-with-values.yaml`; refused `webhook-without-source.yaml`,
      `webhook-with-schedule.yaml`, `cron-with-source.yaml`, `webhook-value-without-pattern.yaml`,
      `webhook-value-without-max-length.yaml`, `webhook-value-pointer-without-slash.yaml`. In
      `runtime/internal/playbook/validate.go`, a new `validateTrigger` refuses `trigger.type: webhook`
      by field as declared but not yet applied, until T031 lifts it
- [ ] T015 `TestTheSchemaAcceptsAndRefusesTheProbeCorpus`'s table in
      `runtime/internal/playbook/parse_test.go` gains T014's fixtures, and `TestAWebhookTriggerIsHeld`
      in `runtime/internal/playbook/validate_test.go` asserts the hold by field name, so that lifting
      it is a one-line change to the table

### Mutants for the foundation

- [ ] T016 Register, in `internal/sources/sources.go` with command
      `go test ./internal/sources -count=1` unless named: `a literal source secret is accepted`, `a
      source secret may name a value not marked secret`, `sources.json accepts an unknown key`, `a
      replay window of zero is accepted`, and `the source summary resolves the secret`, the last with
      command `go test ./cmd/gronin -count=1 -run TestSourcesAreListedWithoutTheirSecret`. In
      `internal/record/delivery_refusals.go`, `an unauthenticated refusal inserts a row per request`,
      command `go test ./internal/record -count=1 -run TestWebhookRecord`. In
      `internal/playbook/playbook.schema.json`, `the trigger schema lets a cron trigger carry a
      source`, command `go test ./internal/playbook -count=1 -run
      TestTheSchemaAcceptsAndRefusesTheProbeCorpus` — scoped, because the test holding the two schema
      copies identical would otherwise kill it for the wrong reason. In `internal/playbook/validate.go`,
      `the gate lets a webhook trigger through before its rules exist`, command
      `go test ./internal/playbook -count=1 -run TestAWebhookTriggerIsHeld`; T031 retires it

**Checkpoint**: the record has somewhere to put deliveries and refusals, a deployment can declare
sources and list them without their secrets, and the schema describes a webhook trigger the gate
still refuses.

---

## Phase 3: User Story 3 — A payload cannot steer the run (P1)

**Goal**: a webhook playbook declares every value it takes, each held to a pattern and a length; a
declared value reaches the agent only as data in `trigger.json` and a gather step only as one word
that cannot become an option; the prompt and the sinks cannot reference the payload; and a delivery
cannot choose which playbooks run.

**Independent Test**: load a corpus of webhook playbooks that reference the payload where they may
not, and confirm each is refused at load with the field named. Hand a delivery whose declared values
violate their shapes, and one carrying a sentinel in an undeclared field, to the hand-off with a real
executor and the stub agent: the first produces no run, and the sentinel appears nowhere a run can
see.

Needs Phase 2, and G047's shape of `Execute`. No listener: the hand-off is driven from a delivery the
test writes into the record.

### Tests for User Story 3

- [ ] T017 [P] [US3] SC-315, the corpus, in `runtime/testdata/playbooks/hostile/`:
      `webhook-prompt-reference.yaml` with its own `webhook-prompt.md` holding `${trigger.alertname}`,
      `webhook-sink-reference.yaml` (a discord webhook and a github repository each from
      `${trigger.x}`), `webhook-unconfigured-source.yaml`, `webhook-undeclared-gather-reference.yaml`,
      `webhook-pattern-does-not-compile.yaml` and `webhook-data-file-name.yaml` (a gather step writing
      `trigger.json`). Each is pinned to its field and reason in
      `TestTheHostileCorpusIsRefusedForItsOwnReason` in `runtime/internal/playbook/validate_test.go`,
      whose `deployment()` gains the source `alerts`; the unconfigured-source refusal names `alerts` as
      what is configured. And `TestTheGateRefusesAWebhookValueTheSchemaWouldHave`, for a value without
      a pattern and one without a length, on typed playbooks the schema never reads — as
      `TestTheGateRefusesAReservedBlockThatReachesIt` does for the reserved blocks — so the gate's own
      copy of each rule is shown to hold
- [ ] T018 [P] [US3] SC-315, the sink half, `TestASinkRefusesATriggerReferenceForAWebhookPlaybook` in
      `runtime/internal/sink/sink_test.go`: with the build option T026 adds, a `${trigger.x}` in a
      discord webhook, a slack webhook, a github repository, token and label is each refused when the
      sink is built; the same declarations build for a manual playbook, which may still interpolate
      the trigger everywhere but the label, as today
- [ ] T019 [P] [US3] SC-317 and SC-319, `TestDeclaredValues…` and `TestALeadingDash…` in
      `runtime/internal/playbook/values_test.go`: from a body, a value absent, `null`, an object, an
      array, one code point over its length, and matching its pattern only in part is each refused
      naming the playbook and the value; a value exactly its length in multi-byte code points, a
      string, a boolean and a number are accepted, the number as the literal the body holds
      (`9007199254740993` stays itself). A value beginning with `-` is refused when a gather step
      references it and accepted when none does. The same function on `--trigger` strings refuses the
      same values with the same message
- [ ] T020 [P] [US3] SC-317 and SC-320, `TestAHandOff…` in `runtime/internal/ingress/handoff_test.go`:
      a delivery written into the record for a source bound to two playbooks, a third loaded playbook
      bound to another source, and a recording dispatcher. A value the first playbook refuses leaves a
      delivery refusal naming the playbook and value and dispatches nothing for it, while the second is
      dispatched once with exactly its declared values; a body carrying `"playbook": "<the third>"`
      dispatches only the two bound ones
- [ ] T021 [P] [US3] SC-318, `TestUndeclaredContentReachesNoRun` in `runtime/internal/ingress/reach_test.go`:
      the hand-off, a real executor with the stub agent, and an `httptest` sink. The delivery declares
      one value, `marker-ok`, and carries the sentinel in an undeclared field. A gather step writes its
      environment and a listing of every file in the working directory, whole, into its output. The
      sentinel is found in the delivery's body blob and nowhere in the gathered inputs, the recorded
      prompt, the stub agent's receipt or the sink's received bodies; `marker-ok` is found in the
      `trigger.json` gathered input, which is what makes the absence mean something
- [ ] T022 [P] [US3] SC-321, `TestAManualWebhookRunIsHeldToItsDeclarations` in
      `runtime/cmd/gronin/run_cmd_test.go`: through the built executable, `gronin run` of a webhook
      playbook with a value its pattern refuses exits non-zero, prints the message T019 asserts for the
      same value, and records no run; an undeclared `--trigger` name is refused naming it; a valid value
      runs and the run records `trigger.json`
- [ ] T023 [P] [US3] `TestServeRefusesAWebhookPlaybookWithNoIngress` in
      `runtime/cmd/gronin/serve_webhook_test.go`: `gronin serve` with a webhook playbook exits non-zero
      before arming anything, naming the playbook and the missing `--ingress-address`; the same
      directory with that playbook removed arms and serves. FR-305's refusal, before there is an ingress
      to configure; T054 narrows it and T066 tests the whole requirement

### Implementation for User Story 3

- [ ] T024 [US3] `runtime/internal/playbook/values.go`: `Extract(trigger, body)` reading each declared
      pointer from a body decoded with number literals kept (`json.Decoder.UseNumber`), and
      `Check(book, values)`, applying FR-322's single value, length in code points and anchored pattern,
      and FR-326's leading dash for names a gather step references — both returning problems that name
      the playbook and the value. `runtime/internal/config/shell.go` exports `TriggerReferences(line)`,
      the names a gather line references, which `Check` and the gate both use
- [ ] T025 [US3] `runtime/internal/playbook/validate.go`: `validateWebhook` — the source is in
      `Deployment.Sources`, naming the configured ones when not (FR-310); every value has its three
      fields, a pointer that parses and a pattern that compiles (FR-322); no `${trigger.` in the prompt
      body (FR-324) or in any sink field (FR-325); a gather step references only declared values; no
      gather step writes `trigger.json`
- [ ] T026 [US3] `runtime/internal/sink/build.go`: `BuildOptions` gains the option that refuses any
      field holding `${trigger.` when the sink is built, set for every webhook playbook (FR-325)
- [ ] T027 [US3] `runtime/internal/run/execute.go` and `runtime/internal/run/replay.go`: for a webhook
      playbook, `trigger.json` is written into the working directory before the gather steps and
      recorded as a gathered input (FR-324); the values reach `BindShell` and nothing else, the prompt is
      interpolated with no trigger values, and the sinks are built with T026's option on every path —
      run, replay and resume
- [ ] T028 [US3] `runtime/internal/ingress/handoff.go`: the `Dispatcher` interface, and `HandOff`,
      which for each playbook bound to the delivery's source — found by the source in the loaded set,
      never by anything in the body (FR-321) — extracts and checks the values, records a refusal
      through T008 and marks the hand-off `refused` when they fail, and otherwise dispatches exactly the
      declared values. What the dispatcher returns decides the hand-off: a run or a guard refusal other
      than `dropped` marks it `handed_off` or `refused`, and a wait the guard accepted marks it
      `waiting`, which is not a decision; the delivery follows its hand-offs — `waiting` while any of
      them is, `handed_off` once all are decided ([data-model.md](./data-model.md), *Hand-off*)
- [ ] T029 [US3] `runtime/cmd/gronin/run_cmd.go`: for a webhook playbook, `--trigger` values go through
      `Check` before anything runs, and an undeclared name is refused (FR-327)
- [ ] T030 [US3] `runtime/cmd/gronin/serve_cmd.go`: a loaded webhook playbook refuses startup before
      anything is armed (FR-305, while no ingress exists)
- [ ] T031 [US3] `runtime/internal/playbook/validate.go`: `validateTrigger` stops refusing
      `type: webhook`, and T015's table flips that row to accepted. T016's mutant `the gate lets a
      webhook trigger through before its rules exist` loses its target and is removed in the same change,
      replaced by `the gate refuses a webhook trigger that passes every rule`, command
      `go test ./internal/playbook -count=1 -run TestAWebhookTriggerIsHeld` — the test now tells an
      applied trigger from an unapplied one rather than observing a refusal

### Mutants for User Story 3

- [ ] T032 [US3] SC-315, in `internal/playbook/validate.go` with command
      `go test ./internal/playbook -count=1 -run TestTheHostileCorpusIsRefusedForItsOwnReason` unless
      named: `a webhook prompt may reference the payload`, `a webhook sink may reference the payload`,
      `a webhook trigger may name an unconfigured source`, `a gather step may reference an undeclared
      value`, `a pattern that does not compile passes the gate`, `a gather step may write the data
      file's name`; `a value without a pattern passes the gate` and `a value without a length passes the
      gate`, command `go test ./internal/playbook -count=1 -run
      TestTheGateRefusesAWebhookValueTheSchemaWouldHave`; in `internal/playbook/playbook.schema.json`, `the
      trigger schema lets a value omit its pattern`, command `go test ./internal/playbook -count=1 -run
      TestTheSchemaAcceptsAndRefusesTheProbeCorpus`; in `internal/sink/build.go`, `the sink resolves a
      trigger reference for a webhook playbook`, command
      `go test ./internal/sink -count=1 -run TestASinkRefusesATriggerReferenceForAWebhookPlaybook`
- [ ] T033 [US3] SC-317, SC-319 and SC-320: in `internal/playbook/values.go`, command
      `go test ./internal/playbook -count=1 -run TestDeclaredValues`: `the pattern is matched
      unanchored`, `the length is counted in bytes`, `an object is taken as a single value`, `an absent
      value is taken as empty`; and `a leading dash is let into a gather step`, command
      `go test ./internal/playbook -count=1 -run TestALeadingDash`. In `internal/ingress/handoff.go`,
      command `go test ./internal/ingress -count=1 -run TestAHandOff`: `one refused value stops every
      playbook bound to the source`, and `the bound set is every loaded webhook playbook, whatever its
      source`
- [ ] T034 [US3] SC-318, SC-321 and FR-305: in `internal/ingress/handoff.go`, `the hand-off passes the
      body's top-level fields as values`, and in `internal/run/execute.go`, `the data file is not recorded
      as a gathered input`, both with command
      `go test ./internal/ingress -count=1 -run TestUndeclaredContentReachesNoRun`; in
      `cmd/gronin/run_cmd.go`, `a manual webhook run skips its declarations`, command
      `go test ./cmd/gronin -count=1 -run TestAManualWebhookRunIsHeldToItsDeclarations`; in
      `cmd/gronin/serve_cmd.go`, `serve arms a webhook playbook with no ingress`, command
      `go test ./cmd/gronin -count=1 -run TestServeRefusesAWebhookPlaybookWithNoIngress` — T054
      rewrites the line it targets and re-declares it

**Checkpoint**: webhook playbooks load, are refused wherever the payload could steer them, and run by
hand with their values held to their declarations. `serve` refuses them: there is no ingress yet.

---

## Phase 4: User Story 1 — A signed delivery becomes one run (P1)

**Goal**: a correctly signed delivery is recorded durably before it is answered, handed to each bound
playbook exactly once by the process that recorded it, and recognised as a repeat inside its window
— across a restart, across processes, and whatever timestamp it carries.

**Independent Test**: the built executable with one source and one playbook bound to it, driving the
stub agent. A signed body sent to the ingress on loopback is recorded as webhook-triggered and linked
to one run; the same body again is answered as accepted and no second run exists.

Needs Phase 3, and the guard landed through its last story: G002 (`bintest.Start`), G012 and G013
(the trigger reference and the clock), G045, G047, G048 and G049 (the decision, `Execute` behind it,
the coordinator and `serve`), G050 (`gronin refusals`), G074, G075, G077 and G079 (the waiting slot,
the instance lock, the wait and its reconciliation), and G088, G093 and G094 (the rate window).

### Tests for User Story 1

- [ ] T035 [P] [US1] SC-301 and SC-307 in `runtime/cmd/gronin/webhook_binary_test.go`. `TestASignedDeliveryRunsOnce`:
      the secret set on standard input, `sources.json`, one playbook bound to the source whose gather
      step appends a line to a file under the test's directory, and `serve` started with
      `bintest.Start` on `--api-address 127.0.0.1:0 --ingress-address 127.0.0.1:0`, the ingress address
      read from its startup line. A body signed with `ingresstest.Sign` is answered `202`; the file
      gains exactly one line; `gronin runs` shows one `webhook` run and `gronin show` names the
      delivery. The same body again is `202`, the file still holds one line, and `gronin deliveries`
      shows one repeat. An unsigned body beside them is `403` and adds nothing.
      `TestADeliveryIsARepeatAcrossARestart`: `serve` stopped after the first delivery and started
      again on the same state directory; its startup line says repeat detection reaches this host only,
      and the same body is `202` with no second line
- [ ] T036 [P] [US1] SC-302 and SC-303, in `runtime/internal/ingress/accept_test.go`, against the handler
      with no socket, a durable-step bound of 200 ms and the write lock held by `ingresstest.HoldWrites`.
      `TestTheAnswerWaitsForTheRecord`: while the lock is held no answer is written, and at the bound the
      answer is `503` — checked from the test's own watchdog against the bound plus slack.
      `TestALateWriteStillRuns`: the lock is released 500 ms after the `503`; the delivery lands, its
      one hand-off is dispatched once, and the same body sent again is `202` and dispatches nothing. A
      store that failed outright would prove nothing about the ordering, which is why the write lands
      late rather than not at all
- [ ] T037 [P] [US1] SC-304, `TestAKilledHandOffIsDropped` in `runtime/cmd/gronin/webhook_drop_test.go`:
      a re-execution of the test binary holds its instance lock (G075), accepts a signed delivery
      through the ingress handler with a dispatcher that never returns, says so, and is killed with
      SIGKILL — killed, not stopped, because a record written on the way out passes a graceful stop.
      A second re-execution accepts another delivery the same way and stays alive. The built
      `gronin deliveries` then shows the first `dropped` and the second still `accepted`. `serve` is
      started on the state directory, after the live child is killed in turn: no run comes from the
      first delivery, and resending its body produces exactly one run. The positive assertions are what
      give this test a mutant; the absence alone is satisfied by a runtime that never started
- [ ] T038 [P] [US1] SC-305, `TestTheReplayWindow…` in `runtime/internal/ingress/window_test.go`: the
      guard's fake clock (G013) injected, a source with a 10-minute window. The same body twice inside it
      is one hand-off and a repeat count of one; the clock moved to one second past the window, the same
      body is a second hand-off; and the clock moved to exactly one window after an acceptance, new
      (research.md §5). Every body carries a `timestamp` field and every request a `Date` header set an
      hour past the window, and a second set an hour before the first acceptance: none of them changes
      an outcome, because the window is judged on the injected clock's wall reading alone
- [ ] T039 [P] [US1] SC-306, `TestOneIdentityIsNewOnce` in `runtime/internal/record/accept_test.go`: a
      re-execution of the test binary opens the store on the test's state directory and stops inside
      its acceptance of identity X at the seam T049 adds, after its decision and before its commit, and
      says so. The test then issues its own acceptance of X with a bound longer than the hold, and tells
      the child to commit. Exactly one of the two is new, and the delivery has exactly one set of
      hand-offs. Forced, not raced: an implementation that reads the identity before its write
      transaction passes every race it happens to win
- [ ] T040 [P] [US1] SC-308, `TestADeclaredIdentity…` in `runtime/internal/ingress/identity_test.go`: a
      source declaring `/id`. Two bodies differing only outside `/id` are one hand-off; a body with no
      `id` is `400` and a delivery refusal with its body; `/id` holding an object is refused likewise;
      and two bodies whose `id` are the integers `9007199254740993` and `9007199254740992` are two
      hand-offs. With no identity declared, reordering a body's keys makes a new delivery
- [ ] T041 [P] [US1] SC-322, `TestADeliveryWaitsUnderTheGuard` in `runtime/cmd/gronin/webhook_guard_test.go`:
      through the built executable on a single-host deployment, a playbook whose gather step sleeps and
      then appends a line. The first delivery runs; a second, sent while it runs, is `202`, waits, and
      runs once the first ends — two lines, the second run's `waiting_trigger_id` set; a third, sent
      while the second waits, leaves a `waiting_slot_full` line in `gronin refusals` naming its
      delivery, and no third line
- [ ] T042 [P] [US1] SC-329, `TestAWebhookRunReplays` in `runtime/internal/run/webhook_replay_test.go`: a
      webhook run executed with the stub agent, then `Replay`. The replay succeeds, its gathered inputs
      include `trigger.json` with the original values, no delivery or hand-off is written, and no
      dispatcher is called
- [ ] T043 [P] [US1] FR-313, `TestTheDurableStepFitsInsideTheWriteLimit` in
      `runtime/internal/ingress/limits_test.go`: the default bounds are the plan's, and the time to read a
      whole request plus the durable step is less than the answer-write limit — the relation research.md
      §3 found an answer vanishes without
- [ ] T044 [P] [US1] `TestFileLockRateCountsWebhookRuns` in `runtime/internal/run/filelock_rate_test.go`:
      the single-host window (G088) counts webhook runs beside scheduled and manual ones, on the injected
      clock
- [ ] T045 [P] [US1] SC-304's waiting half, in `runtime/cmd/gronin/webhook_wait_drop_test.go`, on a
      single-host deployment with one source, one playbook bound to it whose gather step appends its
      declared value to a file the test owns and then sleeps, and `serve` started with `bintest.Start`.
      `TestADroppedWaitRunsOnRetry`: delivery `one` runs; delivery `two`, sent while it runs, is `202`
      and waits — polled until `gronin deliveries` shows it `waiting`, never slept for; `serve` is
      SIGKILLed while it waits. After a restart on the same state directory, `gronin deliveries` shows
      `two` `dropped` and `gronin refusals` a `dropped` naming it, and resending `two` inside the window
      produces exactly one line `two`. Resending `one`, whose run was recorded, is a repeat and adds no
      line — the pair is what separates "a wait is not a decision" from "nothing is a decision".
      `TestARetryAtASurvivingServeRuns`: two `serve` processes on one state directory, each with its
      own API and ingress addresses. Delivery `one` to the first runs and holds the playbook; delivery
      `two` to the second waits behind it; the second process is SIGKILLed while it waits, and `two` is
      then sent to the first — nothing restarted, `gronin deliveries` not read in between, so the
      acceptance's own probe of the dead instance is the only thing that can find the drop. It produces
      exactly one line `two`, and sending it once more is a repeat that adds none. `gronin deliveries`
      afterwards shows the dead process's delivery `dropped` and the retry superseding it

### Implementation for User Story 1

- [ ] T046 [US1] `runtime/internal/ingress/limits.go`: the bounds as options with the plan's defaults,
      and the `http.Server` built from them — header read timeout, read timeout, write timeout, header
      size. US4 adds the body and in-progress bounds to the same options
- [ ] T047 [US1] `runtime/internal/ingress/signature.go`: one MAC and one `hmac.Equal` per request, the
      header read only when it appears once and starts with the declared prefix, the remainder decoded
      from hexadecimal only when it is 32 bytes, and a buffer no MAC equals compared otherwise
      ([contracts/ingress.md](./contracts/ingress.md), *Step 5*)
- [ ] T048 [US1] `runtime/internal/ingress/identity.go`: the body parsed as JSON with number literals
      kept, the identity read at the source's pointer as a single value in `values.go`'s sense, or the
      SHA-256 of the exact bytes when the source declares none (FR-316)
- [ ] T049 [US1] `runtime/internal/record/accept.go`: `Accept`, one write transaction holding the write
      lock from its first statement, deciding newness and writing the delivery, the identity and the
      hand-offs or the repeat together ([data-model.md](./data-model.md), *Delivery identity*); a dropped
      delivery's identity is new, and its retry's hand-offs are the bound playbooks no delivery in the
      chain it supersedes decided; the accepting instance is recorded. An identity pointing at an
      `accepted` or `waiting` delivery whose instance lock can be taken is reconciled first, inside the
      same transaction and through the same record functions T051 uses — its waiting rows dropped with
      their drop records (G074, G075), the delivery marked `dropped` or `handed_off` — and only then
      judged, so a retry reaching a process that outlived the accepting one runs (FR-315). The liveness
      probe is passed in, since `record` does not import `guard`. A seam between the decision and the
      commit, nil outside tests, for T039
- [ ] T050 [US1] `runtime/internal/ingress/ingress.go`: the handler, in contracts/ingress.md's order — the
      route matched on the path and the method checked in the handler, the signature, the identity; the
      acceptance on a context detached from the request, bounded by the durable step, the answer `503`
      when the bound passes first; and the hand-off started by the acceptance completing, whichever of
      the two came first (FR-314). A delivery for a source no playbook is bound to is `unbound`. Every
      failure before the signature passes gets one answer, which T076 makes one amount of work. A path
      or method the ingress does not serve is counted `not_found` through T077, under the
      `(unconfigured)` bucket: it has no source to attribute
- [ ] T051 [US1] `runtime/internal/ingress/reconcile.go`: the guard's own waiting reconciliation (G075)
      runs first, so a dead process's waiting rows are `dropped` and their drop records name their
      deliveries; then every `accepted` or `waiting` delivery whose instance lock (G075) can be taken has
      each hand-off no decision names — no run, no guard refusal other than `dropped`, no delivery
      refusal — marked `dropped`, a hand-off whose waiting row was dropped included, and is itself
      marked `dropped` or `handed_off` (FR-315). A waiting row consulted, never counted as a decision:
      that is the owner's decision of 2026-09-11, and T064's mutant is what holds it
- [ ] T052 [US1] The guard's side of a webhook trigger: `runtime/internal/guard/coordinator.go` —
      `TriggerRef.Kind` may be `webhook` (G012); `runtime/internal/guard/guard.go` — a refusal of a
      webhook trigger records its delivery (G045, FR-328), a refusal that drops a waiting webhook
      trigger included, so the drop names what was lost; `runtime/internal/guard/wait.go` — a webhook
      trigger waits like a manual one, the waiting row records its delivery, and when it comes to run
      its values are extracted again from the body it holds in memory and checked against the playbook
      as re-read (G077); `runtime/internal/run/filelock.go` — the single-host rate window counts webhook
      runs (G093)
- [ ] T053 [US1] `runtime/internal/run/execute.go`: `Execute` is handed the delivery identifier with the
      trigger kind `webhook`, records it on the run, and passes it to the guard with the trigger
- [ ] T054 [US1] `runtime/cmd/gronin/serve_cmd.go` and `runtime/cmd/gronin/webhook.go`:
      `--ingress-address`, with no default; T030's refusal narrowed to a webhook playbook with no ingress
      address, and T034's mutant `serve arms a webhook playbook with no ingress` re-declared against the
      new line; the delivery reconciliation before either listener opens; the ingress listener bound
      after the API's; the startup line of contracts/cli.md naming the address, the sources and the
      single-host reach of repeat detection (FR-319); and the dispatcher, which runs each hand-off in a
      goroutine of its own through the guard and the executor, so that a waiting trigger never holds up
      another playbook's hand-off, and which reports back what the guard did with each — ran, refused,
      or accepted into its waiting slot, and then what ended that wait — so T028 can carry the hand-off
      and the delivery through the states of [data-model.md](./data-model.md), *Hand-off*
- [ ] T055 [US1] `runtime/cmd/gronin/deliveries_cmd.go` and `runtime/cmd/gronin/root.go`: `gronin
      deliveries` and `gronin deliveries show`, listing the delivery's state and each hand-off's, and
      running T051's reconciliation first, as the guard's `gronin refusals` does — which, with the
      acceptance's own probe (T049), is the whole of when a dead process's deliveries are found;
      `runtime/cmd/gronin/records_cmd.go` — `gronin show` names a webhook run's delivery; `runtime/cmd/gronin/refusals_cmd.go` — a refusal of a webhook trigger names its delivery
      (G050)

### Mutants for User Story 1

- [ ] T056 [US1] SC-301 and SC-307: in `internal/record/accept.go`, `a repeat is handed off again`, and
      in `internal/run/execute.go`, `a webhook run records no delivery`, both with command
      `go test ./cmd/gronin -count=1 -run TestASignedDeliveryRunsOnce`; in `internal/record/accept.go`,
      `the identity lookup sees only this process's acceptances`, and in `cmd/gronin/serve_cmd.go`,
      `serve does not state the reach of repeat detection`, both with command
      `go test ./cmd/gronin -count=1 -run TestADeliveryIsARepeatAcrossARestart`
- [ ] T057 [US1] SC-302 and SC-303, in `internal/ingress/ingress.go`: `the acceptance is answered before
      the write` and `a write past its bound is answered as accepted`, command
      `go test ./internal/ingress -count=1 -run TestTheAnswerWaitsForTheRecord`; `the hand-off follows
      the answer rather than the write` and `the write runs on the request's context`, command
      `go test ./internal/ingress -count=1 -run TestALateWriteStillRuns`
- [ ] T058 [US1] SC-304, all with command `go test ./cmd/gronin -count=1 -run TestAKilledHandOffIsDropped`:
      in `internal/ingress/reconcile.go`, `an undecided hand-off is dispatched on restart`, `an undecided
      hand-off is left accepted` and `a live process's delivery is dropped`; in
      `internal/record/accept.go`, `a dropped delivery's retry is a repeat`
- [ ] T059 [US1] SC-305, all with command `go test ./internal/ingress -count=1 -run TestTheReplayWindow`:
      in `internal/record/accept.go`, `an acceptance exactly one window old is a repeat` and `the window
      is never applied`; in `internal/ingress/ingress.go`, `the acceptance is dated by the request's Date
      header`
- [ ] T060 [US1] SC-306, in `internal/record/accept.go`: `the identity is read before the write
      transaction`, command `go test ./internal/record -count=1 -run TestOneIdentityIsNewOnce`
- [ ] T061 [US1] SC-308, in `internal/ingress/identity.go`, command
      `go test ./internal/ingress -count=1 -run TestADeclaredIdentity`: `the declared identity location
      is ignored`, `a missing identity falls back to the digest`, `a number identity is decoded as a
      float`
- [ ] T062 [US1] SC-322 and the rate window: in `internal/guard/wait.go`, `a webhook trigger is refused
      rather than made to wait`, and in `internal/guard/guard.go`, `a refusal of a webhook trigger names
      no delivery`, both with command `go test ./cmd/gronin -count=1 -run TestADeliveryWaitsUnderTheGuard`;
      in `internal/run/filelock.go`, `the single-host window ignores webhook runs`, command
      `go test ./internal/run -count=1 -run TestFileLockRateCountsWebhookRuns`
- [ ] T063 [US1] SC-329 and FR-313: T034's `the data file is not recorded as a gathered input` declared a
      second time with command `go test ./internal/run -count=1 -run TestAWebhookRunReplays`, so the
      replay test is shown to fail on its own; in `internal/ingress/limits.go`, `the answer's write limit
      is shorter than the durable step`, command
      `go test ./internal/ingress -count=1 -run TestTheDurableStepFitsInsideTheWriteLimit`
- [ ] T064 [US1] SC-304's waiting half, all with command
      `go test ./cmd/gronin -count=1 -run TestADroppedWaitRunsOnRetry` unless named: in
      `internal/ingress/reconcile.go`, `a hand-off the guard accepted into its waiting slot is decided`,
      which makes the retry a repeat and runs nothing; in `internal/guard/wait.go`, `dropping a waiting
      webhook trigger records no delivery`; in `internal/record/accept.go`, `the acceptance does not
      reconcile a delivery whose process is gone`, command
      `go test ./cmd/gronin -count=1 -run TestARetryAtASurvivingServeRuns`

**Checkpoint**: deliveries run. Not yet to be released on its own: US2 is what proves the ingress's
refusals, makes an unknown source cost what a wrong signature costs, refuses two listeners on one
port, and records the requests that did not become deliveries.

---

## Phase 5: User Story 2 — The ingress cannot read the record, and the record cannot be reached from the ingress (P1)

**Goal**: the ingress serves its one route and nothing else, the operator API does not serve it, a
request that is not a correctly signed delivery from a configured source achieves nothing but a
count, and every refusal is readable through the operator's surface.

**Independent Test**: both listeners on loopback ports. A run's record requested from the ingress is
not found; a delivery sent to the API is not found; unsigned, mis-signed and unknown-source deliveries
produce no run and no stored body, and the last two identical answers.

Needs Phase 4.

### Tests for User Story 2

- [ ] T065 [P] [US2] SC-309 and SC-328's ingress half, `TestTheIngressServesOneRoute` in
      `runtime/internal/ingress/routes_test.go`, against the handler with a delivery accepted in the same
      test: `GET /runs`, `GET /runs/<the run's id>`, `GET /deliveries`, `GET /deliveries/refused`,
      `GET /hooks/alerts`, `PUT /hooks/alerts` and `POST /hooks` are each `404` with the fixed body, and
      no answer holds the run's identifier or anything from the record. Each of those also leaves a
      count row for the minute under `not_found` and the `(unconfigured)` bucket, read back through
      `record` and summing to the number of requests made — the reason is in the enum and the contract,
      so a `404` that counts nothing is a row an operator would look for and not find
- [ ] T066 [P] [US2] SC-309, SC-310 and SC-311 through the built executable, in
      `runtime/cmd/gronin/listeners_binary_test.go`. `TestTheListenersAreSeparate`: a signed delivery to
      the ingress is `202`; `GET /runs/<its run>` on the ingress is `404`; the same delivery `POST`ed to
      the API address is `404`, and `gronin deliveries` still shows one. `TestAWebhookSettingDoesNotWidenTheAPI`:
      with a source and an ingress address configured, `--api-address 0.0.0.0:0` and no API token is
      refused as the runtime core refuses it; `--api-address 127.0.0.1:18080 --ingress-address
      [::1]:18080` is refused naming both. `TestTheIngressListensOnlyWhenConfigured`: with no ingress
      address and no webhook playbook, the `serve` process owns exactly one listening socket — its
      socket inodes read from `/proc/<pid>/fd` and matched against the `LISTEN` rows of
      `/proc/net/tcp` and `/proc/net/tcp6`, never counted from those tables alone, which the whole
      suite shares; with the address set, two, and a signed delivery is accepted in the same test; with
      a webhook playbook and no address, startup is refused
- [ ] T067 [P] [US2] SC-310, `TestCheckAddresses` in `runtime/internal/ingress/address_test.go`: one port
      under two hosts refused, naming both addresses; the same host on two ports accepted; port 0 on both
      accepted, because each binds a port of its own
- [ ] T068 [P] [US2] SC-312, `TestTheSignatureCorpus` in `runtime/internal/ingress/signature_test.go`,
      against the handler: absent, empty, not hexadecimal, one byte short, the correct value carried
      twice, without the declared prefix, computed over a different body, and computed under another
      configured source's secret are each `403` with no hand-off and no stored body; the correct
      signature, in upper- and lower-case hexadecimal, is `202`
- [ ] T069 [P] [US2] SC-313, `TestTheComparisonIsConstantTime` in
      `runtime/internal/ingress/constanttime_test.go`: parses `signature.go` with `go/parser` and asserts
      that the function comparing the MAC calls `hmac.Equal`, and that no `bytes.Equal`, `==` or
      `subtle` call on byte slices appears anywhere else in the file. It asserts no time
- [ ] T070 [P] [US2] SC-314, `TestAnUnknownSourceIsAnsweredLikeAWrongSignature` in
      `runtime/internal/ingress/unknown_test.go`: with a verifier that records its calls injected, a
      delivery to an unconfigured name and one to a configured source with a wrong signature get
      answers identical in status, headers and body, and the verifier is called exactly once for each
- [ ] T071 [P] [US2] SC-326, the count half, `TestUnauthenticatedRefusalsAreCounted` in
      `runtime/internal/ingress/refusals_test.go`, the clock injected: ten thousand requests, each with a
      distinct unconfigured source name and a body holding the sentinel, leave one count row for that
      minute holding ten thousand, and the sentinel is found nowhere under the state directory. One
      authenticated refusal sent in the same test is a row of its own, with its body — which is what
      shows the store was being written at all
- [ ] T072 [P] [US2] SC-327, `TestThePeerIsTheConnections` in `runtime/internal/ingress/peer_test.go`:
      requests from `127.0.0.1` carrying `X-Forwarded-For: 192.0.2.7` and `Forwarded: for=192.0.2.7` —
      an accepted delivery, an authenticated refusal and an unauthenticated count — each record
      `127.0.0.1`
- [ ] T073 [P] [US2] SC-326's readable half and SC-328, in `runtime/cmd/gronin/deliveries_cmd_test.go`,
      records seeded through `record`: `TestRefusedDeliveriesAreReadable` — `gronin deliveries refused`
      prints each authenticated refusal with its source, reason, value, receipt time in UTC and peer, and
      each count with its interval, reason, bucket and last peer; `TestDeliveriesAreListed` — `gronin
      deliveries` lists each delivery with its state and repeats, and `gronin deliveries show` its body
      and hand-offs
- [ ] T074 [P] [US2] SC-328, `TestTheAPIListsDeliveries` in `runtime/internal/api/api_test.go`:
      `GET /deliveries`, `/deliveries/{id}` and `/deliveries/refused` read the seeded records back, behind
      the token when one is configured

### Implementation for User Story 2

- [ ] T075 [US2] `runtime/internal/ingress/address.go`: `CheckAddresses(api, ingress)` refusing one port,
      whatever the hosts, port 0 excepted (FR-304); `runtime/cmd/gronin/serve_cmd.go` calls it before
      binding either, and leaves the API's own address check exactly as it is (FR-303)
- [ ] T076 [US2] `runtime/internal/ingress/unknown.go` and `runtime/internal/ingress/ingress.go`: the key
      generated at startup for unconfigured names, never stored; a delivery to one goes through the same
      verification as a configured source with no usable signature (FR-309)
- [ ] T077 [US2] `runtime/internal/ingress/refusals.go`: every refusal before the signature passes is
      counted through T008 under its reason and bucket, every unconfigured name sharing
      `(unconfigured)`, with the interval from the clock's wall reading; every authenticated refusal is a
      row with its body; the peer is the connection's `RemoteAddr` and nothing else (FR-332 to FR-334)
- [ ] T078 [US2] `runtime/internal/api/api.go`: the three read-only delivery routes (FR-335)
- [ ] T079 [US2] `runtime/cmd/gronin/deliveries_cmd.go`: `gronin deliveries refused`

### Mutants for User Story 2

- [ ] T080 [US2] SC-309 and SC-328: in `internal/ingress/ingress.go`, `the delivery route answers
      another method`, command `go test ./internal/ingress -count=1 -run TestTheIngressServesOneRoute`;
      in `cmd/gronin/serve_cmd.go`, `serve mounts the operator API behind the ingress` and `serve mounts
      the ingress on the operator API`, command
      `go test ./cmd/gronin -count=1 -run TestTheListenersAreSeparate`; in `internal/ingress/ingress.go`,
      `a request for another route is answered without being counted`, command
      `go test ./internal/ingress -count=1 -run TestTheIngressServesOneRoute`
- [ ] T081 [US2] SC-310 and SC-311: in `internal/ingress/address.go`, `one port under two hosts is
      allowed`, command `go test ./internal/ingress -count=1 -run TestCheckAddresses`; in
      `cmd/gronin/serve_cmd.go`, `a configured source skips the API's address check`, command
      `go test ./cmd/gronin -count=1 -run TestAWebhookSettingDoesNotWidenTheAPI`, and `serve binds an
      ingress when no address is configured`, command
      `go test ./cmd/gronin -count=1 -run TestTheIngressListensOnlyWhenConfigured`
- [ ] T082 [US2] SC-312, in `internal/ingress/signature.go`, command
      `go test ./internal/ingress -count=1 -run TestTheSignatureCorpus`: `a doubled signature header is
      read as its first value`, `the comparison covers only the offered bytes`, `a signature that does
      not decode is accepted`, `the declared prefix is not required`, `every configured secret is
      tried`, `the MAC is compared with itself`
- [ ] T083 [US2] SC-313, in `internal/ingress/signature.go`: `the MAC is compared with bytes.Equal`,
      command `go test ./internal/ingress -count=1 -run TestTheComparisonIsConstantTime`
- [ ] T084 [US2] SC-314, in `internal/ingress/ingress.go`, command
      `go test ./internal/ingress -count=1 -run TestAnUnknownSourceIsAnsweredLikeAWrongSignature`: `an
      unknown source is answered before any MAC is computed`, `an unknown source is answered 404`
- [ ] T085 [US2] SC-326: in `internal/ingress/refusals.go`, command
      `go test ./internal/ingress -count=1 -run TestUnauthenticatedRefusalsAreCounted`: `an
      unauthenticated refusal stores its body` and `each unconfigured name is a bucket of its own`; in
      `cmd/gronin/deliveries_cmd.go`, `the refusal listing drops the peer`, command
      `go test ./cmd/gronin -count=1 -run TestRefusedDeliveriesAreReadable`
- [ ] T086 [US2] SC-327, in `internal/ingress/refusals.go`: `the peer is read from X-Forwarded-For`,
      command `go test ./internal/ingress -count=1 -run TestThePeerIsTheConnections`
- [ ] T087 [US2] SC-328: in `cmd/gronin/deliveries_cmd.go`, `the delivery listing omits the state`,
      command `go test ./cmd/gronin -count=1 -run TestDeliveriesAreListed`; in `internal/api/api.go`,
      `the API's delivery listing returns nothing`, command
      `go test ./internal/api -count=1 -run TestTheAPIListsDeliveries`

**Checkpoint**: the MVP — the three P1 stories. A stranger reaching the ingress learns nothing and
costs a counter; every refusal is readable, and nothing on the ingress reveals a run.

---

## Phase 6: User Story 4 — A hostile or broken sender cannot exhaust the ingress (P2)

**Goal**: an oversized body, a stalled sender and a crowd of deliveries in progress are each refused
or cut off within a declared bound, while a well-formed delivery sent alongside is still accepted.

**Independent Test**: against the ingress on loopback with the bounds set low, each misbehaving
request is ended within its bound while a correctly signed delivery sent alongside is accepted.

Needs Phase 4. Every test here binds loopback, because the thing under test is the server's handling
of a real connection (research.md §1).

### Tests for User Story 4

- [ ] T088 [P] [US4] SC-323, `TestTheSizeBounds…` in `runtime/internal/ingress/size_test.go`, the body
      bound set to 1 KiB and a `CountingListener`: a declared length of 1 MiB with no body sent is `413`
      before the sender sends a byte of it; a chunked body of 3 KiB is `413` with the server having
      consumed no more than the headers, the bound plus one byte, and the chunk framing; 32 KiB of
      headers is `431`. A signed delivery of 1 KiB exactly, sent in the same test, is `202`. The two
      `413`s each leave a `body_too_large` count row for the minute, read back through `record`; the
      `431` leaves none, since the server ends it before any handler runs (contracts/ingress.md)
- [ ] T089 [P] [US4] SC-324, `TestAStalledSender…` in `runtime/internal/ingress/stall_test.go`, the header
      and request timeouts set to 200 ms and 400 ms: a sender that stops partway through its headers,
      and one that stops partway through its body, each have their connection closed within the bound
      plus slack — read from the test's own deadline on the connection — with no status line and no
      delivery recorded. A signed delivery sent while both stall is `202`. The sender that stalls in its
      body leaves a `body_timeout` count row for the minute, read back through `record`; the one that
      stalls in its headers leaves none, for the reason the `431` above leaves none
- [ ] T090 [P] [US4] SC-325, `TestTheInProgressBound` in `runtime/internal/ingress/inprogress_test.go`,
      the bound set to 2: two deliveries stall mid-body and hold both slots; a third is `503` within
      50 ms of sending; once one stalled sender is closed by the test, a signed delivery is `202`. The
      `503` leaves a `busy` count row for the minute, read back through `record`

### Implementation for User Story 4

- [ ] T091 [US4] `runtime/internal/ingress/limits.go`: the body bound and the in-progress bound join the
      options, with the plan's defaults; T043's test gains them
- [ ] T092 [US4] `runtime/internal/ingress/ingress.go`: a slot taken without waiting before the body is
      read, released when the request ends; a declared length over the bound refused before reading; the
      body read through a byte-capped reader; `busy`, `body_too_large` and `body_timeout` counted through
      T077 (FR-329 to FR-331)

### Mutants for User Story 4

- [ ] T093 [US4] SC-323, command `go test ./internal/ingress -count=1 -run TestTheSizeBounds`: in
      `internal/ingress/ingress.go`, `a declared length over the bound is read anyway` and `the body is
      read without a cap`; in `internal/ingress/limits.go`, `the header bound is left at the library's
      default`; in `internal/ingress/ingress.go`, `a body over the bound is refused without being
      counted`
- [ ] T094 [US4] SC-324, in `internal/ingress/limits.go`, command
      `go test ./internal/ingress -count=1 -run TestAStalledSender`: `the header timeout is not set`,
      `the request timeout is not set`; and in `internal/ingress/ingress.go`, `a body that stalls is cut
      off without being counted`
- [ ] T095 [US4] SC-325, in `internal/ingress/ingress.go`, command
      `go test ./internal/ingress -count=1 -run TestTheInProgressBound`: `a delivery waits for a slot
      rather than being refused`, `a slot is not released when its delivery ends`, `a delivery refused
      for want of a slot is not counted`

**Checkpoint**: what an exposed listener can cost is bounded, and each bound is shown against a
subject that exceeds it.

---

## Phase 7: Polish

- [ ] T096 [P] `specs/001-runtime-core/data-model.md`: the `webhook` trigger, the Run's `delivery_id`,
      and the *deliberately absent* section no longer naming webhook deliveries — after the guard's
      G098 has made its own change there
- [ ] T097 [P] `docs/playbook-format.md` documents the webhook trigger and its declared values, and
      `docs/architecture.md` describes the ingress as built — its own listener, the order of a delivery,
      the hand-off through the guard. Neither calls the trigger a "webhook sink": that name is taken by
      the outbound sink
- [ ] T098 [P] `README.md` and `runtime/README.md`: webhook triggers are no longer refused; a deployment
      that exposes the ingress terminates TLS in front of it; which senders sign in the shape the ingress
      accepts and which need a re-signing proxy, pointing at research.md §7 rather than restating it;
      and that an accepted delivery survives a killed process and, on storage that honours a flush, a
      lost machine — the gap contracts/ingress.md states
- [ ] T099 SC-330: `scripts/check-mutation.py --self-test`, then `scripts/check-mutation.py`, report zero
      survivors with every mutant above declared; each success criterion from SC-301 to SC-329 has at
      least one declared mutant whose command is scoped to that criterion's own test, per the table
      below. Run the suite through `scripts/run-suite.sh` under its declared timeout, and let the repeat
      workflow run it against the unchanged tree: the acceptance, the kill, the stalls and the guard's
      wait are all races or clocks, and a flake is a failure
- [ ] T100 Follow [quickstart.md](./quickstart.md) on a real host with a real sender. It is the one place
      a signature is produced by software this repository did not write; what it finds that the suite
      could not becomes a task here rather than a note

---

## Success criteria coverage

| Criterion | Test tasks | Mutant tasks |
| --------- | ---------- | ------------ |
| SC-301 | T035 | T056 |
| SC-302 | T036 | T057 |
| SC-303 | T036 | T057 |
| SC-304 | T037, T045 | T058, T064 |
| SC-305 | T038 | T059 |
| SC-306 | T039 | T060 |
| SC-307 | T035 | T056 |
| SC-308 | T040 | T061 |
| SC-309 | T065, T066 | T080 |
| SC-310 | T066, T067 | T081 |
| SC-311 | T023, T066 | T034, T081 |
| SC-312 | T068 | T082 |
| SC-313 | T069 | T083 |
| SC-314 | T070 | T084 |
| SC-315 | T017, T018 | T032 |
| SC-316 | T011, T013 | T016 |
| SC-317 | T019, T020 | T033 |
| SC-318 | T021 | T034 |
| SC-319 | T019 | T033 |
| SC-320 | T020 | T033 |
| SC-321 | T022 | T034 |
| SC-322 | T041 | T062 |
| SC-323 | T088 | T093 |
| SC-324 | T089 | T094 |
| SC-325 | T090 | T095 |
| SC-326 | T071, T073, T009 | T085, T016 |
| SC-327 | T072 | T086 |
| SC-328 | T065, T073, T074 | T080, T087 |
| SC-329 | T042 | T063 |
| SC-330 | T099 | every task in the column above, and T004 |

---

## Dependencies & Execution Order

- **Setup (Phase 1)** needs nothing and can land before the guard.
- **Foundational (Phase 2)** needs the guard's G005, G006, G007 and G074 — T005 alters tables they
  create and T006 edits their files — and G010, which T014 lands beside. Within it: T005 → T006 →
  T007, T008 → T009. T010 → T011 → T012 → T013. T014 → T015. T016 last, against code that exists.
- **US3 (Phase 3)** needs Phase 2, and G047, whose shape of `Execute` T027 edits. T024 → T025 → T031;
  T026 → T027; T024 → T028; T024 → T029; T030 is independent. The mutant tasks follow the code they
  mutate.
- **US1 (Phase 4)** needs US3 — its hand-off is T028 — and the guard through its last story, the
  G-tasks listed at the head of the phase. T046 → T047 → T048 → T049 → T050 → T051; T052 → T053 →
  T054 → T055. T054 needs T050 and T051. T045 needs the guard's wait (G077) and its instance lock
  (G075) as well as T054, since it drives the built executable; T064 follows T049, T051 and T052,
  whose lines it mutates.
- **US2 (Phase 5)** needs US1. T075, T076 and T077 edit `ingress.go` or `serve_cmd.go` and run in that
  order; T078 and T079 are independent of them.
- **US4 (Phase 6)** needs US1, and T092 needs T077's counting. It does not need US2 beyond that.
- **Polish (Phase 7)** follows the stories it documents, and T096 follows G098. T099 is last among the
  tasks that change the tree, because it is the audit of every mutant declared before it.

### Parallel opportunities

Within Phase 1, T001 and T002. Within Phase 2, T007 and T008; T010 and T014 against each other and
against the record tasks. Within each story, the tests marked `[P]` are in files no other task of their phase touches. The
implementation tasks are not marked: each builds on the one before, and several share `ingress.go`,
`serve_cmd.go` or `execute.go`. **The mutant tasks are never parallel**: every one of them edits
`runtime/testdata/mutations.json`.

### Parallel example: User Story 3

```text
T017 testdata/playbooks/hostile/webhook-*  T020 internal/ingress/handoff_test.go
T018 internal/sink/sink_test.go            T021 internal/ingress/reach_test.go
T019 internal/playbook/values_test.go      T022 cmd/gronin/run_cmd_test.go
```

### Parallel example: User Story 1

```text
T035 cmd/gronin/webhook_binary_test.go     T040 internal/ingress/identity_test.go
T036 internal/ingress/accept_test.go       T041 cmd/gronin/webhook_guard_test.go
T037 cmd/gronin/webhook_drop_test.go       T042 internal/run/webhook_replay_test.go
T038 internal/ingress/window_test.go       T043 internal/ingress/limits_test.go
T039 internal/record/accept_test.go        T044 internal/run/filelock_rate_test.go
T045 cmd/gronin/webhook_wait_drop_test.go
```

### Parallel example: User Story 2

```text
T065 internal/ingress/routes_test.go       T070 internal/ingress/unknown_test.go
T066 cmd/gronin/listeners_binary_test.go   T071 internal/ingress/refusals_test.go
T067 internal/ingress/address_test.go      T072 internal/ingress/peer_test.go
T068 internal/ingress/signature_test.go    T073 cmd/gronin/deliveries_cmd_test.go
T069 internal/ingress/constanttime_test.go T074 internal/api/api_test.go
```

---

## Implementation Strategy

**The MVP is the three P1 stories**: Phases 1 to 5. The specification made them all P1 on purpose —
a delivery that runs (US1) is not worth accepting before what a stranger can reach (US2) and what a
payload can do (US3) are stated and bounded. They land in the order US3, US1, US2 because each needs
the one before it to be tested at all, and each checkpoint leaves the runtime safe: after US3 a
webhook playbook loads and runs by hand but `serve` refuses it; after US1 deliveries run behind a
signature; after US2 the refusals are proven and recorded. A release is not tagged between US1's and
US2's checkpoints.

Then US4, which bounds what an exposed listener costs. It is P2 because none of what it refuses
reaches a run or the record, and the header and request timeouts it proves are already set by US1,
where FR-313 needs them. A deployment that exposes the ingress beyond loopback before US4 lands does
so without a body bound or an in-progress bound, and the release notes say so.

## Notes

- The effect a binary test counts is a line a gather step appends to a file the test owns. A gather
  step runs only once the guard has admitted the run, so the line is evidence of a run, not of a log
  line claiming one.
- Every test that waits for something to happen polls with a deadline. None sleeps a fixed time and
  then asserts: a sleep shorter than the thing it waits for passes without the thing ever happening.
- Every bound here — the durable step, the body, the stalls, the slots — is tested against a subject
  that exceeds it. A bound is invisible against a subject that is prompt.
- Nothing in a fixture is real: the sources sign with the suite's own test secrets, send from
  loopback, and name `example.com` and `192.0.2.0/24` where an address is needed.
- Commit per task or per logical group. Stop at any checkpoint.
