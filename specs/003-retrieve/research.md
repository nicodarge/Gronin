# Phase 0 — Research

Sections 1 to 6 were answered by running throwaway programs on 2026-09-10, and sections 7 to 11 on
2026-09-11, with Go 1.27.1 on linux/amd64 and `modernc.org/sqlite` at v1.58.0 wherever SQLite is
involved. None of the programs is committed; each is described closely enough to be run again.
What running them did not settle is listed at the end.

Sections 8, 9 and 10 share one corpus, chosen because anyone on Debian 13 can rebuild it: the manual
pages installed in both French (`/usr/share/man/fr`) and English (`/usr/share/man`), each rendered to
plain text with `MANWIDTH=80 man -E UTF-8 -l <page> | col -bx`. Where a probe needs a query with a
known answer, the page's own NAME (or NOM) description is the query and that section is removed
from the page before it is indexed, so a page is never found by matching its own summary. That
leaves pages carrying a description in both languages, the same pages in each, which is what lets
the two languages be compared at all. The repository's own Markdown is the third set in section 8.

## 1. A lexical search that survives the static-binary check

**Question**: is there a BM25 search that builds with cgo disabled into a binary
`scripts/check-static.sh` accepts?

**Method**: a program opening an in-memory database through `modernc.org/sqlite` at v1.58.0 — the
driver and version `runtime/go.mod` already pins for the record store — reading its compile
options, creating FTS5 tables under three tokenizers, and ranking a query with `bm25()`. Built with
`CGO_ENABLED=0`, then handed to `scripts/check-static.sh`.

**Answer**: yes, and it adds no dependency. The driver reports SQLite 3.53.4 with `ENABLE_FTS5`
among its compile options. The `unicode61`, `porter unicode61` and `trigram` tokenizers were all
accepted and `bm25()` ranked the matching rows. Under `porter unicode61` the word `disk` matched a
document holding only `Disks`; under `unicode61` alone it matched nothing. The check printed
`check-static: ./fts5probe ok (linux, CGO_ENABLED=0)` and `file` called the binary statically
linked.

**Alternative, also run**: `github.com/blevesearch/bleve/v2` at v2.6.1, with its index mapping's
scoring model set to `bm25`. It built with cgo disabled, the same check accepted it, and it ranked
a three-document fixture. It was not chosen because it is a second search engine: `go list -m all`
named 53 modules for its probe, the probe's own included, where the FTS5 path is inside a module
the runtime already links. A BM25 written in this repository was the fallback had neither built,
and is not needed.

**Consequence for the design**: FR-205's lexical search is FTS5 through the existing driver.

## 2. A query handed to FTS5 verbatim is a query in FTS5's language

**Question**: FR-211 requires the query to be searched as plain text. Does that happen by default?

**Method**: the same probe, matching four documents against raw strings, then against the same
strings with each whitespace-separated word wrapped as an FTS5 string, embedded quotes doubled,
and the words joined with `OR`.

**Answer**: no. `disk NOT logs` returned nothing, because `NOT` was read as an operator and removed
both matching documents. `cert*` was read as a prefix search. `disk" AND (` failed outright with
`SQL logic error: unterminated string`. Quoted, `NOT` became an ordinary word, the query matched the
two documents holding `disk` or `logs`, and the hostile string searched without error.

**Consequence for the design**: FR-211 is an obligation on the implementation, not a property the
engine provides. The query is split and quoted before it reaches `MATCH`, and SC-217's fixture is
the three strings above plus a field filter.

## 3. Scores on a small collection

**Question**: what does `bm25()` produce on a collection the size of a first deployment's?

**Method**: the same probe, four documents, a query whose words each appear in two of them, scores
printed to seventeen significant digits.

**Answer**: the two matching documents scored `-2.93e-6` and `-2.57e-6`. With four more documents
that match nothing added to the collection, the same two scored `-2.27` and `-1.93`. The order held;
the magnitude moved by six orders.

**Consequence for the design**: a score means something only against the other scores of the same
retrieval. FR-227 bounds a retrieval by count and by bytes, never by a minimum score, because a
threshold that suits one collection's size discards everything in a smaller one. The score is still
recorded (FR-225), as a rank's evidence rather than as a measure. FR-210's tie-break is reached by
exact ties, so SC-216's fixture needs passages that tie exactly: the same words, at the same
length.

## 4. An index brought up to date matches one built from scratch

**Question**: FR-217 has a retrieval update the index rather than rebuild it. Does an updated FTS5
index rank exactly as a fresh one does?

**Method**: two in-memory indexes over six documents. One took them in order. The other took them in
reverse, then a seventh document that was deleted again, then one of the six deleted and inserted
again. Both were queried with a three-word query and their results printed to seventeen significant
digits. Its output was then hashed on two further runs.

**Answer**: identical, to every printed digit, and the two hashes agreed. That is one probe, not a
proof: SC-209 is where the property is held, against the implementation.

## 5. The semantic mode can be tested with no network

**Question**: Principle VI requires the suite to pass with no network. Can a stub embeddings API be
reached, and can its misbehaviour be staged, inside `scripts/no-network.sh`?

**Method**: a four-test Go package run under `scripts/no-network.sh go test -race -count=1`. One
stub answers on loopback; one holds its response until the test ends, against a client with a 200
millisecond deadline; one is closed before it is called; and one request goes to `example.com`.

**Answer**: all four behaved as a test needs. The answering stub returned `200 OK`. The held
response was cut off by the deadline at 200 milliseconds. The closed stub gave
`connect: connection refused`. `example.com` failed to resolve. The script reported that it isolated
the run with an unprivileged user namespace. The suite's sink tests already reach `httptest`
servers on loopback under the same script, so the mechanism is not new to this repository.

**What this does not establish**: that a stub's idea of an embeddings API matches a real one — see
§7, which observed one real API and states what a stub built from it still cannot show.

## 6. Similarity search needs no vector engine

**Question**: is a linear scan over stored vectors fast enough, or does semantic mode need an index
structure — and with it a dependency?

**Method**: a Go benchmark, one goroutine, vectors held in memory, taking the dot product of a query
against every vector and keeping the best. Three runs of twenty iterations each, on a 2.4 GHz
server processor released in 2014.

**Answer**: 10,000 vectors of 768 dimensions took 9.2 to 9.8 milliseconds per scan; 50,000 of 1,024
took 62 to 65 milliseconds. Neither figure includes reading the vectors from disk.

**Consequence for the design**: a linear scan, in Go, with no vector engine. A dot product is a
cosine only on vectors of unit length, so vectors are normalised when they are stored.

## 7. The wire protocol, pinned to an observed API

**Question**: which protocol does the embeddings client speak, and what does a real server answer —
when it succeeds and when it refuses?

**Method**: a program sending hand-built requests to an Ollama 0.34.0 server on a CPU-only machine,
at both its own endpoint, `/api/embed`, and its OpenAI-compatible one, `/v1/embeddings`. It used the
two embedding models the server already held, and pulled none: model A, 768 dimensions with a
512-token context, and model B, 1,024 dimensions with an 8,192-token context as the server's model
listing declares it. Against each model: one input, three inputs, `encoding_format: base64`,
`dimensions: 256`, an empty string, an empty list, a list holding an empty string, no input, an
input far past either context, the same with `truncate: false`, and a token array. Then an unknown
model, a completion-only model, malformed JSON, no model, and a bearer token. Vectors were compared
across repeated requests, one input alone against the same input in a batch, and the native
endpoint against the OpenAI-compatible one. The server's name and address and the models' names
are deliberately not recorded here.

**Answer**: an exchange on the OpenAI-compatible endpoint, sanitised — the model name replaced and
each vector cut to its first three dimensions, which are otherwise as the server returned them:

```http
POST /v1/embeddings HTTP/1.1
Content-Type: application/json
Authorization: Bearer REPLACE_ME

{"model": "REPLACE_ME", "input": ["disk full on /var", "certificate expired", "bonjour"]}
```

```json
{"object": "list",
 "data": [{"object": "embedding", "embedding": [-0.05774, 0.006852, -0.01974], "index": 0},
          {"object": "embedding", "embedding": [-0.1169, -0.005786, -0.005202], "index": 1},
          {"object": "embedding", "embedding": [0.02383, 0.04432, -0.008450], "index": 2}],
 "model": "REPLACE_ME",
 "usage": {"prompt_tokens": 17, "total_tokens": 17}}
```

Every case, on both models unless the row says otherwise:

| Request | `/v1/embeddings` | `/api/embed` |
| ------- | ---------------- | ------------ |
| one input, or several | 200; `data[i].index` in input order | 200; `embeddings` in input order |
| `encoding_format: base64` | 200; each embedding a base64 string | not sent |
| `dimensions: 256` | 200; vectors of length 256, unit length | not sent |
| `""` alone | 200; one vector | 200; **no vectors at all** |
| `[]` | 400, "invalid input" | 200; no vectors |
| `["disk", ""]` | 200; two vectors | 200; two vectors |
| no `input` field | 400, "invalid input" | 200; no vectors |
| an input past the context | 200; **silently truncated**, `usage.prompt_tokens` equal to the context | 200; silently truncated |
| the same with `truncate: false` | 200; still truncated — the field is ignored | 400, "the input length exceeds the context length" |
| a token array | 400, "invalid input type" | not sent |
| an unknown model | 404, `not_found_error` | 404 |
| a completion-only model | 501, `api_error` | 501 |
| malformed JSON | 400, `invalid_request_error` | 400 |
| no `model` field | 404, "model '' not found" | not sent |
| a bearer token | 200 — the header is ignored | not sent |

The OpenAI-compatible endpoint's errors are one envelope, `{"error": {"message", "type", "param",
"code"}}`, with `param` and `code` null in every case seen; the native endpoint's are
`{"error": "<message>"}`. Vectors were identical, to the last bit, across repeats, between one
input sent alone and in a batch, and between the two endpoints; every one had unit length as
returned. Model B truncated at 2,048 tokens, not at the 8,192 its listing declares: the context in
force is a setting of the server, not something the model listing reports. The first request after
the server had been idle included 4.1 seconds (A) and 5.1 seconds (B) of model loading, and one
input at the full context took 2.0 to 2.5 seconds on A and 19 to 20 seconds on B.

**Decision**: the OpenAI-compatible protocol. The catalogue names the full URL of the embeddings
resource, so a service that mounts it under another path is still reachable. Three reasons, all from
the table: the two protocols returned the same vectors bit for bit, so the choice costs nothing
against Ollama; the native one reaches Ollama only; and the native one answers an empty string, an
empty list and a missing field alike with 200 and no vectors, which a client trusting the status
reads as success. The one thing the native protocol has that the other lacks is `truncate: false`,
and that is answered by bounds rather than by the API: passages and queries are cut by the runtime
to fit the smallest context observed (§8, §9).

What the client does, each rule traceable to a row above:

- It sends `model` and `input` as a list of strings, and never `encoding_format` or `dimensions`:
  one changes the type of what comes back and the other changes the vector length under an
  unchanged model name, which FR-218 would then refuse on every query.
- It never sends an empty string. FR-228 already refuses an empty query, and the passage rules of §8
  produce no empty passage.
- Any status but 200 is FR-207's refusal, naming the API and quoting `error.message` when the
  envelope parses.
- It refuses a response whose `data` does not hold exactly one entry per input, each `index` once,
  every vector of one non-zero length, and it orders vectors by `index`, never by position.
- It normalises vectors when storing them, as the plan already says: unit length was observed from
  this server, and the protocol does not promise it.
- It sends the credential as `Authorization: Bearer`. This server ignored it.

**What a contract test built from this pins, and what it cannot**: a stub replaying these shapes
pins the request the client writes — its path, its fields, and the absence of the two optional
fields — and its decoding of the response above. It pins the refusal on each observed error status
and envelope, on a short or reordered `data`, and on a vector of the wrong length. That is more than
the stub guarded before, because the shapes came from a server rather than from a reading of its
documentation. It cannot pin:

- That any other service answers the same way. OpenAI's own was not called — no credential — so its
  shapes are the documented ones, not observed ones.
- The credential path. This server ignored the header, so no refusal of a wrong credential, and no
  401 shape, has been observed from anything.
- Truncation. A batch response says nothing about which input was cut; `usage` is the batch's
  total.
- Latency under load, and whether the vectors mean anything — including a model changed behind its
  name at the same length, which the specification's edge cases already state as undetectable.

The stub records one server on one date. It is evidence of that server, not a specification of the
protocol.

## 8. Where passages begin and end, and what text of a report is indexed

**Question**: what is the unit indexed and handed to the agent, and which text of a JSON report is
indexed?

**Method**: four probes.

- **Sizes**: every document of the three sets split into blocks at blank lines, with a Markdown ATX
  heading (`#` to `######` followed by a space) opening a new passage, blocks packed in order while
  they fit a cap, and a block larger than the cap cut at its last whitespace before it. The
  distribution printed for caps of 1, 2 and 4 KiB.
- **Fit**: forty passages of 1.5 to 2 KiB from each set sent to both models of §7, one per request,
  to `/api/embed` with `truncate: false`, so that one past the context is refused rather than cut.
  Bytes per token from `prompt_eval_count` less the two tokens an empty input costs.
- **Granularity**: the known-item probe of §10 under `unicode61`, with pages indexed whole and as
  1 KiB passages. A page counts as found at the rank of its first passage.
- **Reports**: three reports in the shape of `examples/doc-check.yaml`'s output schema — two holding
  one finding each, one an empty findings list — indexed once as their JSON text and once as their
  string and number values joined by blank lines, then queried for the key names, for words from
  the values, and for `nthe`.

**Answer**:

- Whole documents are large beside what a result can carry: a median of 4.8 KB and a largest of
  99 KB among the French pages, a median of 9.2 KB among the repository's Markdown. Paragraphs are
  small: a median of 161 bytes in French, 142 in English, 207 in the Markdown, and 2.2 KB at the
  ninety-ninth percentile of the Markdown. With a 1 KiB cap the median passage is 878, 893 and
  636 bytes.
- The densest text measured was 3.41 bytes per token (Markdown), the median 3.8 to 4.8 depending on
  the set. Model A, with its 512-token context, refused 20 of the 40 Markdown passages of 1.5 to
  2 KiB and 1 of the 40 English ones; model B refused none.
- Granularity did not move ranking measurably: the mean reciprocal rank was 0.619 whole and 0.599
  as passages in French, 0.679 whole and 0.691 as passages in English, with unicode61. Paired over
  the same 233 queries, passages minus whole is −0.020 ± 0.017 in French and +0.012 ± 0.018 in
  English (standard error), each within about one standard error of zero.
  What it moved is what the agent is handed per result: a whole page, 99 KB at the largest, or at
  most 1 KiB.
- Indexed as JSON text, a query for `findings` matched all three reports, the empty one included;
  `title` and `body` matched both non-empty ones; and `nthe` matched the first, because the escaped
  newline `\n` before `The` became part of a token, which also put a bare `n` in the vocabulary.
  Indexed as values, no key name matched anything, `nthe` matched nothing, and `removed`, `journald`
  and `97` each matched the report that holds them.

**Decision**:

- A passage is at most 1,024 bytes, fixed by the runtime. Blank lines separate blocks; an ATX heading
  opens a new passage; blocks are packed in order while they fit; a block longer than the cap is cut
  at its last whitespace before the cap, or at a character boundary when there is none. The cap is
  set by the context rather than by ranking: at 3.41 bytes per token, 1,024 bytes is about 300
  tokens, inside the smallest context seen with room to spare, where 2 KiB is not.
- Other heading forms — setext underlines, a manual page's capitalised section names — are not
  recognised. On a manual page that leaves a section's name attached to its first paragraph, which
  is where a reader meets it anyway.
- A passage carries its source — the file, or the run for a report — its ordinal within it, and its
  byte offset. That is FR-210's tie-break key.
- A report's indexed text is the string and number values of its JSON, in the order the document
  holds them. Keys, booleans and nulls are not indexed. Order is read from the decoder's token
  stream: decoding into a map loses it, and FR-210 orders ties by position. Each value is a block,
  and each object that is an element of an array opens a new passage, so a finding's title and body
  stay together whenever they fit. A report holding no values contributes no document.

## 9. The bounds' values, and who declares each

**Question**: what are the values of FR-208's and FR-209's bounds and of FR-227's result count,
retrieved bytes and query length, and which of them does a playbook declare?

**Method**: the timings of §7, and two more measurements. Batches of 1, 16 and 64 inputs of about
1,000 bytes each took 0.81, 11.6 and 44.7 seconds on model A, and 1.45, 20.9 and 82.3 seconds on
model B. And a lexical index of the French passages of §10 under `unicode61`, queried with strings
of corpus words quoted and joined as §2 requires, best of five runs each:

| Query | 59 bytes | 249 bytes | 1,003 bytes | 4,094 bytes | 16,378 bytes |
| ----- | -------- | --------- | ----------- | ----------- | ------------ |
| Time | 16.7 ms | 38.7 ms | 167 ms | 1.06 s | 20.0 s |

A lexical update, for comparison, indexed 2,989 passages in 0.11 seconds.

**Decision**: each value below is a chosen threshold, set on 2026-09-11 for the reason beside it.

| Bound | Value | Declared by | Reason |
| ----- | ----- | ----------- | ------ |
| Query length (FR-227) | 1,024 bytes, cut at the last whitespace before it and recorded as truncated | the runtime | Lexical cost grows faster than the query: from 1 to 4 KiB, four times the length cost six times the time, and from 4 to 16 KiB nineteen times; and a webhook's sender writes the query. At 3.41 bytes per token, 1 KiB also fits the smallest context seen, past which the API cuts silently (§7). A playbook's author cannot know the model's context, which belongs to the deployment |
| Passage size | 1,024 bytes | the runtime | §8 |
| Results per retrieval (FR-227) | 10 unless the playbook says otherwise; at most 50 | the playbook, refused at load above the ceiling | On §10's probe the right page was among the first five results for 77% of French and 86% of English queries, and among the first ten for 86% and 93%. The ceiling is a round figure under the 64 full passages the byte ceiling would hold at the passage size, leaving room for the lines naming their sources |
| Retrieved bytes (FR-227) | 16 KiB unless the playbook says otherwise; at most 64 KiB | the playbook, refused at load above the ceiling | The default carries the default count of full passages with the lines naming their sources; the ceiling carries the largest count. A retrieval supplements what gather hands the agent, and a need for more than 64 KiB from one query is better met by two queries |
| Each request (FR-208) | 60 seconds unless the collection says otherwise | the deployment, per collection | The slowest 16-input batch took 20.9 seconds and a cold model load 5.1, on a CPU-only server; 60 seconds is a little over twice their sum. How fast an endpoint answers is a fact of the deployment, which a portable playbook cannot know |
| Inputs per request | 16 | the runtime | Batching saved 11 to 14% per input on this server (0.81 to 0.70 seconds per input on model A), so its purpose is fewer requests against a hosted API. Sixteen keeps a request inside the default bound on the slowest server measured |
| Each retrieval (FR-209) | 2 minutes unless the collection says otherwise | the deployment, per collection | The gather step's default timeout, for the gather step's reason: the stage runs while the playbook holds its single-flight claim. At the slowest rate measured, 1.3 seconds per 1 KiB passage, two minutes embed about 90 changed passages, far more than one run's report adds; a lexical update of 2,989 took a tenth of a second. A larger change is the operator's rebuild (FR-220), whose every request FR-208 still bounds |
| Document size | 1 MiB; a larger file is skipped with that reason | the runtime | §11 |

Retrievals run in the order the playbook declares them, each bounded by its collection's bound, so
the stage's bound is their sum and is known when the playbook loads. The bound covers everything the
retrieval does: walking and digesting the sources, embedding what changed, and searching.

## 10. One tokenizer, and which

**Question**: §1 showed the Porter stemmer matching `disk` against `Disks`. It applies English
suffix rules. Does it help or harm a collection in another language, and should the tokenizer be a
choice each collection makes?

**Method**: the known-item probe over the shared corpus — each page's description as the query, its
remaining text cut into passages by §8's rules — indexed under five tokenizer configurations in
FTS5. The query was quoted and joined as §2 requires, results ordered by `bm25()` then by page and
position, and a page counted as found at the rank of its first passage. Reciprocal ranks were then
paired query by query between each configuration and `unicode61`. Separately, French words were
inserted one at a time under `unicode61` and `porter unicode61` and the stored term read back
through `fts5vocab`.

**Answer**: mean reciprocal rank, and the share of queries whose page was first, in the first five,
and in the first ten.

| Tokenizer | French MRR | @1 | @5 | @10 | English MRR | @1 | @5 | @10 |
| --------- | ---------- | -- | -- | --- | ----------- | -- | -- | --- |
| `unicode61` | 0.599 | 0.472 | 0.773 | 0.863 | 0.691 | 0.562 | 0.858 | 0.931 |
| `porter unicode61` | 0.618 | 0.498 | 0.773 | 0.858 | 0.692 | 0.562 | 0.867 | 0.923 |
| `trigram` | 0.607 | 0.485 | 0.751 | 0.850 | 0.708 | 0.588 | 0.858 | 0.918 |

`remove_diacritics 2` scored exactly as the default did, with and without Porter, in both
languages. Paired against `unicode61`, Porter's mean difference in French was +0.019 with a standard
error of 0.015: better on 43 queries, worse on 51, the same on 139. In English it was +0.001 with a
standard error of 0.017. Trigram's was +0.009 and +0.017, each with a standard error of about the
same size. The sign also depends on how the text is cut: with a splitter that also opened a passage
at a manual page's capitalised section names, Porter came out 0.006 below `unicode61` in French and
0.021 above it in English, and on whole pages 0.018 and 0.039 below. The trigram index took six to
seven times as long to build.

What Porter did to French words: `paquets`, `utilisateurs` and `fichiers` lost their plural, which
helps; `port`, `porte` and `portes` all became `port`, conflating a harbour with a door; `installé`
became `instal` while `installée` became `installe`, splitting one verb in two; `supprimez` stayed
apart from `supprimer` and `supprimé`. `unicode61` alone folded case and accents — `installé` to
`installe`, `répertoires` to `repertoires` — and applied no language's rules.

**Decision**: `unicode61` with its default diacritic folding, for every collection, and no
per-collection choice. No configuration differed from it by much more than one standard error in
either language, and the direction flipped with the passage rule, so a per-collection setting would
be one whose effect this measurement cannot tell from noise. It would still cost a catalogue key, a
field in the configuration identity, a column in the listing and tests of its own. `unicode61`
conflates only words spelled with the same letters; Porter applies English rules to every language
it is handed. The tokenizer stays part of FR-217's configuration identity, so changing it later
rebuilds the index rather than mixing two tokenizations in one.

## 11. How a directory's changes are detected

**Question**: FR-217 has every retrieval bring the index up to date. Is a content digest per file
affordable on every retrieval, or does a size-and-modification-time check have to stand in for it —
and what would that check miss?

**Method**: a Go program, on ext4 and the processor of §6. It walked a directory taking `lstat` of
each regular file, then again reading each and taking its SHA-256, both with the tree in the page
cache and after evicting that tree's own pages from it with `posix_fadvise(POSIX_FADV_DONTNEED)`,
file by file. Two trees: `/usr/share/man/fr`, 226 regular files and 0.73 MB, the size of a
directory of runbooks, and `/usr/share/doc`, 4,227 files and 67 MB. It then rewrote a file with
content of the same size straight after taking its `lstat`, 10,000 times, on ext4 and on tmpfs,
counting the rewrites the stat did not show. Last, it made a same-size edit and put the file's
modification time back, as `touch -r` and `cp -p` do.

**Answer**:

| Tree | `lstat` only | Digest, cached | Digest, evicted |
| ---- | ------------ | -------------- | --------------- |
| `/usr/share/man/fr` | 0.9 ms | 10 to 12.5 ms | 63 to 65 ms |
| `/usr/share/doc` | 22 to 64 ms | 0.40 s | 1.56 s |

Size and modification time were unchanged after 9,442 of the 10,000 rewrites on ext4 and 9,960 on
tmpfs, and adding the change time and the inode number caught none of them: this kernel, Linux 6.12 built with
`CONFIG_HZ=250`, stamps files from a clock that advances in 4 millisecond ticks, not at every write.
The restored modification time was caught by the change time, which moved, while size, modification
time and inode did not.

**Decision**: a SHA-256 digest of every file, on every retrieval, with no stat check in front of it.
A stat tuple misses most rewrites that land within one tick of the previous look, and nothing in the
tuple catches them. Making it safe needs git's rule for a "racily clean" entry — a file stamped no
earlier than the scan is digested anyway — a second path whose correctness rests on each
filesystem's timestamp granularity. What it would save is 60 milliseconds on a directory of
runbooks, and 1.5 seconds at 67 MB, where a single embedding request costs more than either (§7).
The digests are also what the generation's identity is built from, so they are computed either way.

Reading every file on every retrieval makes the read a bound to state: a file over 1 MiB is skipped
with that reason (FR-213), the size to which a gather step's output and a playbook's prompt are
already held. The largest file in the three sets of §8 is 99 KB. The walk counts toward FR-209's
bound.

## 12. What a killed update leaves

**Question**: plan.md and data-model.md left open whether one SQLite write transaction per update
is enough for FR-219, or whether a generation has to be a file built beside the index and renamed
into place, and gave the decision to SC-211's kill rather than to argument.

**Method**: `TestAKilledRebuildLeavesThePreviousGeneration` in
`runtime/internal/index/generation_test.go`, run on 2026-09-13. A generation over two documents is
committed; the sources change; a re-execution of the test binary opens the index, starts a rebuild,
and blocks at a seam inside its write transaction — after the old passages are deleted and the new
ones inserted, before the generation row is written — where it is sent SIGKILL. The index uses
SQLite's default rollback journal, with writes taking the lock when they begin.

**Answer**: the next open of the index named the previous generation, and a search returned that
generation's passages and none of the killed rebuild's.

**Decision**: one write transaction per update, and no file swapped by rename.

## Open

1. **One embeddings server has been observed.** §7 is Ollama. OpenAI's own service, and any server
   that checks the credential, have not answered anything, so the shape of a refused credential is
   unknown. A deployment pointing at a hosted API is where a disagreement with the stub would first
   show, and the stub is not to be read as covering it.
2. **The passage rules and the tokenizer were judged on manual pages and three synthetic reports**
   (§8, §10), not on a deployment's runbooks and report history. They are to be revisited against
   the first real collection, not argued about before one exists.
3. **Truncation past the server's context cannot be seen in a batch response** (§7). The 1 KiB
   passage makes it unlikely for text like the three sets, at about 300 tokens where the smallest
   context is 512. It does not make it impossible: text denser in tokens than anything measured —
   long identifiers, encoded data — can still overrun a small context and is then ranked on its
   beginning alone. The lexical mode is unaffected.
