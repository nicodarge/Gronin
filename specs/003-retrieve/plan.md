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
  §6). A passage is at most 1,024 bytes, cut at blank lines and Markdown headings, and a report is
  indexed by its JSON values rather than its text (research §8). Every collection uses the
  `unicode61` tokenizer (research §10). A generation carries an identity derived from the
  collection's configuration — the tokenizer included and, for the semantic mode, the endpoint and
  the model — and the content digests of the sources it was built from, so two builds of the same
  sources carry the same identity. The digests are exact because every file is digested on every
  retrieval, with no size-and-time check in front (research §11): such a check missed most
  same-size rewrites landing within one clock tick, and a missed edit would carry the previous digest
  forward and the identity with it.
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
holding it. The endpoint is the full URL of an OpenAI-compatible embeddings resource (research
§7). An entry also carries the two time bounds a playbook cannot know, since they depend on how fast
the endpoint answers: FR-208's per request and FR-209's per retrieval (research §9). A playbook names an entry and nothing else (FR-203), and the load gate refuses a name
the catalogue does not hold (FR-204), as it refuses an MCP server the deployment does not provide.

**Testing**: the existing suite under Principle VI. The semantic mode is exercised against stub
APIs on loopback inside `scripts/no-network.sh`, which Phase 0 showed can answer, refuse, and hold
a response past a client's deadline with no network present (research §5). The stub answers in the
shapes research §7 observed from a real server, errors included. What it still cannot show — another
provider's behaviour, a refused credential, silent truncation — is listed there.

**Target Platform**: unchanged, and the static-link check is what holds it unchanged.

**Project Type**: an added stage in an existing pipeline, a catalogue beside the existing one, and
one schema block that stops being refused.

**Performance Goals**: not a throughput system. The bounds that matter are FR-208's per request and
FR-209's per retrieval, set by the deployment for each collection, and FR-227's, which a playbook
declares within the runtime's ceilings. Their values and the reason for each are in research §9.

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
The new surface is research §7's captured exchange with a real embeddings API, which the stub's
fixture is built from. As recorded there it holds a request, vectors cut to three dimensions, and
`REPLACE_ME` where the model's name was: nothing naming the server, the account or the models. It
is reviewed as a fixture that came from outside, which it is.

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
are used. Research §7 did the second half for the wire protocol; the catalogue's keys are
Phase 1's.

## Project Structure

### Documentation (this feature)

```text
specs/003-retrieve/
├── spec.md          # this feature's requirements
├── plan.md          # this file
├── research.md      # Phase 0 — every question resolved by running it, limits listed
├── data-model.md    # Phase 1 — the catalogue, the index and its generations, the record
├── contracts/
│   ├── cli.md                 # the operator commands, collections.json, the results file
│   ├── embeddings.md          # what the client sends and accepts, from research §7
│   └── retrieve.schema.json   # the playbook's retrieve block
├── quickstart.md    # Phase 1 — validation against a real host and a real embeddings server
└── tasks.md         # the task list
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

The operator commands — the listing of FR-215 and the rebuild of FR-220 — are in
[contracts/cli.md](./contracts/cli.md), with the catalogue's shape; the playbook's `retrieve` block
is [contracts/retrieve.schema.json](./contracts/retrieve.schema.json).

## Phase 0 — Resolved

Eleven questions answered by running throwaway programs; findings and method in
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
7. **The wire protocol is OpenAI-compatible**, pinned to a real server's answers, errors included
   (§7). It returned the same vectors as Ollama's own protocol, which answers an empty input with
   200 and no vectors at all. Both truncate an input past the context without saying so, which is
   why the runtime bounds passages and queries itself. The section ends with what a stub built from
   the exchange pins and what it cannot — the answer to issue #16's trap for this stage.
8. **A passage is at most 1,024 bytes**, cut at blank lines and Markdown headings, sized to fit the
   smallest context observed; granularity did not move ranking measurably. **A report is indexed by
   its JSON values**, never its text: indexed as text, every report matched its own key names, the
   empty one included (§8).
9. **The bounds' values**, each with its reason and dated 2026-09-11 (§9): the query at 1,024 bytes
   and passages at 1,024, fixed by the runtime; results at 10 and bytes at 16 KiB unless the playbook
   declares otherwise, within ceilings of 50 and 64 KiB; each request at 60 seconds and each
   retrieval at 2 minutes unless the deployment sets otherwise for the collection. FR-208, FR-209
   and FR-227 now say who declares which.
10. **One tokenizer, `unicode61`, for every collection** (§10). On French and English manual pages no
    alternative, Porter included, differed from it by much more than one standard error, and the
    sign flipped with the passage rule. A per-collection choice would be a setting whose effect
    cannot be told from noise.
11. **Every file is digested on every retrieval** (§11). A size-and-time check missed most same-size
    rewrites landing within one clock tick; the digest costs about 60 milliseconds on a directory of
    runbooks. A file over 1 MiB is skipped with that reason, now in FR-213.

None of what research lists as still open gates `tasks.md`. Each is a limit on what the evidence
covers — one server observed, a corpus of manual pages rather than a deployment's own, truncation
invisible in a batch response — and each is stated where a reader of the stub, the rules or the
listing will meet it.

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
