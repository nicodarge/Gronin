# Phase 1 — Data Model

Three places hold this feature's state, and the division is the design.

- **The deployment's catalogue** says what a collection is: its source, its mode, and for the
  semantic mode the endpoint, the model and a reference to the credential. A playbook names an
  entry and nothing else (FR-203).
- **The index**, one SQLite database per collection under the state directory, is derived data
  (FR-216). Deleting it loses nothing a rebuild does not restore, which is why nothing that has to
  survive — what a run saw — is ever kept only there.
- **The record store** holds what a run retrieved, in full (FR-225). A record points at nothing in
  the index, because the index moves under a run's feet and a pointer into it answers what the
  index says now, not what the run saw.

## Entities

### Retrieval declaration (in the playbook)

The `retrieve` block, a list, validated at load (FR-204); its schema is
[contracts/retrieve.schema.json](./contracts/retrieve.schema.json). Each entry is one retrieval,
run in the order declared (research.md §9).

| Field | Type | Notes |
| ----- | ---- | ----- |
| `collection` | name | Must be a collection this deployment's catalogue declares (FR-204) |
| `query` | string | Interpolated against `${trigger.…}` and `${config.…}` as every other playbook string is. Exactly one of `query` and `query_from` |
| `query_from` | name | The `as` of a gather step, whose output is the query. Exactly one of `query` and `query_from` |
| `as` | name | The file the results are written to in the working directory. Collides with no gather step's `as` and no other retrieval's (FR-204) |
| `max_results` | integer, 1 to 50 | Default 10 (FR-227, research.md §9) |
| `max_bytes` | integer, 1 to 65,536 | Default 16,384. Counts every byte of the results file, the lines naming sources included (FR-227) |

No mode, endpoint, model, credential or provider appears here, and a key naming one is refused as
an unknown key (FR-203). They belong to the deployment, so a playbook retrieving from `runbooks`
runs against a deployment whose `runbooks` is lexical and one whose `runbooks` is semantic alike.

**Why `query_from` rather than a new interpolation namespace.** FR-202 lets a query be formed from
gathered input. Principle IV names the only two sources interpolation may resolve against — the
trigger payload and the deployment configuration — so a `${gathered.…}` reference would widen
what the constitution bounds. A gathered input is a file the run already wrote, and reading it is
what the agent does with it too. `query_from` reads that file whole, as the query, and adds no
source to interpolation.

### Collection (in the deployment)

`collections.json` in the state directory, beside `config.json` and `mcp_servers.json`, read the
way the MCP catalogue is: absent means none, and a malformed one refuses every command that loads
playbooks. Its keys are in [contracts/cli.md](./contracts/cli.md).

| Field | Type | Notes |
| ----- | ---- | ----- |
| name | the entry's key | Same shape as a playbook name; it names the index file |
| `directory` | absolute path | A source (FR-212). Exactly one of `directory` and `reports` |
| `reports` | list of playbook names | A source: the reports this runtime recorded for those playbooks (FR-212, FR-214) |
| `embeddings.url` | URL | The full URL of an OpenAI-compatible embeddings resource (research.md §7). Its presence is what makes the collection semantic (FR-206) |
| `embeddings.model` | string | Sent as `model` |
| `embeddings.credential` | reference, optional | A `${config.…}` reference to a value marked secret, never a literal (FR-224). Optional because a server on the same host may ask for none |
| `embeddings.request_timeout` | duration | FR-208. Default 60 s (research.md §9) |
| `retrieval_timeout` | duration | FR-209, for either mode. Default 2 min (research.md §9) |

**Mode** is derived, never declared: `semantic` when `embeddings` is present, `lexical` otherwise.
A key that could say `mode: lexical` beside an `embeddings` block would be a second statement of
one fact, and the two could disagree.

**Configuration identity** is a SHA-256 over a canonical encoding of what decides how passages are
cut, tokenized and embedded: the passage rule's version, the tokenizer (`unicode61`), the source's
kind and path or playbook list, and, for the semantic mode, the URL and the model. It excludes the
credential and both timeouts: rotating a credential or allowing a slower endpoint changes nothing
the index holds. An index whose stored identity differs from the collection's is treated as empty
(FR-217).

### Source document

What a walk of the source produced, before anything is indexed. Listing a collection (FR-215) is
this walk compared with the generation the index holds, which is how an operator sees what an
embeddings API would receive before anything is sent (FR-221, US3 scenario 5).

| Field | Type | Notes |
| ----- | ---- | ----- |
| `source` | string | A path relative to the collection's directory, or a run identifier for a report |
| `digest` | SHA-256 | Of the file's bytes, or of the report as recorded. Taken on every walk, with no size-and-time check in front (research.md §11) |
| `bytes` | integer | |

A directory walk never follows a symbolic link (FR-213), and records it as skipped instead. A file
is skipped, with its reason, when it is a symbolic link, when it is larger than 1 MiB, or when it is
not text: it holds a NUL byte, or it is not valid UTF-8. An entry that is none of a directory, a
regular file and a symbolic link — a named pipe, a socket, a device — is skipped as not a regular
file: a pipe with no writer reads as an empty document, and one with a writer holds the walk.
Nothing else is skipped. The collection's own directory is the one path resolved before the walk,
every component of it links included: it is the path the deployment declared, and a mount point is
often a link. Nothing below it is followed. A file that cannot
be read, and a directory that does not exist, refuse the retrieval and name the path (FR-228)
rather than reading as an empty collection.

A report is a source document when its run recorded a report and derives from no other run. The
runtime core sets a run's report only once the report has validated against its playbook's schema,
and a replay and a resume are the only runs that derive from another (FR-214). The rule is stated
on those two facts rather than on a list of trigger kinds, so a run begun by a trigger kind added
later is indexed without this feature changing.

### Passage

The unit indexed and retrieved (research.md §8).

| Field | Type | Notes |
| ----- | ---- | ----- |
| `source` | string | As above |
| `ordinal` | integer | Its position among its document's passages, from 1 |
| `offset` | integer | Its byte offset in the document, or in the report's joined values |
| `text` | string | At most 1,024 bytes |

Text is cut at blank lines, a Markdown ATX heading opens a new passage, blocks are packed in order
while they fit, and a block longer than the cap is cut at its last whitespace before it, or at a
character boundary when it has none. A report's text is the string and number values of its JSON
in document order, read from the decoder's token stream; each value is a block, each object that is
an element of an array opens a passage, and a report holding no values is no document. No passage
is empty.

FR-210's tie-break key is `(source, ordinal)`: equal scores are ordered by source, then by ordinal,
never by the order rows happen to have in the index.

### Index generation (in the index)

One complete state of a collection's index (FR-219).

| Field | Type | Notes |
| ----- | ---- | ----- |
| `generation` | SHA-256, shown shortened | Over the configuration identity and the sorted `(source, digest)` pairs it was built from. Two builds of the same sources under the same configuration carry the same generation, whichever path built them |
| `identity` | SHA-256 | The configuration identity it was built under |
| `built_at` | timestamp | The runtime's wall clock, UTC, when the generation was committed |
| documents | rows | `(source, digest, bytes)` |
| passages | rows | With an FTS5 table over `text`, tokenizer `unicode61` |
| vectors | rows, semantic only | One per passage: its length and the vector normalised to unit length, so a dot product is a cosine (research.md §6) |

```text
none ──first retrieval or rebuild──▶ G1 ──sources change, next retrieval──▶ G2 ──▶ …
                                      │
                                      ├─ update fails or is killed ──▶ G1, untouched
                                      └─ configuration identity changes ──▶ treated as empty ──▶ G′
```

**How an update is made whole.** An update walks and digests the sources, reads the generation
the index holds, and works out what was added, changed and removed. For the semantic mode it then
embeds the passages of what was added or changed — the only network step, and the one that takes
time — before it opens a write transaction. Inside the transaction it checks that the generation
is still the one it read; if another update committed meanwhile, it rolls back and works the
difference out again from the new one, inside the same retrieval bound. Otherwise it applies the
difference and the new generation's row and commits.

It reads the text of each document it adds as it indexes that document, one at a time inside the
transaction, and checks the text still has the digest the walk took: reading every added file first
would hold the whole collection's text until the commit on a rebuild or a first retrieval, with no
bound on its size. A file changed since the walk is never indexed under the earlier digest: the
update rolls back, walks the sources again and works the difference out afresh, inside the same
retrieval bound, and is refused naming the file only when the bound ends with the file still
changing. A rebuild, which has no bound, is refused naming the file once it has changed on each of
ten walks. A refusal names a changed file only while the file is still changing: once an attempt
has read every document it adds unchanged, a later refusal is for its own cause. Waiting for a
write lock another process holds is inside the same bound. A search open at COMMIT holds the
database as well, and under the rollback journal COMMIT cannot take it exclusively until the
search ends: the update waits for it there, within the same bound, and neither redoes its
transaction nor reads any document again for it. A search reads inside one transaction, so it
sees the generation before or the one after, never part of either. A seam between reading the
generation and opening the transaction is what lets a test hold one update there while another
commits (SC-211).

One SQLite write transaction per update survives a kill, and no generation is a file renamed into
place: SC-211's kill decided it, and [research.md](./research.md) §12 records what it showed. The
listing reads the index through a connection that can roll a hot journal back, which a read-only
one cannot.

### Retrieval (in the record store)

One search a run performed (FR-225). A child of the run, as its gathered inputs are.

| Field | Type | Notes |
| ----- | ---- | ----- |
| `run_id`, `sequence` | key | `sequence` is the retrieval's position in the playbook's `retrieve` list, from 1 |
| `as_name` | string | The results file's name in the working directory |
| `collection` | string | |
| `mode` | enum | `lexical`, `semantic` |
| `generation`, `generation_built_at`, `identity` | as above, nullable | Null only for a retrieval refused before it read a generation |
| `query` | text | As searched: resolved, and cut to 1,024 bytes at the last whitespace before the cap. Redacted at the write boundary |
| `query_truncated` | bool | |
| `outcome` | enum | `found`, `empty`, `refused` |
| `results_ref` | blob ref, nullable | The results file, byte for byte as written to the working directory. Null when refused |
| `results_bytes` | integer | |
| `count_truncated`, `bytes_truncated` | bool | More matched than `max_results`; the file was cut to `max_bytes` (FR-227) |
| `error` | text, nullable | For `refused`: the cause, naming the directory, the API, or the empty query (FR-207, FR-228) |

**Retrieved item** — one row per result, `(run_id, sequence, rank)` its key: its `score` as the
search produced it, its `source`, `ordinal` and `offset`, and its `content_ref`, the passage as the
agent received it. A score is recorded as the evidence of a rank, not as a measure: it is only
comparable within one retrieval (research.md §3).

The results file is passed through the redactor before it is written to the working directory, not
only on its way into the record. The blob store redacts at its write boundary; were the working
copy left unredacted, a document holding a configured secret would give the agent one text and the
record another, and SC-214's byte-for-byte reproduction would hold only for documents holding none.

```text
query resolved ─ empty ───────────────────────────────────────▶ refused (FR-228)
               ─ source unreadable, API unreachable or erring,
                 retrieval bound exceeded, vector length differs ▶ refused (FR-207, FR-209, FR-218, FR-228)
               ─ searched, nothing matched ──────────────────────▶ empty — the file says so, the agent runs
               ─ searched, matched ─────────────────────────────▶ found
```

A refused retrieval refuses the run, with the status `refused`, before the agent starts (FR-202).
Its row is still written, with no items and no results file, so the record says which retrieval
refused and why; no row with the outcome `found` or `empty`, and no item, exists for it under
either mode (SC-205).

**Replay** (FR-226) reads a run's retrievals, writes each results file back to the working
directory under its `as_name`, and records the same rows against the replay. It opens no index and
calls no API, so it succeeds after the index is deleted and the endpoint is gone (SC-215). A resume
runs no agent and retrieves nothing.

### Run (runtime core, changed)

The runtime core's Run entity
([specs/001-runtime-core/data-model.md](../001-runtime-core/data-model.md)) gains retrievals as a
child, beside gathered inputs, and its description of `refused` — a run that never reached the
agent — gains the retrieval's causes beside the receipt mismatch and the failed gather step it
names today (FR-202). No column of `runs` changes. That document is updated when this feature's
tasks land; it is recorded here so the change is not discovered in a migration.
