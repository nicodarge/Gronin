---

description: "Task list for the retrieve stage"
---

# Tasks: Retrieve

**Input**: Design documents from `/specs/003-retrieve/`

**Prerequisites**: [plan.md](./plan.md), [spec.md](./spec.md), [research.md](./research.md),
[data-model.md](./data-model.md), [contracts/](./contracts/), [quickstart.md](./quickstart.md)

**Tests**: included, and not optional here. SC-220 makes a test that fails when its behaviour is
removed a success criterion in its own right, and much of what this feature promises is that
something does *not* happen — no text leaves the host, no lexical search runs under the semantic
name, no skipped file reaches an index — which a broken test asserts as readily as a working one.
So every success criterion below has a test task and a mutant registered with
`scripts/check-mutation.py`, and a mutant counts only once it is declared in
`runtime/testdata/mutations.json` and killed.

**Organization**: grouped by user story, in the specification's priority order. US1 is the stage in
its default form, and US2 and US3 each add a kind of collection on top of the index, the record and
the refusal path US1 builds. Each phase leaves the runtime shippable: the `retrieve` block stays
refused at load until US1's stage applies it (T008, lifted by T039), and a catalogue entry naming
`reports` or `embeddings` stays refused until its story lands (T010, lifted by T059 and T081),
because a declared collection nothing can search is a bound that reads as applied.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an incomplete task)
- **[Story]**: the user story the task serves

## Path Conventions

Paths follow [plan.md](./plan.md)'s structure, under the runtime core's module at `runtime/`:

- `internal/collections` — the deployment's catalogue, `collections.json`. Beside `mcpcatalog`,
  never inside it.
- `internal/index` — passages, the directory walk, generations, the FTS5 lexical index and the
  vector table. It knows nothing of runs: a reports source is handed to it as documents.
- `internal/embed` — the embeddings client, bounded per request, redacting what it sends.
- `internal/embed/embedtest` — the stub API on loopback. Imported by `_test.go` files only.
- `internal/stage/retrieve` — resolve the query, bound the retrieval, update, search, render the
  results file. It imports `index`, `embed`, `collections` and `record`, never `run`.
- `internal/record`, `internal/playbook`, `internal/run`, `cmd/gronin` — as named in plan.md.

One file the plan does not name: `cmd/gronin/collections_cmd.go`, the three operator commands of
[contracts/cli.md](./contracts/cli.md), beside `mcp_cmd.go`.

## Mutants

Every mutant in this file is declared in `runtime/testdata/mutations.json` with `tree: ".."`, the
file it mutates relative to `runtime/`, and a command. Where another test in the same package would
kill the mutant too, the command is scoped with `-run` to the test the task names — otherwise the
harness shows that the package can fail, not that this test can — and the name a mutant task gives
to `-run` is the name the test task it cites gives its test. The find text is written when the code
exists, and the harness refuses one it cannot find exactly once, so a mutant whose target later
moves fails the mutation job rather than passing silently.

## Rebasing over the guard

The guard ([specs/002-guard](../002-guard/)) is being implemented on other branches while this is,
and touches several files these tasks touch. Nothing here assumes the tree as it stands on
`production` today is the tree these tasks land on. Where the two meet, whichever lands second
rebases onto the other, and T098 is the checklist for doing it:

- **Migration numbering.** T005 writes `0002_retrieve.sql` because `0001_initial.sql` is the only
  migration on `production` today. The guard's tasks add `0002_guard.sql`. If the guard's migration
  merges first, this one is renamed to the next free number during the rebase, before merging.
  `internal/record/schema.go` applies migrations by name and records each name it applied, so a
  migration that has reached `production` is never renamed afterwards — a renamed one would be
  applied twice on every store that already holds it.
- **`internal/playbook/validate.go` and the reserved blocks.** The guard's tasks shrink
  `validateReserved` to `retrieve` alone; T008 shrinks it to `guard` alone. The second to land
  deletes `validateReserved` with nothing left to reserve, `playbook.Unknown` if nothing else uses
  it, and `TestTheGateRefusesAReservedBlockThatReachesIt`, and removes the mutant
  `the gate accepts a reserved block` from `runtime/testdata/mutations.json`, whose target text
  goes with the function. Both add a `validate…` function to `Validate`, a field to `Playbook`,
  cases to the tables in `parse_test.go` and `validate_test.go`, and fixtures to the hostile corpus,
  whose test counts its files against its table.
- **The playbook schema.** Both replace a reserved property in
  `specs/001-runtime-core/contracts/playbook.schema.json` and in
  `runtime/internal/playbook/playbook.schema.json`, which `parse_test.go` holds identical. The
  first is published on GitHub Pages, so a merge that keeps one side's property and drops the
  other's publishes the loss.
- **`internal/run/execute.go` and `internal/run/replay.go`.** The guard decides before `Begin` and
  fences before the agent stage and before each sink. Retrieval sits between gather and the prompt
  (T034), so it runs while the claim is held, and the guard's fence before the agent stage also
  catches a claim lost during a long retrieval. The conflict is textual; neither changes the
  other's order.
- **`cmd/gronin`.** The guard adds `coordination.go` and edits `deployment.go`, `root.go` (a
  `refusals` command), `serve_cmd.go`, `run_cmd.go` and `records_cmd.go` (`show` gains its wait and
  reach lines). T012, T036, T037 and T038 edit the same files for the catalogue, the stage, the
  `collections` command and `show`'s retrieval lines.
- **`internal/bintest`.** The guard's tasks add `Start`, a built `gronin` held open. T004 adds the
  same helper only if it is not on `production` yet; the second branch keeps one.
- **`runtime/testdata/mutations.json`**, **`specs/001-runtime-core/data-model.md`**,
  **`docs/playbook-format.md`**, **`README.md`**. Both append or amend; the conflicts are at the
  tail of a list or in neighbouring paragraphs, and both sides are kept.

---

## Phase 1: Setup

- [ ] T001 [P] `runtime/testdata/fakeclaude/main.go` gains `FAKECLAUDE_QUOTE`: a comma-separated list
      of names the stub reads from its working directory when it starts, placing each file's
      content in its report under `quoted.<name>`, and `null` for a file that is not there. The
      report is `FAKECLAUDE_RESULT`'s object, or `{}`, with `quoted` added.
      `runtime/internal/fakeagent/fakeagent.go` exports `QuoteVar`, and `Wrapped(t, env...)`: an
      executable script under the test's directory that sets those variables and executes the stub,
      for tests driving the built binary — whose agent child inherits a fixed list of variables, so
      a `FAKECLAUDE_…` set by the test never reaches it otherwise. SC-201 needs the agent to show it
      read the passage: a results file that exists after the run passes against a stage that wrote
      it after the agent had gone
- [ ] T002 `TestTheStubQuotesWhatItFound` in `runtime/internal/fakeagent/fakeagent_test.go`: a file
      present in the stub's working directory is quoted verbatim, an absent one is `null`, and a
      `FAKECLAUDE_RESULT` given alongside keeps its own fields
- [ ] T003 Register T002's mutant, `the stub quotes nothing`, in `testdata/fakeclaude/main.go`, command
      `go test ./internal/fakeagent -count=1 -run TestTheStubQuotesWhatItFound`
- [ ] T004 [P] `runtime/internal/bintest/bintest.go`: `Start` — a built `gronin` held open, its standard
      output readable line by line, a `Wait` bounded by a deadline, and a cleanup that kills it.
      Only if the guard's version is not yet on `production`; if it is, use that one and mark this
      task done. SC-202 has to observe `serve` while it starts, which a command that exits cannot show

**Checkpoint**: the stub agent can say what it read, and a test can hold `serve` open.

---

## Phase 2: Foundational (blocks every story)

### The record store

- [ ] T005 `runtime/internal/record/migrations/0002_retrieve.sql`: the `retrievals` and
      `retrieved_items` tables of [data-model.md](./data-model.md). A new file rather than an edit
      of `0001_initial.sql`, which `schema.go` would skip on every store that already applied it.
      The number is today's next free one; see *Rebasing over the guard*
- [ ] T006 `runtime/internal/record/retrievals.go`: `Retrieval` and `RetrievedItem`, the outcomes
      `found`, `empty` and `refused`; `AddRetrieval`, writing a retrieval and its items in one
      transaction, and `Retrievals(ctx, runID)`, items ordered by rank. `query` and `error` pass the
      redactor at the write boundary like every other text field; content travels as blob
      references, which `Blobs.Put` already redacts
- [ ] T007 `TestRetrievals…` in `runtime/internal/record/retrievals_test.go`: a store created under
      `0001_initial.sql` alone migrates and its runs read back with no retrievals; a retrieval of
      each outcome round-trips with its items in rank order; `testsecret.Value`, configured as a
      secret and written into a retrieval's query, its error and an item's content, is in neither
      `record.db` nor any blob — the check `leak_test.go` makes for the runtime core's fields

### The `retrieve` block

- [ ] T008 [P] `runtime/internal/playbook/playbook.go` gains `Retrieval` (`collection`, `query`,
      `query_from`, `as`, `max_results`, `max_bytes`, their defaults 10 and 16,384) and `Retrieve
      []Retrieval`. The reserved `retrieve` property of
      `specs/001-runtime-core/contracts/playbook.schema.json` is replaced by the content of
      [contracts/retrieve.schema.json](./contracts/retrieve.schema.json), and
      `runtime/internal/playbook/playbook.schema.json` with it. `Deployment` gains `Collections`.
      In `validate.go`, `validateReserved` keeps only `guard`, and a new `validateRetrieve` refuses,
      by field: a collection `Deployment.Collections` does not hold; a `query_from` no gather step's
      `as` names; an `as` that a gather step or another retrieval already writes; a count or byte
      bound above its ceiling, where the runtime can say why as well as the schema. And, until T039
      lifts it, every `retrieve` block as declared but not yet applied — the stage does not exist
      yet, and a block the gate accepted would be ignored. `interpolatable` gains
      `retrieve[i].query`, so a bare or unconfigured reference in a query is refused like one
      anywhere else. Fixtures: refused `retrieve-mode.yaml`, `retrieve-endpoint.yaml`,
      `retrieve-model.yaml`, `retrieve-credential.yaml`, `retrieve-unknown-key.yaml`,
      `retrieve-results-above-ceiling.yaml`, `retrieve-bytes-above-ceiling.yaml` and
      `retrieve-query-and-query-from.yaml`, and accepted `retrieve.yaml`, under
      `runtime/internal/playbook/testdata/schema/`. In `runtime/testdata/playbooks/hostile/`,
      `retrieve-block.yaml` becomes `retrieve-undeclared-collection.yaml`, its content reshaped from
      today's mapping (`retrieve: {collection: incidents}`) to a list of one retrieval carrying
      `collection`, `as` and `query` — well-shaped, and refused now only for what it names, which is the change of meaning plan.md warns about — beside
      `retrieve-undeclared-gathered-input.yaml` and `retrieve-colliding-name.yaml`. The tables in
      `parse_test.go` and `validate_test.go` follow, `validate_test.go`'s `deployment()` declares
      one collection, and
      `TestTheGateRefusesAReservedBlockThatReachesIt` loses its `retrieve` case
- [ ] T009 SC-203, the refusal half, `TestRetrieveBlock…` in
      `runtime/internal/playbook/parse_test.go` and `runtime/internal/playbook/validate_test.go`:
      every fixture of T008 refused with the field named — a mode, an endpoint, a model and a
      credential among them, each refused as a key the block does not have — and the valid block's
      only problem the not-yet-applied one, asserted by field name so that T039's lift is a
      one-line change to the table

### The catalogue

- [ ] T010 [P] `runtime/internal/collections/doc.go` and `runtime/internal/collections/collections.go`:
      `Load(stateDir)` reads `collections.json`, absent meaning none, decoding with unknown fields
      disallowed; every refusal of [contracts/cli.md](./contracts/cli.md)'s table, all of them
      reported rather than the first; `Names`, and per entry its source, `Mode`, `RetrievalTimeout`
      (default 2 minutes) and `RequestTimeout` (default 60 seconds). An entry naming `reports` or
      `embeddings` is refused by field as not yet applied, until T059 and T081 lift each
- [ ] T011 `TestCollections…` in `runtime/internal/collections/collections_test.go`: absent is an empty
      catalogue; refused, each by name — an unknown key, both sources, neither, a relative
      directory, a name that is not a slug, a duration that does not parse, a zero one; and
      `reports` and `embeddings` refused as not yet applied, in a table the later lifts edit
- [ ] T012 `runtime/cmd/gronin/deployment.go`: `openCollections`, read once per invocation like
      `openCatalog`, and `capabilities` hands its names to the gate as `Collections`; `loadPlaybooks`
      and its callers in `validate_cmd.go`, `run_cmd.go`, `serve_cmd.go` and `records_cmd.go` pass
      it. A malformed `collections.json` refuses every command that loads playbooks, and none that
      only reads run history — the rule `openResolvableCatalog` states for the MCP catalogue
- [ ] T013 In `runtime/cmd/gronin/collections_cmd_test.go`, through the built binary:
      `TestValidateRefusesAnUndeclaredCollection` — `gronin validate` with `collections.json`
      declaring `runbooks` refuses a playbook retrieving from `incidents`, naming the field; and
      `TestAMalformedCollectionsFileRefusesTheDeployment` — an unknown key refuses `gronin validate`
      and leaves `gronin runs` working

### Mutants for the foundation

- [ ] T014 Register, with command `go test ./internal/playbook -count=1 -run TestRetrieveBlock`:
      `the gate accepts an undeclared collection`, `the gate accepts a query_from no gather step
      writes`, `the gate accepts a results name a gather step writes` and `the gate accepts a
      results name another retrieval writes`, in `internal/playbook/validate.go`; `the retrieve
      block accepts an unknown key` and `the result count has no ceiling`, in
      `internal/playbook/playbook.schema.json` — scoped, because the test holding the two schema
      copies identical would otherwise kill them for the wrong reason. With command
      `go test ./internal/collections -count=1 -run TestCollections`: `the catalogue accepts an
      unknown key` and `a collection with two sources is accepted`, in
      `internal/collections/collections.go`. With command
      `go test ./internal/record -count=1 -run TestRetrievals`: `a retrieval's query is written
      unredacted`, in `internal/record/retrievals.go`

**Checkpoint**: the record store has somewhere to put what a retrieval finds, the gate reads the
`retrieve` block and still refuses it, and the catalogue is read. Nothing searches anything yet.

---

## Phase 3: User Story 1 — A playbook reads what the deployment's documents say (P1) 🎯 MVP

**Goal**: a lexical collection over a directory is kept current with its sources, searched before
the agent runs, and everything the search handed the agent is in the record and replayable.
Nothing leaves the host.

**Independent Test**: a directory of three text files declared as a collection, a playbook whose
query matches one of them, the stub agent quoting the results file. The stub's report holds the
matching passage, and `gronin show` names the collection, `lexical`, the generation and the passage.

### Tests for User Story 1

- [ ] T015 [P] [US1] SC-201, `TestTheAgentReadsWhatWasRetrieved` in
      `runtime/cmd/gronin/retrieve_binary_test.go`: three files, a query matching one, a playbook
      whose agent is the stub with `FAKECLAUDE_QUOTE` naming the results file. The report quotes
      the matching passage; `gronin show` names the collection, `lexical`, a generation and the
      passage's source. A second retrieval with `query_from` a gather step's output finds what that
      output names — the query formed from gathered input that FR-202 orders the stages for
- [ ] T016 [P] [US1] SC-201, `TestTheQuery…` in `runtime/internal/stage/retrieve/query_test.go`: a
      `query` resolves `${trigger.…}` and `${config.…}` and nothing else; a `query_from` reads the
      named file from the working directory whole; with `query_from` set, nothing in the trigger is
      read, so a trigger value shaped like a query never becomes one
- [ ] T017 [P] [US1] SC-203, the acceptance half, in `runtime/internal/playbook/validate_test.go`: the
      valid block of T008 is accepted with no problem at all, and the refusals of T009 still hold.
      The runtime refuses every `retrieve` block before this feature, so the valid case is the one
      that can fail
- [ ] T018 [P] [US1] `TestPassages…` in `runtime/internal/index/passage_test.go`, research.md §8's text
      rules as a table: blank lines separate blocks, an ATX heading opens a passage, a setext
      underline does not; blocks pack while they fit 1,024 bytes; a longer block is cut at its last
      whitespace, and one with none at a character boundary, a multi-byte character straddling the
      cap included; no passage is empty or over the cap; ordinals and offsets are as the document
      holds them
- [ ] T019 [P] [US1] SC-207, the walk, `TestTheWalk…` in `runtime/internal/index/walk_test.go`: a
      directory holding a text file, a file holding a NUL byte, a text file of 1 MiB and one byte, a
      symbolic link to a file outside it whose content is a marker, and a symbolic link to its own
      parent. The documents and the skipped entries are asserted exactly, each skip with its reason;
      the walk returns inside a deadline of the test's own, so a walk that follows the parent link
      fails as an assertion rather than a hang. A directory that does not exist is an error naming
      it, never an empty walk
- [ ] T020 [P] [US1] SC-207, the operator's surface, `TestAListingNamesEverySkippedFile` in
      `runtime/cmd/gronin/collections_cmd_test.go`: T019's directory as a collection, and
      `gronin collections show` printing its lines exactly as [contracts/cli.md](./contracts/cli.md)
      shows them. A run retrieving from it with a query made of the outside file's other words then
      leaves the marker in no result, no row of `record.db`, and no blob — the query itself never
      holds the marker, or the record would hold it for an innocent reason
- [ ] T021 [P] [US1] SC-209, `TestAnUpdateMatchesARebuild` in `runtime/cmd/gronin/retrieve_update_test.go`:
      a retrieval; one document added, one changed, one removed, the removed one carrying a marker;
      `gronin collections list` says the sources changed; the second retrieval reflects all three
      and holds no marker. Its results equal those after `gronin collections rebuild`, and those
      after the state directory's `index/` is deleted and the next retrieval rebuilds it; the
      listing then shows a generation whose build time is after the deletion
- [ ] T022 [P] [US1] SC-211, in `runtime/internal/index/generation_test.go`.
      `TestAKilledRebuildLeavesThePreviousGeneration`: a re-execution of the test binary — the
      pattern of `TestALockHeldByAKilledProcessIsAcquirable` — opens an index holding generation G1,
      starts a rebuild over changed sources, and at the seam inside its write transaction says so
      and blocks; it is killed with SIGKILL. The index then names G1, and a search returns G1's
      results. Killed rather than stopped, because an implementation that cleans up on its way out
      passes a graceful stop. `TestConcurrentUpdatesLeaveOneWholeGeneration`: update A is held at
      the seam between reading the generation and writing, update B commits, A is released; the
      generation left matches one full rebuild of the sources as they stand. The interleaving is
      injected rather than hoped for
- [ ] T023 [P] [US1] SC-214, `TestTheRecordHoldsWhatWasRetrieved` in
      `runtime/cmd/gronin/retrieve_record_test.go`: the stub agent copies the results file it read
      into its report; `gronin show <run> --retrieval <as>` reproduces it byte for byte and `show`
      names the generation. Then the source changes and `gronin collections rebuild` runs: the
      earlier run's `show --retrieval` output and generation are unchanged. A record pointing into
      the index fails the second half
- [ ] T024 [P] [US1] SC-215, the lexical half, `TestAReplayDoesNotSearch` in
      `runtime/internal/run/retrieve_replay_test.go`: a run retrieves; the collection's index file
      is deleted and its directory removed; the run is replayed. The replay succeeds, the stub
      agent's quoted results file is byte-identical to the original's, and the replay's record holds
      the same retrieval rows
- [ ] T025 [P] [US1] SC-216, the lexical half, `TestTiesFollowTheKey` in
      `runtime/internal/index/tiebreak_test.go`: passages that tie exactly — the same words at the
      same length (research.md §3) — written to the index in an order that disagrees with
      `(source, ordinal)`. Ten repeated searches return one order, and it is the key's
- [ ] T026 [P] [US1] SC-217, `TestAQueryIsPlainText` in `runtime/internal/index/lexical_test.go`:
      `disk NOT logs`, `cert*`, `disk" AND (`, and a column filter `text:disk` each return what the
      same words searched as plain terms return, and none errors (research.md §2)
- [ ] T027 [P] [US1] SC-218, `TestBounds…` in `runtime/internal/stage/retrieve/bounds_test.go`, each
      fixture past its bound: a query of 1,500 bytes is cut at its last whitespace before 1,024 and
      recorded `query_truncated`; a search matching 30 passages with `max_results: 10` returns 10
      and is recorded `count_truncated`; results totalling more than `max_bytes` produce a file of
      at most `max_bytes`, ending in the line [contracts/cli.md](./contracts/cli.md) gives, recorded
      `bytes_truncated`. A fixture inside a bound passes whether or not the bound exists
- [ ] T028 [P] [US1] SC-219, in `runtime/internal/run/retrieve_refusal_test.go`.
      `TestARetrievalThatCannotRunRefuses`: a collection whose directory is missing, and a query
      that resolves to whitespace, each refuse the run with the status `refused` — not `failed` —
      and an error naming the directory or the empty query; the stub agent never starts, and the
      retrieval row says `refused` with no items. `TestNothingFoundIsSaid`: a query matching nothing
      lets the run proceed, the agent quotes a results file saying nothing was found, and the row
      says `empty`

### Implementation for User Story 1

- [ ] T029 [US1] `runtime/internal/index/doc.go` and `runtime/internal/index/passage.go`: research.md
      §8's rules for text, and the passage rule's version constant that the configuration identity
      carries
- [ ] T030 [US1] `runtime/internal/index/walk.go`: `fs.WalkDir` over the directory, which does not
      follow links; a symbolic link skipped as such; a regular file's size read from its `lstat`
      and the file skipped past 1 MiB without being read; the rest read, skipped when it holds a NUL
      byte or is not valid UTF-8, and digested with SHA-256. A directory that does not exist, and a
      file that cannot be read, are errors naming the path (FR-228)
- [ ] T031 [US1] `runtime/internal/index/index.go` and `runtime/internal/index/schema.sql`: one
      database per collection at `<state-dir>/index/<collection>.db`; the configuration identity and
      the generation as [data-model.md](./data-model.md) defines them; `Update`, which works the
      difference out from a read of the stored generation, then inside one write transaction checks
      the generation is unchanged — working it out again if another update committed — and applies
      it; `Rebuild`; and `Status`, the generation, its build time, identity and documents, compared
      with a fresh walk for the listing. An index under another identity is emptied first. Two seams,
      nil outside tests: after the read, and inside the write transaction. When T022's kill leaves
      a torn generation, the generation becomes a file built beside the index and renamed into
      place, and research.md records which the kill decided
- [ ] T032 [US1] `runtime/internal/index/lexical.go`: the FTS5 table over passage text with the
      `unicode61` tokenizer (research.md §10); the query split on whitespace, each word quoted with
      embedded quotes doubled, joined with `OR` (§2); `ORDER BY bm25(…), source, ordinal`, the
      search read inside one transaction
- [ ] T033 [US1] `runtime/internal/stage/retrieve/doc.go`, `query.go`, `retrieve.go` and `results.go`:
      a `Stage` holding the catalogue, the index directory, the configuration and the redactor.
      Per retrieval, in declared order: resolve the query, from `query` or `query_from`; cut it to
      1,024 bytes at the last whitespace; refuse it empty; open the collection's index under a
      context bounded by the collection's `retrieval_timeout`, which covers the walk, the update and
      the search (FR-209); search; take `max_results`; render the results file of
      [contracts/cli.md](./contracts/cli.md) within `max_bytes`; pass it through the redactor; write
      it to the working directory under `as`. It returns, per retrieval, what the record needs, and
      stops at the first refusal
- [ ] T034 [US1] `runtime/internal/run/execute.go`: `Executor.Retrieve`, the stage, run after gather and
      before the prompt is read (FR-202); each outcome recorded — `AddRetrieval`, the results file
      and each item's content put in the blob store — while the working directory exists, as
      gathered inputs are; a refusal ends the run `refused`, naming the retrieval by its position and
      its collection, before any agent is started
- [ ] T035 [US1] `runtime/internal/run/replay.go`: `Replay` reads the parent's retrievals, writes each
      recorded results file back to the working directory under its `as_name`, and records the same
      rows for the replay. It opens no index (FR-226)
- [ ] T036 [US1] `runtime/cmd/gronin/deployment.go`: the stage built with `<state-dir>/index`, the
      catalogue, the configuration and the deployment's redactor, and handed to the executor
- [ ] T037 [US1] `runtime/cmd/gronin/collections_cmd.go` and `runtime/cmd/gronin/root.go`:
      `gronin collections list`, `show` and `rebuild`, with the output of
      [contracts/cli.md](./contracts/cli.md). `list` and `show` walk and compare, and write nothing;
      `rebuild` exits non-zero naming the cause
- [ ] T038 [US1] `runtime/cmd/gronin/records_cmd.go`: `gronin show` prints each retrieval and its
      results as [contracts/cli.md](./contracts/cli.md) shows; `--retrieval <as>` writes the recorded
      results file to standard output unchanged, and exits non-zero for a name the run did not
      retrieve
- [ ] T039 [US1] `runtime/internal/playbook/validate.go`: `validateRetrieve` stops refusing a well-formed
      block as not yet applied; T009's table loses that row

### Mutants for User Story 1

- [ ] T040 [US1] SC-201: `the results file is written after the agent stage` — the stage's call
      deferred until `Execute` returns, which is the implementation SC-201 names — in
      `internal/run/execute.go`, and `the results file is never written`, in
      `internal/stage/retrieve/retrieve.go`, both with command
      `go test ./cmd/gronin -count=1 -run TestTheAgentReadsWhatWasRetrieved`; `query_from is
      ignored for the trigger`, in `internal/stage/retrieve/query.go`, command
      `go test ./internal/stage/retrieve -count=1 -run TestTheQuery`
- [ ] T041 [US1] SC-203: `the gate refuses every retrieve block` — T039's lift put back, a problem
      appended for any declared block — in `internal/playbook/validate.go`, command
      `go test ./internal/playbook -count=1 -run TestRetrieveBlock`. Only the valid case kills it;
      confirm T014's mutants are still killed now that the block is accepted
- [ ] T042 [US1] SC-207: `the walk follows symbolic links`, `a file past the document bound is read`
      and `a file that is not text is indexed`, in `internal/index/walk.go`, each with command
      `go test ./internal/index -count=1 -run TestTheWalk`; the first declared a second time with
      command `go test ./cmd/gronin -count=1 -run TestAListingNamesEverySkippedFile`, so that the
      test driving the built binary is shown to fail on its own
- [ ] T043 [US1] SC-209, all with command `go test ./cmd/gronin -count=1 -run
      TestAnUpdateMatchesARebuild`: `an update ignores removed documents` and `an update ignores
      changed digests`, in `internal/index/index.go`; `the listing never reports a change`, in
      `internal/index/index.go`; `the index is kept outside the state directory`, in
      `cmd/gronin/deployment.go`
- [ ] T044 [US1] SC-211: `an update commits in two transactions`, in `internal/index/index.go`, command
      `go test ./internal/index -count=1 -run TestAKilledRebuildLeavesThePreviousGeneration`; `an
      update writes without checking the generation it read`, same file, command
      `go test ./internal/index -count=1 -run TestConcurrentUpdatesLeaveOneWholeGeneration`
- [ ] T045 [US1] SC-214, command `go test ./cmd/gronin -count=1 -run
      TestTheRecordHoldsWhatWasRetrieved`: `the results file is not recorded`, in
      `internal/run/execute.go`; `the retrieval row omits its generation`, in
      `internal/record/retrievals.go`
- [ ] T046 [US1] SC-215: `a replay does not restore the retrieved files`, in `internal/run/replay.go`,
      command `go test ./internal/run -count=1 -run TestAReplayDoesNotSearch`
- [ ] T047 [US1] SC-216: `lexical ties are left in index order`, in `internal/index/lexical.go`, command
      `go test ./internal/index -count=1 -run TestTiesFollowTheKey`
- [ ] T048 [US1] SC-217: `the query reaches MATCH unquoted`, in `internal/index/lexical.go`, command
      `go test ./internal/index -count=1 -run TestAQueryIsPlainText`
- [ ] T049 [US1] SC-218, command `go test ./internal/stage/retrieve -count=1 -run TestBounds`: `the
      query is not cut`, in `internal/stage/retrieve/query.go`; `the result count is not capped`,
      `the byte bound is not applied` and `a cut is not recorded as truncated`, in
      `internal/stage/retrieve/results.go`. And `a passage exceeds the cap`, in
      `internal/index/passage.go`, command `go test ./internal/index -count=1 -run TestPassages`
- [ ] T050 [US1] SC-219, command `go test ./internal/run -count=1 -run
      TestARetrievalThatCannotRunRefuses`: `a missing directory walks as empty`, in
      `internal/index/walk.go`; `an empty query is searched`, in `internal/stage/retrieve/query.go`;
      `a refused retrieval lets the run continue` and `a refused retrieval is recorded failed`, in
      `internal/run/execute.go`. And `nothing found is not said`, in
      `internal/stage/retrieve/results.go`, command
      `go test ./internal/run -count=1 -run TestNothingFoundIsSaid`

**Checkpoint**: the MVP. A playbook retrieves from a directory of runbooks, lexically, with nothing
leaving the host; the record holds what it saw; a replay needs no index. `reports` and `embeddings`
are still refused in the catalogue.

---

## Phase 4: User Story 2 — A playbook reads what earlier runs concluded (P2)

**Goal**: a collection over the reports this runtime recorded for named playbooks, holding only
reports that validated and that no replay or resume copied, each result naming its run.

**Independent Test**: a record store holding a run whose report validated, a run whose report did
not, a replay of the first and a resume of it. A retrieval from a collection over that playbook's
reports returns exactly one result, naming the first run.

### Tests for User Story 2

- [ ] T051 [P] [US2] `TestReportPassages…` in `runtime/internal/index/passage_report_test.go`, research.md
      §8's report rules: key names, booleans and nulls are not indexed; values keep document order;
      each object in an array opens a passage; a report holding no values is no document; and the
      query `nthe` matches nothing, where indexing the JSON text would have matched the escaped
      newline
- [ ] T052 [P] [US2] SC-208, `TestOnlyOriginalValidatedReportsAreRetrieved` in
      `runtime/internal/run/retrieve_reports_test.go`, seeded through the executor rather than by
      writing rows: a run whose stub report validates, one whose report fails its schema, a replay of
      the first answering the same report, and a resume of the first. A playbook retrieving from a
      collection over those reports gets exactly one result, and both the record and the results
      file name the first run
- [ ] T053 [P] [US2] `TestIndexableReports` in `runtime/internal/record/reports_test.go`: the store's
      answer for a list of playbooks, as a table — a run with a report and no parent is listed; one
      with no report, a replay and a resume are not; a playbook not named is not
- [ ] T054 [P] [US2] In `runtime/internal/collections/collections_test.go`, the table of T011 edited:
      `reports` is accepted, an empty `reports` list refused, and `embeddings` still refused as not
      yet applied
- [ ] T055 [P] [US2] `TestAReportsCollectionListsItsRuns` in `runtime/cmd/gronin/collections_cmd_test.go`:
      `gronin collections show` over a reports collection names each indexed run by its identifier,
      and says the sources changed once a further run has recorded a report

### Implementation for User Story 2

- [ ] T056 [US2] `runtime/internal/record/reports.go`: the runs of the named playbooks whose
      `report_ref` is set and whose `parent_run_id` is null, ordered by identifier — the rule
      [data-model.md](./data-model.md) states on the two facts rather than on a list of trigger kinds
- [ ] T057 [US2] `runtime/internal/index/passage.go`: research.md §8's rules for a report, reading
      the JSON through the decoder's token stream so that order survives
- [ ] T058 [US2] `runtime/internal/index/reports.go`: a source whose documents are those runs, each
      identified by its run, digested over its report as recorded, and cut by T057's rules
- [ ] T059 [US2] `runtime/internal/collections/collections.go`: lift the refusal of `reports`
- [ ] T060 [US2] `runtime/internal/stage/retrieve/retrieve.go` and `results.go`, and
      `runtime/cmd/gronin/deployment.go`: the stage is handed the record store for a reports source,
      and each result from one is headed with the run it came from (FR-214)
- [ ] T061 [US2] `runtime/cmd/gronin/collections_cmd.go`: `list` and `show` for a reports collection

### Mutants for User Story 2

- [ ] T062 [US2] SC-208, command `go test ./internal/run -count=1 -run
      TestOnlyOriginalValidatedReportsAreRetrieved`: `a derived run's report is indexed` and `a run
      with no validated report is indexed`, in `internal/record/reports.go`; `a result does not name
      its run`, in `internal/stage/retrieve/results.go`. And `report keys are indexed`, in
      `internal/index/passage.go`, command `go test ./internal/index -count=1 -run TestReportPassages`

**Checkpoint**: a playbook reads what its own earlier runs concluded, and can trace each claim to
the run that made it.

---

## Phase 5: User Story 3 — A deployment turns on semantic search, and is told when it is not getting it (P3)

**Goal**: a collection with an embeddings API is ranked by meaning; an API that cannot answer, or
answers too slowly, refuses the run and names itself; and only that collection's redacted text
reaches it, only once something asks.

**Independent Test**: a collection against a stub on loopback whose fixed vectors put the nearest
passage sharing no word with the query. The retrieval finds it and the record says `semantic`. With
the stub stopped, the run is refused, the stub agent never starts, and nothing is recorded as
retrieved.

### The stub

- [ ] T063 [P] [US3] `runtime/internal/embed/embedtest/stub.go` and
      `runtime/internal/embed/testdata/exchange.json`: an `httptest` server on loopback answering in
      the shapes of research.md §7's exchange, which the fixture holds as research.md records it —
      `REPLACE_ME` for the model, three dimensions, nothing naming the server. Vectors come from a
      function of the input text the test supplies. It records every request, headers and body, and
      can answer with an error status and envelope, be closed, hold its response until the request's
      context ends, delay each answer by a set time, fail from its Nth request on, and answer short,
      reordered or wrong-length data. Its recording never reaches the test's output: a request
      carries the credential
- [ ] T064 [US3] `TestTheStub…` in `runtime/internal/embed/embedtest/stub_test.go`: each of those
      behaviours observed from a plain client, under `scripts/no-network.sh` as the suite runs, so
      a stub that cannot hold or refuse is found before a test relies on it (research.md §5)

### Tests for User Story 3

- [ ] T065 [P] [US3] `TestTheClient…` in `runtime/internal/embed/client_test.go`, every rule of
      [contracts/embeddings.md](./contracts/embeddings.md): the path, `model` and `input` as a list,
      no `encoding_format` or `dimensions`, at most 16 inputs, the bearer header when a credential
      is configured and none otherwise; vectors ordered by `index` from a reordered response; a
      refusal naming the URL for each observed error status with `error.message` quoted, for a
      short or duplicated `data`, for vectors of differing or zero length, and for no answer within
      the request bound
- [ ] T066 [P] [US3] In `runtime/internal/collections/collections_test.go`: `embeddings` accepted and
      the collection's mode `semantic`; refused, each by name — a literal credential, a reference to
      an unconfigured key, a reference to a key not marked secret, a URL that is not `http` or
      `https`, a URL carrying user information, an empty model
- [ ] T067 [P] [US3] SC-202, `TestNothingIsSentUntilAsked` in
      `runtime/cmd/gronin/semantic_binary_test.go`: a deployment with a lexical collection and a
      semantic one at a recording stub. The stub records nothing while `gronin serve` starts and
      runs (held open by `bintest.Start`), while `gronin collections list` and
      `gronin collections show` list the semantic collection's documents — US3 scenario 5's
      preview — and while a playbook retrieves from the lexical collection. Then
      `gronin collections rebuild` of the semantic one, and the stub records requests: the stub was
      reachable all along
- [ ] T068 [P] [US3] SC-204, `TestMeaningRatherThanWords` in
      `runtime/internal/stage/retrieve/semantic_test.go`: the stub's vectors put one passage nearest
      the query, and that passage shares no word with it. It is ranked first, and the outcome says
      `semantic`. The stub's vectors are not of unit length, and chosen so that the nearest by
      cosine is not the largest by raw dot product: the protocol does not promise unit vectors
      (research.md §7), and a scan that skips normalising ranks the wrong passage first
- [ ] T069 [P] [US3] SC-205, `TestAnUnreachableAPIRefusesTheRun` in
      `runtime/internal/run/semantic_refusal_test.go`, against a closed stub, a stub answering 500
      with an error envelope, and one holding its response: each run is `refused`, its error names
      the stub's URL, the stub agent never starts, and no retrieved item and no retrieval with the
      outcome `found` or `empty` exists under either mode. The held case is refused within the
      request bound plus slack, measured by the test's own watchdog with a deadline well short of
      the suite's; its `retrieval_timeout` is set above that deadline, so that only the request
      bound can end it in time
- [ ] T070 [P] [US3] SC-206, `TestTheRetrievalBound` in
      `runtime/internal/stage/retrieve/semantic_bound_test.go`: a collection whose update needs more
      requests than fit its `retrieval_timeout`, the stub answering each just inside
      `request_timeout`. The retrieval is refused within the retrieval bound plus slack. A prompt
      stub passes with the bound removed, which is why each answer is slowed
- [ ] T071 [P] [US3] SC-207, the semantic half, `TestASkippedFileNeverReachesTheAPI` in
      `runtime/cmd/gronin/collections_cmd_test.go`: T020's directory as a semantic collection, rebuilt
      and retrieved from; the outside file's marker is in no request the stub recorded
- [ ] T072 [P] [US3] SC-210, in `runtime/internal/stage/retrieve/model_change_test.go`.
      `TestAModelChangeReembeds`: after the model is changed between two retrievals, the stub
      receives every passage again and the second outcome names a new generation under a new
      identity. `TestAVectorOfAnotherLengthIsRefused`: the stub answers the query with vectors of
      another length under the same model name, and the retrieval is refused naming both lengths
- [ ] T073 [P] [US3] SC-211, the failing API, `TestAFailingRebuildKeepsThePreviousGeneration` in
      `runtime/cmd/gronin/rebuild_failure_test.go`: a semantic collection at generation G1, its
      sources changed, and a stub that fails from its second request; `gronin collections rebuild`
      exits non-zero naming the stub's URL, and `gronin collections list` still names G1
- [ ] T074 [P] [US3] SC-212, `TestOnlyThisCollectionReachesItsAPI` in
      `runtime/cmd/gronin/semantic_scope_test.go`: two semantic collections at two stubs, a lexical
      collection, and a gathered input the query is not formed from, each carrying its own marker.
      During a rebuild of the first semantic collection and a retrieval from it, its stub receives
      exactly its passages and that retrieval's query, each request carrying the configured
      credential; no marker appears in any captured request, and the second stub receives none
- [ ] T075 [P] [US3] SC-213, `TestNoSecretReachesTheAPI` in `runtime/internal/run/semantic_secret_test.go`:
      a configured secret placed in a document of the collection and in the trigger value the query
      is formed from appears in no captured request. The credential is `testsecret.Value`, and it
      appears in no row of `record.db`, no blob, and no file under `<state-dir>/index` — and in no
      line of the suite's output, which `scripts/run-suite.sh` already scans for it. A second run
      against the stub answering 401 is refused, and its recorded error holds no credential either:
      a refusal is where a client is likeliest to quote what it sent
- [ ] T076 [P] [US3] SC-215, the semantic half, in `runtime/internal/run/retrieve_replay_test.go`:
      `TestAReplayDoesNotSearch` gains a semantic case whose stub is stopped as well as its index
      deleted before the replay
- [ ] T077 [P] [US3] SC-216, the semantic half, in `runtime/internal/index/tiebreak_test.go`:
      `TestTiesFollowTheKey` gains a case whose tied passages get identical vectors, written in an
      order that disagrees with the key

### Implementation for User Story 3

- [ ] T078 [US3] `runtime/internal/embed/doc.go` and `runtime/internal/embed/client.go`: the client of
      [contracts/embeddings.md](./contracts/embeddings.md) — built with the URL, the model, the
      resolved credential and the deployment's redactor, which it applies to every input at the
      point it sends; each request under its own `request_timeout` derived from the caller's
      context, so the retrieval bound still holds over it
- [ ] T079 [US3] `runtime/internal/index/vectors.go`: vectors stored normalised with their length; a
      linear scan by dot product (research.md §6), ordered by similarity then `(source, ordinal)`;
      a query vector of another length refused (FR-218)
- [ ] T080 [US3] `runtime/internal/index/index.go`: the identity carries the URL and the model, never
      the credential; `Update` embeds the added and changed passages through an embedder it is
      handed, before its write transaction, and commits none of a generation whose embedding failed
- [ ] T081 [US3] `runtime/internal/collections/collections.go`: lift the refusal of `embeddings`; the
      credential rules of [contracts/cli.md](./contracts/cli.md), resolved through the configuration
      the way the MCP catalogue's references are
- [ ] T082 [US3] `runtime/internal/stage/retrieve/retrieve.go`: a semantic collection's update and
      search go through its client; every failure refuses the retrieval naming the URL, and nothing
      in the stage can reach the lexical search for a semantic collection (FR-207)
- [ ] T083 [US3] `runtime/cmd/gronin/deployment.go` and `runtime/cmd/gronin/collections_cmd.go`: a client
      per semantic collection, built only when a retrieval or `rebuild` needs it; `list` and `show`
      never build one

### Mutants for User Story 3

- [ ] T084 [US3] The client's contract, in `internal/embed/client.go` unless named, command
      `go test ./internal/embed -count=1 -run TestTheClient`: `the client asks for dimensions`, `a
      status other than 200 is accepted`, `vectors are taken in response order`, `a short data list
      is accepted`, `the credential is not sent`
- [ ] T085 [US3] SC-202, command `go test ./cmd/gronin -count=1 -run TestNothingIsSentUntilAsked`:
      `serve rebuilds every collection at startup`, in `cmd/gronin/serve_cmd.go`; `showing a
      collection updates its index`, in `cmd/gronin/collections_cmd.go`; `a lexical collection is
      embedded`, in `internal/stage/retrieve/retrieve.go`; and `rebuild sends nothing`, in
      `cmd/gronin/collections_cmd.go`, which only the closing half kills
- [ ] T086 [US3] SC-204, command `go test ./internal/stage/retrieve -count=1 -run
      TestMeaningRatherThanWords`: `a semantic collection is searched by words`, in
      `internal/stage/retrieve/retrieve.go`; `vectors are stored without normalising`, in
      `internal/index/vectors.go`
- [ ] T087 [US3] SC-205, command `go test ./internal/run -count=1 -run
      TestAnUnreachableAPIRefusesTheRun`: `an unreachable API falls back to the lexical search`, in
      `internal/stage/retrieve/retrieve.go`; `a request is not bounded`, in `internal/embed/client.go`
- [ ] T088 [US3] SC-206: `the retrieval bound is not applied`, in `internal/stage/retrieve/retrieve.go`,
      command `go test ./internal/stage/retrieve -count=1 -run TestTheRetrievalBound`
- [ ] T089 [US3] SC-207: T042's `the walk follows symbolic links` declared a third time, command
      `go test ./cmd/gronin -count=1 -run TestASkippedFileNeverReachesTheAPI`
- [ ] T090 [US3] SC-210: `the model is left out of the identity`, in `internal/index/index.go`, command
      `go test ./internal/stage/retrieve -count=1 -run TestAModelChangeReembeds`; `the vector length
      is not compared`, in `internal/index/vectors.go`, command
      `go test ./internal/stage/retrieve -count=1 -run TestAVectorOfAnotherLengthIsRefused`
- [ ] T091 [US3] SC-211, command `go test ./cmd/gronin -count=1 -run
      TestAFailingRebuildKeepsThePreviousGeneration`: `a failed embedding commits what it had`, in
      `internal/index/index.go`; `a failed rebuild exits zero`, in `cmd/gronin/collections_cmd.go`
- [ ] T092 [US3] SC-212, command `go test ./cmd/gronin -count=1 -run
      TestOnlyThisCollectionReachesItsAPI`: `a rebuild embeds every semantic collection`, in
      `cmd/gronin/collections_cmd.go`; and T084's `the credential is not sent` declared again
- [ ] T093 [US3] SC-213: `the client sends text unredacted` and `a refusal quotes the request it
      sent`, in `internal/embed/client.go`, both with command
      `go test ./internal/run -count=1 -run TestNoSecretReachesTheAPI`; `a literal credential is
      accepted` and `a credential need not be secret`, in `internal/collections/collections.go`,
      command `go test ./internal/collections -count=1 -run TestCollections`
- [ ] T094 [US3] SC-215 and SC-216, the semantic halves: `semantic ties are left in scan order`, in
      `internal/index/vectors.go`, command `go test ./internal/index -count=1 -run
      TestTiesFollowTheKey`; and T046's mutant, confirmed still killed now that its test stops the
      stub too

**Checkpoint**: every mode and every source of the feature is in place, and every way a retrieval
refuses is named in the run's record.

---

## Phase 6: Polish

- [ ] T095 [P] `specs/001-runtime-core/data-model.md`: the Run gains retrievals as a child, and the
      `refused` status's description gains the retrieval's causes, as this feature's data-model.md
      records it will
- [ ] T096 [P] `docs/playbook-format.md` documents the `retrieve` block and stops listing it as
      refused, and `docs/architecture.md`'s `retrieve` section describes the stage as built —
      lexical by default, over a directory or recorded reports, semantic as a deployment's option —
      rather than as a vector store
- [ ] T097 [P] `README.md` and `runtime/README.md`: retrieval is no longer refused, and the semantic
      mode is stated as what it is — a network dependency, a credential, and a third party that
      receives the collection's text — pointing at [contracts/cli.md](./contracts/cli.md) for
      `collections.json` rather than restating it
- [ ] T098 Before merging, reconcile with the guard as *Rebasing over the guard* lists, against the
      `production` of that moment: the migration takes the next free number unless it has merged;
      `validateReserved`, `playbook.Unknown`, `TestTheGateRefusesAReservedBlockThatReachesIt` and the
      mutant `the gate accepts a reserved block` go if nothing is left reserved; both schema copies
      carry both blocks; every mutant of both features is still found exactly once
- [ ] T099 SC-220: `scripts/check-mutation.py --self-test`, then `scripts/check-mutation.py`, report
      zero survivors with every mutant above declared; and each success criterion from SC-201 to
      SC-219 has at least one declared mutant whose command is scoped to that criterion's own test,
      per the table below. Run the suite through `scripts/run-suite.sh`, and let the repeat workflow
      run it against the unchanged tree: SC-211 and SC-216 are about order, and a flake there is a
      failure
- [ ] T100 Follow [quickstart.md](./quickstart.md) on a real host against a real embeddings server.
      It is the one place the stub's shapes meet a server other than the one research.md §7
      observed, and what it finds that the suite could not becomes a task here rather than a note

---

## Success criteria coverage

| Criterion | Test tasks | Mutant tasks |
| --------- | ---------- | ------------ |
| SC-201 | T015, T016, T002 | T040, T003 |
| SC-202 | T067 | T085 |
| SC-203 | T009, T017, T013 | T014, T041 |
| SC-204 | T068 | T086 |
| SC-205 | T069, T065 | T087, T084 |
| SC-206 | T070 | T088 |
| SC-207 | T019, T020, T071, T011 | T042, T089, T014 |
| SC-208 | T052, T053, T051 | T062 |
| SC-209 | T021 | T043 |
| SC-210 | T072 | T090 |
| SC-211 | T022, T073 | T044, T091 |
| SC-212 | T074, T065 | T092, T084 |
| SC-213 | T075, T066, T007 | T093, T014 |
| SC-214 | T023, T007 | T045 |
| SC-215 | T024, T076 | T046, T094 |
| SC-216 | T025, T077 | T047, T094 |
| SC-217 | T026 | T048 |
| SC-218 | T027, T018 | T049 |
| SC-219 | T028 | T050 |
| SC-220 | T099 | every task in the column above |

---

## Dependencies & Execution Order

- **Setup (Phase 1)** → **Foundational (Phase 2)** → the stories. T001 → T002 → T003; T004 is
  independent.
- **Within Phase 2**: T005 → T006 → T007. T008 → T009. T010 → T011. T012 needs T008 and T010; T013
  needs T012. T014 comes last in the phase, because a mutant is declared against code that exists.
- **US1 (Phase 3)** needs all of Phase 2. Its tests are written first and fail until the
  implementation lands. T029 and T030 → T031 → T032; T033 needs T031, T032 and T006; T034 needs
  T033; T035 needs T034; T036 needs T033; T037 needs T031 and T036; T038 needs T006; T039 needs
  T034 — the gate accepts the block only once something applies it. T017 needs T039. The mutant
  tasks T040-T050 follow the implementation they mutate.
- **US2 (Phase 4)** needs US1: a reports source is one more kind of document for the index, the
  stage and the listing US1 built. T056 → T058; T057 → T058; T058 → T060; T059 and T061 need T058.
  T051 and T057 both concern `internal/index/passage.go`'s report rules; T057 edits the file T029
  wrote.
- **US3 (Phase 5)** needs US1, and not US2: a semantic collection over a directory is complete
  without reports. T063 → T064, and every US3 test after T064. T078 → T080 → T082 → T083; T079 →
  T080; T081 before T083. Where US2 is in flight at the same time, T054 and T066 both edit
  `runtime/internal/collections/collections_test.go`, T055 and T071 both edit
  `runtime/cmd/gronin/collections_cmd_test.go`, and T059 and T081 both edit
  `runtime/internal/collections/collections.go`; those pairs run US2 first.
- **Polish (Phase 6)** follows the stories it documents. T098 comes before T099, which is last among
  the tasks that change the tree, because it is the audit of every mutant declared before it.

### Parallel opportunities

Within Phase 1, T001 and T004. Within Phase 2, T008 and T010 against each other and against the
record store's chain. Within each story, the tests marked `[P]` are in files no other task of that
story edits. The implementation tasks are not marked: each builds on the one before it, and several
touch `index.go`, `retrieve.go` or `execute.go`. **The mutant tasks are never parallel**: every one
of them edits `runtime/testdata/mutations.json`.

### Parallel example: User Story 1

```text
# Once Phase 2 is done, the tests of US1 are independent files:
T015 cmd/gronin/retrieve_binary_test.go     T022 internal/index/generation_test.go
T016 internal/stage/retrieve/query_test.go  T023 cmd/gronin/retrieve_record_test.go
T017 internal/playbook/validate_test.go     T024 internal/run/retrieve_replay_test.go
T018 internal/index/passage_test.go         T025 internal/index/tiebreak_test.go
T019 internal/index/walk_test.go            T026 internal/index/lexical_test.go
T020 cmd/gronin/collections_cmd_test.go     T027 internal/stage/retrieve/bounds_test.go
T021 cmd/gronin/retrieve_update_test.go     T028 internal/run/retrieve_refusal_test.go
```

### Parallel example: User Story 2

```text
T051 internal/index/passage_report_test.go  T054 internal/collections/collections_test.go
T052 internal/run/retrieve_reports_test.go  T055 cmd/gronin/collections_cmd_test.go
T053 internal/record/reports_test.go
```

### Parallel example: User Story 3

```text
# After T063 and T064:
T065 internal/embed/client_test.go                   T071 cmd/gronin/collections_cmd_test.go
T066 internal/collections/collections_test.go        T072 internal/stage/retrieve/model_change_test.go
T067 cmd/gronin/semantic_binary_test.go              T073 cmd/gronin/rebuild_failure_test.go
T068 internal/stage/retrieve/semantic_test.go        T074 cmd/gronin/semantic_scope_test.go
T069 internal/run/semantic_refusal_test.go           T075 internal/run/semantic_secret_test.go
T070 internal/stage/retrieve/semantic_bound_test.go  T076 internal/run/retrieve_replay_test.go
                                                     T077 internal/index/tiebreak_test.go
```

---

## Implementation Strategy

**The MVP is User Story 1**: Phases 1, 2 and 3. It is the stage in its default form — a directory
of runbooks, searched lexically inside the binary, recorded in full, replayable without the index —
and it is what the owner's decision made the default. It ships without US2 and US3 because nothing
in it depends on them: a catalogue entry naming `reports` or `embeddings` stays refused, rather than
declared and unsearchable.

Then US2, which is the source the pipeline's retrieve stage was named for and the one the runtime
already holds. Then US3, the option, which a deployment that wants no network dependency never
turns on.

## Notes

- The effect a binary test counts is what the stub agent quotes from its working directory. A
  results file that exists after a run is not evidence the agent read it; one the agent quoted is.
- Every test that asserts something did not travel places a marker in what must not travel and
  fails on the marker, rather than asserting that the expected content arrived — which a leaking
  implementation also satisfies.
- Every time bound here — the request, the retrieval — is tested against a subject that exceeds it,
  under a deadline of the test's own. The retrieval bound is exercised in the semantic mode, where
  a subject can exceed it; the lexical mode goes through the same deadline in `retrieve.go`, set
  once per retrieval rather than per mode.
- No test sleeps a fixed time and then asserts. Where a test waits, it polls with a deadline.
- Commit per task or per logical group. Stop at any checkpoint.
