# Phase 0 — Research

Six questions, answered on 2026-09-10 on Go 1.27.1 and on the SQLite that `modernc.org/sqlite` at
the runtime's pinned version embeds (it reported 3.53.4). Four were answered by running a throwaway
program outside the repository, described here so it can be run again; two by reading source, and
each of those says so. The questions neither could answer are in the plan's Phase 0, still open.

## 1. A loopback listener is available inside the suite's isolation

**Question**: the suite must pass with no network reachable (Principle VI). A listener test needs a
socket. Does the namespace `scripts/run-suite.sh` builds leave one, and does anything else get out?

**Method**: ran commands under `scripts/no-network.sh`, the same wrapper the suite and the mutation
harness use: listed interfaces from `/proc/net/dev`, opened a TCP connection to `192.0.2.1:80`,
resolved `example.com`, and ran the existing API tests that bind `127.0.0.1` through `httptest`.

**Answer**: the only interface is `lo`. The connection to `192.0.2.1` failed, `example.com` did not
resolve, and `TestTheAPIReadsTheRecordBack` and `TestATokenIsRequiredWhenOneIsConfigured` passed.

**Consequence for the design**: a test that binds loopback is hermetic in this repository's sense —
it passes on a machine with no network, because the wrapper verifies that is the machine it is on.
That draws the line the plan uses: a property of a request (signature, route, identity, payload
shape, what is recorded) is tested against the handler with no socket at all; a property of a
connection (a stalled header, a stalled body, the number in progress) needs a real one, and gets
loopback; and the binary-level test binds loopback because the executable is what is under test.

## 2. What the standard HTTP server does with each misbehaving sender

**Question**: FR-329, FR-330 and FR-331 need bounds, and a bound is only worth specifying if the
thing enforcing it behaves as assumed. What does `net/http` actually do, and what does the sender see?

**Method**: one server per case on `127.0.0.1:0`, under `scripts/no-network.sh`, with
`ReadHeaderTimeout` 500 ms, `ReadTimeout` 1 s and `WriteTimeout` 1 s, and a handler that refuses a
declared `Content-Length` over 1024 bytes before reading and otherwise reads through
`http.MaxBytesReader` at 1024. The sender was a raw TCP connection, so what it received is what a
real sender would receive, with nothing in between to tidy it.

**Answer**:

| Case | The sender received | The handler saw |
| ---- | ------------------- | --------------- |
| Declared length 4096, body sent | `413` at once | refused on the declared length, nothing read |
| Declared length 10 MiB, body never sent | `413` at once | refused on the declared length, nothing read |
| Chunked body of 3000 bytes, no length declared | `413` | `*http.MaxBytesError` after exactly 1024 bytes |
| Headers never finished | no status line; connection closed | never called |
| Body stalls after 10 of 100 bytes | no status line; connection closed after 1 s | a read timeout after 10 bytes; its `408` never arrived |
| Handler holds 2 s past a 1 s write timeout, then writes `202` | no status line; connection closed | it called `WriteHeader(202)`, which returns nothing to check |
| Handler bounds its own held step at 300 ms | `503` after 300 ms | its own bound expiring |
| 2 MiB of headers | `431` | never called |

**Consequence for the design**: the size bounds can be enforced before a body costs anything, by
the declared length, and by the byte cap when there is none (FR-329). A stalled sender is ended by
the server's own timeouts and gets no answer at all — which a sender treats as a failure and retries,
the right outcome, and which is why FR-330 says the connection is closed rather than that a status is
returned. The default header limit, `http.DefaultMaxHeaderBytes`, is 1 MiB in the standard library's
source; an ingress whose only header of interest is a signature has no use for that much.

## 3. A write timeout makes an answer disappear, and the handler cannot tell

**Question**: FR-312 has the ingress answer only after a durable write. What happens when the write
is slow?

**Method**: the sixth and seventh rows of the table above.

**Answer**: when the handler outlives the server's write timeout, it writes its acceptance through a
call that has no error to return, and the sender receives nothing. Read afterwards in `net/http`'s source: the
write deadline is set when a request's headers have been read, so the write limit covers reading the
body and everything the handler does, not only the write itself. When the handler bounds
its own wait below that timeout and answers `503`, the sender receives the `503`.

**Consequence for the design**: two requirements came out of this. FR-313 puts the durable step's bound below the listener's write limit and makes exceeding it
an explicit refusal — because otherwise the one outcome the sender cannot distinguish from a lost
packet is the one the handler records as a success. And FR-314 makes the hand-off follow the record
rather than the answer. That half is reasoning, not something this probe measured: a handler that
stops waiting at its bound has not rolled the write back, which may already have committed when the
bound expired, and if the hand-off were tied to answering `202` that delivery would be recorded,
deduplicated on the sender's retry, and never run.

## 4. The standard library's MAC comparison is constant time over equal lengths (read, not run)

**Question**: FR-308 requires a constant-time comparison. Is `hmac.Equal` one, and is there a catch?

**Method**: read `crypto/hmac/hmac.go` in the installed Go toolchain.

**Answer**: `hmac.Equal` returns `subtle.ConstantTimeCompare(mac1, mac2) == 1`. Its own comment says
it does not hide a difference in length.

**Consequence for the design**: the length is not a secret — an HMAC-SHA256 is always 32 bytes — so
the offered signature is decoded and refused when it is not 32 bytes before the comparison runs,
which is FR-308's "wrong length" clause. SC-313 asserts the verifying code calls this function rather
than measuring time, for the reason given there.

## 5. The record store survives a kill after commit, and can decide "new" in one statement

**Question**: FR-312 needs a write that survives the process being killed the moment it answers;
FR-318 needs "is this identity new" and "record it" to be one step across processes; FR-317 needs a
window judged on the runtime's clock. Can the existing store do all three as it is opened today?

**Method**: a program opening a database with the record store's own connection string, copied from
`runtime/internal/record/store.go`, and a table keyed on source and identity.

- Read `PRAGMA journal_mode` and `PRAGMA synchronous` on that connection.
- Twenty times: a child process inserted a row, printed that it had committed, and was sent SIGKILL
  as soon as that was read. Then the database was reopened and the rows counted.
- Two processes each ran the same 300 identities through one statement — an insert that, on a
  conflicting key, updates only when the existing acceptance is older than the window, and returns a
  row only when it inserted or updated — and each counted the identities it was told were new.
- The same statement for one identity at a time of 1000 s, then 1599 s, then 1600 s, with a 600 s
  window.

**Answer**: `journal_mode` is `wal` and `synchronous` is `2`, which is FULL. All twenty killed rows
were present on reopen. The two processes were told "new" 287 and 13 times — 300 in all, so the two
did contend and no identity was new twice. The window answered new, repeat, new: an acceptance
exactly one window old is a new delivery.

**Consequence for the design**: the existing store is enough for FR-312, FR-317 and FR-318 without a
new dependency or a change to how it is opened, and the "new" decision is a statement's result rather
than a read followed by a write. What this did **not** establish is behaviour on power loss: a kill
of the process was measured, a loss of the machine was not, and FULL synchronous is the setting that
is supposed to cover the second. SC-306 still forces the interleaving rather than relying on this
race, because a race that the right answer happened to win is not a test of the wrong one.

## 6. Replay and resume already pass no trigger values to the sinks (read, not run)

**Question**: FR-336 requires a webhook-triggered run to replay. What does a replay, or a resume,
hand the stages that interpolate?

**Method**: read `runtime/internal/run/replay.go`.

**Answer**: a replay restores the recorded gathered inputs into the new working directory and reuses
the recorded, already-resolved prompt, so the prompt needs nothing from the trigger. Both a replay and
a resume then build the sinks with no trigger values at all, so a sink field referencing the payload
has nothing to resolve against.

**Consequence for the design**: FR-325 is also what makes a webhook run replayable and resumable. A
sink field allowed to reference the payload would refuse every replay and every resume of that run
for a missing value. FR-324 is not needed for replay — the resolved prompt is recorded — and is
justified on its own terms in the plan; what replay does rely on is that the declared values reach
the agent as a gathered input, which a replay already restores.
