# Phase 0 — Research

Six questions answered by running throwaway programs on 2026-09-10, with Go 1.27.1 on linux/amd64.
None of the programs is committed; each is described closely enough to be run again. Two more
questions are open and listed at the end, because answering them needs something this session did
not have.

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
the first open question.

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

## Open

1. **What pins the stub to a real embeddings API?** Issue #16 records that every defect the runtime
   core's walkthrough found was the stub disagreeing with the real executable. The stub here is the
   same kind of risk. This session had no credential for any embeddings service and did not call
   one, so the wire protocol is not chosen and no response shape is known from observation. The
   candidate answer is a single exchange with a real service, captured once, holding nothing but
   vectors and a model name, and kept as the stub's fixture — which still has to be checked for
   anything identifying the account before it is committed.
2. **Where passages begin and end, and what text of a report is indexed.** Neither affects whether
   the design holds together, and both affect result quality. Both want a real collection and a real
   report history to judge against, not a fixture.
