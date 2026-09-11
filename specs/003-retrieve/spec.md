# Feature Specification: Retrieve

**Feature Branch**: `add_retrieve_spec`

**Created**: 2026-09-10

**Status**: Draft

**Input**: User description: "The retrieve stage: before the agent runs, search what the deployment
has chosen to index — its documents and what earlier runs reported — and hand the agent what was
found as gathered input. Lexical search built into the binary by default; a remote embeddings API
as an option a deployment configures."

## Clarifications

### Session 2026-09-10

- Q: Lexical search or semantic search? → A: both — the owner's decision, 2026-09-10, answering
  issue #16. Lexical search built into the static binary is the default. A remote embeddings API is
  an option a deployment may configure. The constraint that framed the choice: the release binary
  is built with cgo disabled and `scripts/check-static.sh` refuses one that is not, which rules out
  every local vector engine and embedding runtime reached through a C binding. What remains is a
  search written in Go, or an embeddings service reached over the network.

Two consequences follow from rules this repository already has, not from a new decision. A playbook
is portable data (Principle IV), so the endpoint, the model and the credential belong to the
deployment's configuration and never to a playbook — the division the guard specification made for
its coordination backend. And a configured embeddings API that cannot be reached refuses the
run rather than searching by words: a lexical search delivered under the name of a semantic one is
a declared bound that nothing applies, which is why that specification refuses a run rather than
fall back to the single-host lock when its backend cannot be reached.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - A playbook reads what the deployment's documents say (Priority: P1)

An operator keeps a directory of runbooks on the host. They declare it to the deployment as a
collection, and a playbook names that collection with a query formed from what triggered it. When
the playbook runs, the passages that match are in the run's working directory before the agent
starts, and the run's record says what was retrieved, from which state of the index, by which kind
of search. Nothing left the host.

**Why this priority**: it is the whole stage in its default form, and it is what every other story
refines. Principle III puts the record in the same slice: a retrieval nobody can inspect afterwards
does not ship.

**Independent Test**: declare a directory of three text files as a collection, write a playbook
whose retrieval query matches one of them and whose agent stage is the stub, run it, and confirm
the stub read the matching passage from the working directory and the record holds that passage,
the collection, the mode and the index generation.

**Acceptance Scenarios**:

1. **Given** a collection with no embeddings API configured, **When** a playbook retrieves from
   it, **Then** the results are in the working directory before the agent starts, and nothing is
   sent off the host.
2. **Given** a completed run that retrieved, **When** its record is read through the operator's
   own command surface, **Then** it names the collection, the mode, the index generation searched,
   the query as searched, and each result with its rank, its score, where it came from and its full
   content.
3. **Given** a recorded run that retrieved, **When** it is replayed, **Then** the agent receives
   the recorded results, and no index is searched.
4. **Given** a source document added, changed or removed since the last retrieval, **When** a
   playbook retrieves again, **Then** the results reflect the source as it now stands.
5. **Given** a collection whose directory does not exist, **When** a playbook retrieves from it,
   **Then** the run is refused and the refusal names the directory, rather than the agent being
   told nothing was found.
6. **Given** a playbook whose `retrieve` block names a mode, an endpoint or a collection the
   deployment does not declare, **When** the runtime loads it, **Then** it is refused naming the
   field.

---

### User Story 2 - A playbook reads what earlier runs concluded (Priority: P2)

An operator's disk-space playbook has run for months and each report says what it found. They
declare those reports to the deployment as a collection. The next time the playbook runs, the
reports from earlier runs whose content matches the new trigger are among its inputs, each naming
the run it came from.

**Why this priority**: it is what the pipeline's retrieve stage was named for — what was learned
the last time something like this happened — and it is the one source the runtime already holds
without the operator arranging anything. It is second because it needs a run history to exist, and
because it inherits everything User Story 1 establishes about indexing and recording.

**Independent Test**: seed a record store with a run whose report validated, a run whose report did
not, a replay of the first run and a resume of it. Retrieve from a collection over that
playbook's reports and confirm exactly one result, naming the first run.

**Acceptance Scenarios**:

1. **Given** a collection over a playbook's reports, **When** a later run retrieves from it,
   **Then** each result names the run that produced it.
2. **Given** a run whose report failed validation against its output schema, **When** its playbook
   later retrieves from its reports, **Then** that report is not among the results.
3. **Given** a replay or a resume of an earlier run, **When** its playbook later retrieves from
   its reports, **Then** neither one's report is among the results.

---

### User Story 3 - A deployment turns on semantic search, and is told when it is not getting it (Priority: P3)

An operator whose runbooks use different words from the alerts that should find them configures an
embeddings API for that collection. Retrieval from it then finds passages by meaning rather than by
shared words. When the API cannot be reached, the run does not quietly search by words instead: it
is refused, and the refusal names the API.

**Why this priority**: it is the option, not the default. It adds a network dependency, a
credential, and a third party that receives the collection's text; a deployment that wants none of
that keeps User Story 1 and loses nothing. It is last because everything it adds is on top of an
index, a record and a refusal path that User Story 1 already has to provide.

**Independent Test**: configure a collection against a stub embeddings API on loopback that
answers with fixed vectors, arranged so that the nearest passage shares no word with the query.
Retrieve and confirm that passage is found and the record says semantic. Stop the stub, retrieve
again, and confirm the run is refused, the agent stub never started, and nothing was retrieved
under any mode.

**Acceptance Scenarios**:

1. **Given** a collection with an embeddings API configured, **When** a playbook retrieves from
   it, **Then** passages are ranked by similarity to the query's embedding and the record says the
   mode was semantic.
2. **Given** a collection with an embeddings API configured, **When** the API refuses the
   connection, answers with an error, or does not answer within its bound, **Then** the run is
   refused before the agent starts and the refusal names the API.
3. **Given** a semantic collection indexed under one embeddings model, **When** the deployment
   configures another, **Then** no query embedded by the new model is compared with vectors from
   the old one.
4. **Given** a semantic collection, **When** its index is built and searched, **Then** the only
   text that reached the API is that collection's passages and the queries of retrievals from it,
   with the deployment's secret values removed.
5. **Given** an operator deciding whether to turn the option on, **When** they list the
   collection, **Then** they see every document it indexes and every file it skipped, so what the
   API would receive is known before anything is sent.

---

### Edge Cases

- **A semantic collection too large to embed within one run.** The first retrieval after the option
  is turned on has the whole collection to embed, and FR-209's bound can refuse it. The operator's
  rebuild of FR-220 does the same work outside any run and is how such a collection is brought into
  service.
- **A source changes after retrieval, while the agent is running.** The agent reads the copy in its
  working directory and the record holds that copy. The source is not read again for this run.
- **The embeddings API changes model behind an unchanged name.** The configuration still matches
  the index, so FR-217 cannot notice; the vectors change length, or they do not. FR-218 refuses the
  first case. The second is undetectable from the runtime's side and is stated here rather than
  claimed as covered.
- **A report retrieved, restated, and retrieved again.** A wrong conclusion in one report can be
  retrieved by the next run, repeated in its report, and retrieved again. The runtime cannot judge a
  report's correctness; what it can do is name the run each result came from (FR-214), so an
  operator can trace a repeated claim to its origin.

## Requirements *(mandatory)*

### Functional Requirements

#### The stage and its place in the pipeline

- **FR-201**: A playbook MUST be able to declare one or more retrievals, each naming a collection,
  a query, and the name its results are written under in the run's working directory — where the
  agent reads them as it reads gathered input.
- **FR-202**: Retrieval MUST run after the gather stage and before the agent stage, so that a query
  can be formed from gathered input, and so that a refused retrieval spends no token. A run a
  retrieval refuses is recorded as refused, as one whose gather step fails already is.
- **FR-203**: A playbook MUST NOT carry a retrieval mode, an endpoint, a model, a credential or a
  provider name. The mode is a property of the collection, set by the deployment's configuration.
- **FR-204**: The runtime MUST accept a `retrieve` block, validate its shape when the playbook
  loads, and refuse one that names a key it does not implement, a collection this deployment does
  not declare, a gathered input no gather step produces, or a results name another retrieval or a
  gather step already writes.

#### The two modes

- **FR-205**: A collection whose configuration names no embeddings API MUST be searched lexically,
  by a search built into the runtime's own executable, and retrieval from it MUST send nothing off
  the host.
- **FR-206**: A deployment MUST be able to configure an embeddings API for a collection. Retrieval
  from that collection is then semantic: its passages and the query are embedded by that API, and
  passages are ranked by similarity to the query.
- **FR-207**: A retrieval from a collection with an embeddings API configured that cannot obtain an
  embedding — the API unreachable, answering with an error, or exceeding FR-208's bound — MUST
  refuse the run and name the API. It MUST NOT search lexically instead, and MUST NOT hand the
  agent anything as retrieved.
- **FR-208**: Every request to an embeddings API MUST be bounded in time, and exceeding the bound
  MUST count as the API being unreachable.
- **FR-209**: The retrieval stage MUST complete within a declared bound, and exceeding it MUST
  refuse the run. FR-208 bounds each request; a stage making many requests, each inside its own
  bound, is not bounded by it.
- **FR-210**: The same index generation and the same query MUST produce the same results in the
  same order, in either mode. Equal scores MUST be ordered by where the passage came from — its
  source document or run, then its position within it — rather than by whatever order the index
  happens to hold.
- **FR-211**: The query MUST be searched as plain text. No word or character in it may be
  interpreted as search syntax, because a query can be formed from a trigger payload, which for a
  webhook is written by whoever sends the request.

#### What is indexed

- **FR-212**: The runtime MUST index only the sources the deployment's configuration declares for a
  collection: a directory on the host, or the agent reports this runtime has recorded for playbooks
  the configuration names.
- **FR-213**: Indexing a directory MUST NOT follow a symbolic link, wherever it points, so that
  nothing outside the directory is read through one and a link loop cannot hold the update. A file
  that is not text MUST be skipped, and every skipped file or link MUST carry the reason it was
  skipped.
- **FR-214**: A collection over reports MUST hold only reports that validated against their
  playbook's output schema, and MUST exclude the report of a replay — an experiment against
  recorded inputs — and of a resume, which records a copy of the report it resumed. Each result
  from it MUST name the run that produced it.
- **FR-215**: Operators MUST be able to list, for each collection, its mode, the index generation
  it holds and when that was built, every document indexed and every file skipped with the reason,
  and whether the sources have changed since the generation was built — through the operator's own
  command surface.

#### Where the index lives and how it is rebuilt

- **FR-216**: The index MUST live in the deployment's state directory and MUST be derived data
  only: removing it loses nothing that rebuilding from the sources does not restore.
- **FR-217**: Before searching, a retrieval MUST bring the collection's index up to date with its
  sources — documents added, changed and removed — and search the result. An index built under a
  different collection configuration, the embeddings model included, MUST be treated as empty,
  so that no query is compared with vectors another model produced.
- **FR-218**: A query embedding whose length differs from the vectors in the index MUST refuse the
  retrieval rather than be compared with them.
- **FR-219**: A search MUST read one complete index generation. An update or rebuild that fails or
  is interrupted MUST leave the previous generation in place, and two retrievals updating one
  collection at once MUST NOT leave a generation holding part of each.
- **FR-220**: Operators MUST be able to rebuild a collection's index in full, outside any run. A
  rebuild that fails MUST exit non-zero naming the cause.

#### What leaves the host

- **FR-221**: The runtime MUST NOT index a collection, or send any of its text anywhere, until a
  retrieval from it or an operator's rebuild asks. Declaring a collection is not consent to ship
  it.
- **FR-222**: For a collection with an embeddings API configured, the runtime MUST send that API
  only the collection's passage text and the query text of retrievals from that collection, with
  the API's own credential, to the endpoint configured for that collection.
- **FR-223**: Text sent to an embeddings API MUST first pass through the redactor the record store
  applies, so that no secret value the deployment holds reaches the API.
- **FR-224**: An embeddings API's endpoint, model and credential MUST come from the deployment's
  configuration, with the credential held as a secret value. The credential MUST NOT appear in a
  playbook, the index, a record or a log.

#### What is recorded

- **FR-225**: The record of every run MUST hold, for each retrieval: the collection, the mode, the
  index generation searched with when it was built and the configuration it was built under, the
  query as searched, and each result with its rank, its score, where it came from, and its full
  content as the agent received it — readable through the operator's own command surface.
- **FR-226**: Replaying a run MUST hand the agent the recorded retrieval results, and MUST NOT
  search an index or send anything to an embeddings API.
- **FR-227**: Each retrieval MUST be bounded in how many results it returns, how many bytes of
  retrieved content it hands the agent, and how long its query may be. Anything cut to fit MUST be
  recorded as truncated.
- **FR-228**: A retrieval that cannot be performed as declared — a source that cannot be read, a
  query empty once resolved — MUST refuse the run and name the cause. Only a search that ran and
  matched nothing is an empty result; the agent is then told nothing was found, and the record says
  so.

### Key Entities

- **Collection**: a named body of text the deployment declares and a playbook names. Has a source,
  a mode, and for semantic mode an embeddings API. Its name is the only part a playbook carries.
- **Source**: where a collection's documents come from — a directory on the host, or the recorded
  reports of named playbooks.
- **Passage**: the unit indexed and retrieved; a part of one document, carrying where it came from.
- **Index generation**: one complete state of a collection's index, with when it was built and the
  configuration it was built under. Replaced whole, never edited in place where a search can see it.
- **Retrieval**: one search a run performed — its collection, mode, query and generation — and the
  results it handed the agent, recorded in full.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-201**: A playbook retrieving from a lexical collection finds the passage its query matches,
  and the agent stub reads it from the working directory — FR-201 and FR-202. The stub's report has
  to quote the passage: a test asserting only that the results file exists passes against a stage
  that writes it after the agent has run.
- **SC-202**: In a deployment holding a lexical collection and a semantic one, with the semantic
  one's API a stub that records every request, the stub records none while the runtime starts,
  while playbooks retrieve from the lexical collection, and while nothing retrieves from the
  semantic one — FR-205 and FR-221. It records requests as soon as the operator rebuilds the
  semantic collection, which is what shows the stub was reachable the whole time: a stub nothing
  could reach records none either.
- **SC-203**: A corpus of playbooks whose `retrieve` blocks name a mode, an endpoint, a model, a
  credential, an unknown key, an undeclared collection, an undeclared gathered input, and a
  colliding results name is refused at load with the field named, and a valid block is accepted —
  FR-203 and FR-204. The runtime refuses every `retrieve` block today, so a test that only observes
  a refusal passes before the feature exists; the valid case is what makes this one able to fail.
- **SC-204**: Against a stub API whose vectors put the nearest passage sharing no word with the
  query, a semantic retrieval returns that passage first and the record says semantic — FR-206. A
  lexical search cannot find that passage, so an implementation searching by words under the
  semantic name fails here.
- **SC-205**: Against a stub API that refuses connections, one that answers with an error, and one
  that holds its response, a semantic retrieval refuses the run, the agent stub never starts, no
  result is recorded under any mode, and the refusal names the API — FR-207 and FR-208. The held
  case is refused within the request bound, measured by the test's own clock with a deadline well
  short of the suite's, so that an unenforced bound fails as an assertion rather than as a hung
  suite.
- **SC-206**: A semantic retrieval whose index update needs more requests than fit in the stage
  bound, each answered just inside the request bound, is refused within the stage bound — FR-209.
  A stub that answers promptly passes whether or not the stage bound exists, which is why the
  answers are slowed.
- **SC-207**: A collection directory holding a text file, a file that is not text, a symbolic link
  to a file outside it and one to its own parent is listed with the text file indexed and the other
  three skipped, each with its reason — FR-212, FR-213 and FR-215. The listing is asserted exactly,
  so an implementation that follows the link to its parent lists the text file more than once, or
  never finishes. The outside file's content carries a marker, and the
  marker appears in no result, no record, and no request the stub received.
- **SC-208**: A record store holding a run whose report validated, a run whose report did not, a
  replay of the first and a resume of the first returns, from a collection over that playbook's
  reports, exactly one result, naming the first run — FR-214. The resume's report is a copy of the
  original's and the fixture gives the replay the same one, so an implementation that includes
  either returns more than one.
- **SC-209**: Between two retrievals, one source document is added, one changed and one removed.
  The listing reports the sources changed before the second retrieval, and the second retrieval
  reflects all three changes. Its results are identical to those after an operator's full rebuild,
  and to those after the state directory's index is deleted and rebuilt by the next retrieval —
  FR-215, FR-216, FR-217 and FR-220. The removed document carries a marker, so an update that
  handles additions and ignores removals fails; and the listing must show a generation built after
  the deletion, which an index kept anywhere but the state directory survives to fail.
- **SC-210**: When the configured embeddings model changes between two retrievals, the stub
  receives the collection's passages again and the second record names a new generation built
  under the new model; when the stub instead answers with vectors of a different length under the
  same model name, the retrieval is refused naming the mismatch — FR-217 and FR-218.
- **SC-211**: A rebuild killed partway through, and one whose stub starts failing partway through,
  each leave the previous generation intact and named by the listing, and the failing one exits
  non-zero naming the API — FR-219 and FR-220. The kill is what makes the first
  case meaningful: an implementation that cleans up on the way out passes a graceful stop. Two
  retrievals updating one collection at once, their interleaving injected rather than hoped for,
  leave a generation matching one full rebuild.
- **SC-212**: A stub API recording every request, during a rebuild of a semantic collection and a
  retrieval from it, receives exactly that collection's passages and that retrieval's query,
  carrying the configured credential — FR-222 and FR-224. The deployment also holds a second
  semantic collection at a second stub, a lexical collection, and a gathered input the query is not
  formed from. Each of those carries its own marker; any marker in a captured request, or any
  request at the second stub, fails the test.
- **SC-213**: A secret value the deployment holds, placed in a semantic collection's document and in
  the trigger payload the query is formed from, appears in no request the stub captured; and the
  embeddings credential appears in no record, no index file and no line of the suite's output —
  FR-223 and FR-224.
- **SC-214**: From a run's record alone, read through the operator's command surface, the results
  file the agent received is reproduced byte for byte and the generation it came from is named;
  after the source changes and the index is rebuilt, the earlier run's record still shows what it
  retrieved then and the generation it was — FR-225. A record that stores a reference to the index
  rather than the content fails the second half.
- **SC-215**: A run replayed after its collection's index is deleted and its embeddings stub is
  stopped completes, and the agent receives a results file byte-identical to the original's —
  FR-226. A replay that searches again fails on the missing index or the stopped stub.
- **SC-216**: Against a collection whose passages tie on score, and were written to the index in an
  order that disagrees with FR-210's key, repeated retrievals return the same order, and it is the
  key's order, in both modes — FR-210. Written in the key's order, a fixture passes
  with the tie-break removed, which is why the order disagrees.
- **SC-217**: A query holding the lexical search's own operators — a negation word, a prefix
  wildcard, an unbalanced quote, a field filter — returns the same results as the same words
  searched as plain terms, and never errors — FR-211.
- **SC-218**: A query longer than its bound, a search matching more results than the declared
  count, and results larger than the byte bound are each cut to fit and recorded as truncated —
  FR-227. Each fixture exceeds its bound; a fixture inside the bound passes whether or not it is
  enforced.
- **SC-219**: A collection whose directory is missing, and a query empty once resolved, each refuse
  the run naming the cause; a query that matches nothing lets the run proceed, the agent receives
  a statement that nothing was found, and the record says the search returned nothing — FR-228.
  Each refusal is recorded with the status refused, not failed — FR-202. A missing directory read as
  an empty one fails the first case.
- **SC-220**: Each of the above has at least one test that fails when the behaviour it asserts is
  removed, shown by the mutation harness rather than by the suite passing, and the harness reports
  zero survivors on an unmodified tree.

## Assumptions

- **Hybrid ranking is out of scope.** The owner's decision gives a deployment both modes, one per
  collection; combining lexical and semantic scores for one collection is a separate feature.
- **Semantic search means an embeddings service outside the runtime.** Nothing in this feature
  embeds text in-process. A deployment that wants semantic search with nothing leaving the host can
  point the API at a service it runs on the same host; FR-222 bounds what leaves the runtime, and
  where the endpoint runs is the deployment's choice.
- **The index is per host.** A deployment run on two hosts, as the guard specification allows,
  holds two indexes, and a collection over reports sees the reports recorded on its own host. The
  runtime core already keeps run records locally.
- **Sources are a directory or recorded reports, nothing else.** A knowledge base behind an API, a
  URL, or a repository is indexed by arranging for its content to land in a directory. Fetching from
  a remote source is not this feature.
- **Rebuilding on a schedule is out of scope.** FR-217 keeps an index current at every retrieval,
  and FR-220's rebuild is a command anything that runs commands can invoke.
- **Decided in this specification rather than by the owner**, and open to being overturned: the
  mode is chosen per collection rather than per playbook; the two source kinds above; bringing the
  index up to date at retrieval rather than on a schedule; and excluding the reports of replays and
  resumes. Each follows from a constraint stated above, but none was put to the owner.
