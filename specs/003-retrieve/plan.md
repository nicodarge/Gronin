# Implementation Plan: Retrieve

**Branch**: `add_retrieve_spec` | **Date**: 2026-09-10 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/003-retrieve/spec.md`

## Summary

A stage between gather and the agent (FR-202) that searches a collection the deployment declared
and writes what it found into the run's working directory, where the agent reads it as it reads
gathered input (FR-201). Two modes, per the owner's decision of 2026-09-10: a lexical search inside
the binary, which is the default and sends nothing anywhere (FR-205), and a semantic search through
an embeddings API a deployment configures for a collection (FR-206).

Phase 0 settled the shape of the default before any of it is written, and it did so by running it:
SQLite's FTS5, with its BM25 ranking, is already compiled into the pure-Go driver the record store
links, and a binary using it passes `scripts/check-static.sh` ([research.md](./research.md) §1).
The lexical mode therefore adds no dependency at all. The semantic mode adds a network client
written against the standard library and a linear scan over stored vectors, which Phase 0 timed at
tens of milliseconds for tens of thousands of vectors (§6) — so neither mode brings a search engine
or a vector store with it.

The hard part is not the search. It is that retrieval is the first stage whose input is something
the runtime keeps between runs, so three questions a gather step never raised have to be answered
explicitly:

**What is indexed.** Only what the deployment declares, from two kinds of source — a directory on
the host, or the reports this runtime recorded (FR-212) — and no symbolic link is followed
(FR-213). The operator can list every document a collection holds and every
file it skipped (FR-215), which is also the complete answer to what an embeddings API would
receive.

**Where the index lives and how it changes.** In the state directory, as derived data (FR-216).
Every retrieval brings it up to date with its sources before searching (FR-217), replaces it whole
(FR-219), and treats an index built under another configuration as empty — which is what stops a
query embedded by one model being compared with vectors from another. A model that changes behind
an unchanged name is caught only when its vectors change length (FR-218). The operator's full
rebuild (FR-220) is the same work done outside a run, for a collection too large to embed inside
one.

**What leaves the host.** Nothing, in the default. With the option on: that collection's passages
and its queries, redacted, to that collection's endpoint (FR-222, FR-223), and only once a retrieval
or a rebuild asks (FR-221). When the endpoint cannot be reached, the run is refused and names it
(FR-207); it is never searched by words under the semantic name.

## Technical Context

**Language/Version**: Go, as the runtime core. No change.

**Primary Dependencies**: none new. Lexical search is FTS5 through `modernc.org/sqlite`, which
`runtime/go.mod` already requires for the record store (research §1). The embeddings client is
`net/http` and `encoding/json`. `github.com/blevesearch/bleve/v2` also built statically and was
rejected as a second search engine where the first is already linked.

**Storage**: two additions.

- **The index**, one SQLite database per collection under the state directory: an FTS5 table of
  passages for the lexical mode, and a table of unit-length vectors for the semantic one (research
  §6). A generation carries an identity derived from the collection's configuration and the digests
  of the sources it was built from, so two builds of the same sources carry the same identity —
  which is what lets SC-209 compare an updated index with a rebuilt one by more than their results.
  An update is one transaction, so a search sees the generation before it or the one after it and
  never part of either (FR-219). Whether a transaction is enough, or whether a generation needs to
  be a whole file swapped by rename, is decided by SC-211's kill rather than by argument.
- **The record store** gains a migration: one row per retrieval, naming its collection, mode,
  generation and query, and one row per result, naming its rank, score and source, with the content
  in the blob directory as gathered inputs already are (FR-225). Replay reads them back into the
  working directory instead of searching (FR-226). The runtime core's data model changes here, not
  only this feature's: the Run entity gains retrievals as a child, as it has gathered inputs, and
  its description of the `refused` status — a run that never reached the agent — gains the
  retrieval's causes beside the receipt mismatch and the failed gather step it names today
  (FR-202).

**Collections catalogue**: a file in the state directory beside the MCP server catalogue, loaded
the same way and absent meaning none. An entry names its source and, for the semantic mode, an
endpoint, a model and a reference to the credential held as a secret in the deployment
configuration (FR-224) — the same way an MCP server entry refers to its credential rather than
holding it. A playbook names an entry and nothing else (FR-203), and the load gate refuses a name
the catalogue does not hold (FR-204), as it refuses an MCP server the deployment does not provide.

**Testing**: the existing suite under Principle VI. The semantic mode is exercised against stub
APIs on loopback inside `scripts/no-network.sh`, which Phase 0 showed can answer, refuse, and hold
a response past a client's deadline with no network present (research §5). What a stub cannot show
is that a real API behaves as the stub does — Phase 0's first open question.

**Target Platform**: unchanged, and the static-link check is what holds it unchanged.

**Project Type**: an added stage in an existing pipeline, a catalogue beside the existing one, and
one schema block that stops being refused.

**Performance Goals**: not a throughput system. The bounds that matter are FR-208's per request and
FR-209's per stage; their values are Phase 0 outputs.

**Scale/Scope**: collections of hundreds to tens of thousands of passages. Past that, a linear scan
stops being a reasonable answer and a vector index is what would change.

## Constitution Check

### I. Bounds Are Declared and Enforced — PASS, and FR-207 is where it bites

The `retrieve` block joins the declared bounds: validated at load, and refused when it names a key
the runtime does not implement, a collection the deployment does not declare, a gathered input no
step produces, or a results name already written (FR-204). A mode, an endpoint, a model or a
credential in a playbook is refused on the same terms (FR-203).

The principle's sharper edge is FR-207: a semantic collection whose API cannot be reached refuses
the run rather than searching by words. Searching by words would still return results, the record
would still say something was retrieved, and the report would still read as informed — a bound
nothing applied, looking applied. FR-208 and FR-209 close the two ways the refusal could be
postponed indefinitely: a request that never answers, and a stage whose requests each answer in
time and together never finish.

The agent's reach is unchanged. The index and the sources live outside the working directory, which
the runtime core already refuses as a file-tool scope; what the agent sees of a collection is what
the stage wrote, bounded by FR-227.

### II. The Agent Reports, the Runtime Acts — PASS, with one thing to say about trust

Retrieval runs before the agent and writes only into the working directory; nothing the agent
produces reaches it in the same run. Across runs it does: a collection over reports (FR-214) hands
one run what an earlier run's agent wrote, and that agent may have been reading a trigger payload
someone outside the deployment wrote. Retrieved content is therefore untrusted input, exactly as a
gathered input is. The containment is the same one — an agent that is misled produces a bad report,
not an action — and naming the originating run on every result (FR-214) is what lets an operator
follow a repeated claim back to where it started.

### III. Every Run Is Inspectable — PASS, and it is most of this feature's record work

The constitution already names "the retrieved context" among what every record holds. FR-225
makes that concrete: the collection, the mode, the generation, the query as searched, and every
result's content as the agent received it. Content rather than a reference, because the index
changes under a run's feet and a record that points into it answers "what does the index say now",
not "what did this run see" — SC-214 fails a reference. Replay follows from it: FR-226 hands the
agent the recorded results and never searches, so a replay after the index has moved on is still a
replay of that run (SC-215).

FR-215 is this principle applied to the index itself. What a collection holds, what it skipped and
why, and whether its sources have moved since it was built, are read through the operator's own
command surface rather than inferred.

FR-228 is the other half of inspecting a retrieval: a source that cannot be read refuses the run
instead of reading as empty, because an operator looking at "nothing found" has no way to tell a
quiet collection from a missing one.

### IV. Playbooks Are Portable Data — PASS, and it decides where the mode lives

A playbook names a collection and its query (FR-201). Everything that belongs to one deployment —
the source's path, the mode, the endpoint, the model, the credential — is in the catalogue
(FR-224), and a playbook naming any of them is refused (FR-203). The query is interpolated on the
terms the runtime core already enforces: namespaced to the trigger payload or the deployment
configuration, never the process environment.

### V. Nothing From a Real Fleet Enters This Repository — PASS, with one new surface

The catalogue is configuration and is never committed; examples use documentation-reserved values.
The new surface is Phase 0's first open question: a captured exchange with a real embeddings API,
kept as the stub's fixture, has to hold vectors and a model name and nothing identifying an
account. It is reviewed as a fixture that came from outside, which it is.

### VI. The Suite Is the Gate — the principle this feature is hardest against, again

**Hermetic.** The semantic mode reaches a network by definition. It is tested against stubs on
loopback, which Phase 0 ran under `scripts/no-network.sh` (research §5). SC-202 and SC-212 go
further than a stub that answers: the stub records every request, so "nothing left the host" is
asserted from the receiving side rather than inferred from the absence of an error.

**Deterministic.** FR-210 is the requirement, and research §3 is why its test is harder than it
looks: no fixture there produced a tie, so a tie has to be built. SC-216's fixture writes tied
passages in an order that disagrees with the tie-break key, because a fixture written in the key's
order passes with the tie-break deleted. SC-211's concurrent case has to inject the interleaving of
two updates rather than start two and hope they overlap.

**A test must be able to fail.** Every success criterion says what makes its test fail, and SC-220
holds each of SC-201 through SC-219 to a mutant, with the harness reporting zero on an unmodified
tree. Some of them pass while asserting nothing unless they are built with care:

- SC-203 passes before the feature is written, because the runtime refuses every `retrieve` block
  today. Its valid case is what gives it something to lose, and the existing hostile fixture that
  declares a bare `retrieve` block changes meaning — refused today because the block exists, it must
  be refused afterwards only because of what it holds.
- SC-202 asserts that a stub received nothing, which a stub nobody could reach satisfies. It ends by
  rebuilding the semantic collection and seeing requests arrive, which proves the stub was reachable
  the whole time.
- SC-205 and SC-206 assert bounds, and a bound is exercised only by a subject that exceeds it — the
  point the guard plan makes about its own time bounds. The stub holds its response, or answers
  each request just inside the request bound, and the test's own deadline is well short of the
  suite's so that an unenforced bound fails as an assertion rather than as a hang.
- SC-204 fails a lexical search under the semantic name only because its fixture's nearest passage
  shares no word with the query. A fixture whose nearest passage also matches by words passes
  against exactly the implementation FR-207 exists to refuse.
- SC-207, SC-212 and SC-213 place a marker in everything that must not travel and fail on the
  marker, rather than asserting that expected content arrived — which a leaking implementation also
  satisfies.
- SC-208 and SC-209 count, and a count fails honestly; SC-209's removed document carries a marker
  so that an update handling only additions fails. SC-210 fails an implementation that keeps old
  vectors after the model changes, because the stub has to receive the passages again.
- SC-211 kills the rebuild rather than stopping it, since a graceful stop passes against an
  implementation that cleans up on its way out.
- SC-217 and SC-218 are built from inputs that exceed or abuse the thing under test — research §2's
  operators, fixtures beyond each bound — because inputs inside a bound pass whether or not it is
  enforced. SC-219 separates a missing directory from an empty one, and SC-201 fails a stage that
  writes its results after the agent has read the directory.
- SC-214 and SC-215 change or remove the index before reading the record or replaying, which is
  what fails an implementation that points into the index instead of copying out of it.

### Operational Constraints — two apply

**Secrets.** The embeddings credential is a secret value in the deployment configuration (FR-224),
sent in a request header by the runtime's own client, never on a command line. FR-223's redaction
is applied by the embeddings client itself, to every request body at the point it is sent — the way
the record store and the logger apply the redactor at their own write boundaries rather than
trusting their callers, so a new caller of the client cannot forget it. It is the redactor those
two are built with, not a second copy, so the three cannot come to disagree about what a secret
is.

**Configuration names.** The catalogue's keys, and the request and response fields of the
embeddings API, are verified against the running artifact and a captured real exchange before they
are used — which is why the wire protocol is an open question rather than a choice made here.

## Project Structure

### Documentation (this feature)

```text
specs/003-retrieve/
├── spec.md          # this feature's requirements
├── plan.md          # this file
├── research.md      # Phase 0 — six questions resolved by running them, two open
└── tasks.md         # not yet written
```

### Source Code (repository root)

```text
runtime/internal/
├── stage/retrieve/  # new — the stage: resolve the query, update, search, bound, write, record
├── index/           # new — generations, the FTS5 lexical index, the vector table and its scan
├── embed/           # new — the embeddings client, bounded per request
├── collections/     # new — the deployment's collections catalogue, beside mcpcatalog/
├── playbook/        # the `retrieve` block stops being refused outright (FR-204)
├── record/          # retrievals and retrieved items; the redactor reused by embed/
└── run/             # the stage wired between gather and agent; replay reads recorded results
```

`collections/` is separate from `mcpcatalog/` rather than a second section of it: the two answer
different questions, are refused for different reasons, and will change apart.

The operator commands — the listing of FR-215 and the rebuild of FR-220 — are Phase 1's
`contracts/cli.md` change, as are the playbook schema's `retrieve` block and the catalogue's shape.

## Phase 0 — Partly resolved

Six questions answered by running throwaway programs; findings and method in
[research.md](./research.md).

1. **Lexical search** is FTS5 in the driver already linked, statically built and accepted by the
   check (§1).
2. **A raw query is FTS5 syntax**, including an unbalanced quote that fails the search, so FR-211 is
   an obligation on the stage (§2).
3. **Scores are only comparable within one retrieval**, so the bounds are count and bytes, never a
   score threshold (§3).
4. **An updated FTS5 index matched a rebuilt one** to every printed digit, in one probe (§4).
5. **Stubs on loopback work inside the no-network namespace**, including one that holds its
   response (§5).
6. **A linear scan is fast enough** at the scale this targets, so no vector engine (§6).

Open, and the first gates `tasks.md` the way the guard plan's first question gates its own:

1. **What pins the stub to a real embeddings API?** Research's first open question. The wire
   protocol is not chosen and no response has been observed. The trap is issue #16's: every defect
   the runtime core's walkthrough found was its stub disagreeing with the real executable, and a
   contract test proves the runtime uses the API as the stub describes it, not that the API is
   described correctly.
2. **Passage boundaries, and what text of a report is indexed.** Research's second.
3. **The bounds' values** — per request (FR-208), per stage (FR-209), and FR-227's result count,
   retrieved bytes and query length — and which of them a playbook may declare and which the
   deployment sets. Each is a chosen threshold, so each gets a stated reason and a date.
4. **Which tokenizer.** Research §1 showed the Porter stemmer matching `disk` against `Disks`; it is
   also English-only, and whether it helps or harms a collection in another language was not
   measured. Whether the tokenizer is a per-collection choice is open.
5. **How a directory's changes are detected** for FR-217 — a content digest per file is exact and
   reads every file on every retrieval; size and modification time are cheap and can miss an edit.

## Complexity Tracking

One deliberate complexity, and it is the option rather than the default: the semantic mode gives a
deployment that turns it on a network dependency, a credential, and a third party that receives the
collection's text. The default keeps none of those, which is why the owner's decision made lexical
the default and semantic the option rather than the reverse.

| Choice | Why | Simpler alternative rejected because |
| ------ | --- | ------------------------------------ |
| Every retrieval brings the index up to date (FR-217) | A collection over reports is useless if the last run's report is invisible until someone rebuilds | Rebuilding only on command is one code path instead of two, and makes the newest report — the one most likely to matter — the one never found |
| Refuse rather than fall back when the API is unreachable (FR-207) | A lexical result under the semantic name is a bound that reads as applied and is not | Falling back keeps runs going, and turns an outage of the embeddings service into reports that look informed and are not, with nothing in the run saying so |
| Record retrieved content, not a pointer into the index (FR-225) | The index changes between runs; a pointer answers what it says now, not what the run saw | A reference is smaller, and makes every replay and every inspection after the next update describe a run that did not happen |
| A linear scan rather than a vector index | Research §6 timed it inside tens of milliseconds at this scale, with no dependency | An approximate index scales further and brings a dependency, a build step, and results that differ from an exact scan, which SC-216's determinism would then have to account for |
