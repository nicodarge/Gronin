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

**Followed up by running it**: does abandoning the wait abandon the write? A second program held a
write lock on the store from one connection while another inserted a delivery, and the handler's
wait was bounded at 300 ms; the lock was released 700 ms later. With the write running on the
request's own context, the handler gave up at 300 ms, the write returned `context deadline exceeded`,
and the row was absent. With the write on a context detached from the request, the handler gave up at
300 ms, the write returned no error once the lock was released, and the row was present. So the late
landing FR-314 is written for is what a detached write does routinely, and what a request-bound one
can still do when its commit completes as the bound fires — which no probe can produce on demand, and
which is why FR-314 does not depend on which of the two the implementation picks.

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
- FR-315's case, in a second run of the program: 300 identities recorded as dropped inside the
  window, then raced by two processes through the same statement extended to treat a dropped row as
  new, then tried a third time.

**Answer**: `journal_mode` is `wal` and `synchronous` is `2`, which is FULL. All twenty killed rows
were present on reopen. The two processes were told "new" 287 and 13 times — 300 in all, so the two
did contend and no identity was new twice. The window answered new, repeat, new: an acceptance
exactly one window old is a new delivery. The dropped identities were answered new 297 and 3 times —
300 in all, none twice — and the third try, inside the window, was a repeat.

**Consequence for the design**: the existing store is enough for FR-312, FR-315, FR-317 and FR-318
without a new dependency or a change to how it is opened, and the "new" decision is a statement's
result rather than a read followed by a write. What this did **not** establish is behaviour on power loss: a kill
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

## 7. Which senders can produce the signature, and in which header (read, not run)

**Question**: FR-307 fixes the scheme — HMAC-SHA256 over the exact body bytes, in one header. This
repository's standard is that a claim about third-party software is read off that software, not
recalled. Which real senders can present a signature that fits, and in which header?

**Method**: for each sender, read its own source (where it is open and the sending code is
identifiable) or its own current documentation, fetched on 2026-09-11. Every citation below names
the file and line, or the page, read that day.

**Answer**:

| Sender | Signs? | Header | Format | Bytes signed | Configurable | Source |
| ------ | ------ | ------ | ------ | ------------- | ------------- | ------ |
| GitHub | yes | `X-Hub-Signature-256` | `sha256=<hex>` | exact body | secret per webhook | [Validating webhook deliveries](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries), fetched 2026-09-11: "The hash signature will appear in each delivery as the value of the `X-Hub-Signature-256` header", computed as "an HMAC hex digest" of "your webhook's secret token and the payload contents" |
| Gitea | yes | `X-Gitea-Signature` (also sent as `X-Gogs-Signature` and `X-Hub-Signature-256`) | hex, no prefix on the Gitea/Gogs headers; `sha256=<hex>` on the GitHub-compatible one | exact body | secret per webhook | `services/webhook/deliver.go`, function `addDefaultHeaders`, lines 97–146, commit `4d434455322a4780064ce67524ceb3711d865320` of `go-gitea/gitea`: `sig256 := hmac.New(sha256.New, secret)`; `io.MultiWriter(sig1, sig256).Write(payloadContent)`; `req.Header.Add("X-Gitea-Signature", signatureSHA256)` |
| Forgejo | yes | `X-Forgejo-Signature` (also `X-Gitea-Signature`, `X-Gogs-Signature`, `X-Hub-Signature-256`) | hex, no prefix on the Forgejo/Gitea/Gogs headers | exact body | secret per webhook | `services/webhook/shared/payloader.go`, function `AddDefaultHeaders`, branch `forgejo` of `codeberg.org/forgejo/forgejo`, fetched 2026-09-11 — a near-verbatim fork of Gitea's `addDefaultHeaders`, with `X-Forgejo-Signature` added alongside the inherited headers; only the raw body is signed |
| GitLab, secret token (legacy) | no — a static shared value | `X-Gitlab-Token` | plain text, not a signature | nothing — the header itself is the secret, compared directly | secret per webhook | [Webhooks](https://docs.gitlab.com/user/project/integrations/webhooks/), fetched 2026-09-11: "Secret token for the webhook, sent as plain text"; "The secret token only provides a plain-text value in a header, which offers weaker guarantees" |
| GitLab, signing token | yes, but not to FR-307's scheme | `webhook-signature`, alongside `webhook-id` and `webhook-timestamp` | `v1,<base64>` | `{webhook-id}.{webhook-timestamp}.{body}`, not the body alone | secret per webhook | same page: "The signature is computed over the string `{message_id}.{timestamp}.{body}}`"; "Each signature has the format `v1,{base64_signature}`" |
| Prometheus Alertmanager | no | — | — | — | — | `notify/webhook/webhook.go`, `Notify`, commit `4b400e67d6dafee92409ba8e06a28dfe80ca9046` of `prometheus/alertmanager`: the message is JSON-encoded into `buf` and posted with `notify.PostJSON(ctx, n.client, url, &buf)` — no header is added beyond what `http_config` sets, and `WebhookConfig`'s documented fields (`url`/`url_file`, `http_config`, `max_alerts`, `timeout`, `payload`) carry no signing option |
| Grafana alerting | yes, and can match FR-307 exactly | configurable, default `X-Grafana-Alerting-Signature`; timestamp, if used, in a second configurable header | hex | exact body when no timestamp header is configured; `timestamp + ":" + body` when one is | secret, header name and the optional timestamp header are all configuration fields (`HMACConfig.Secret`, `.Header`, `.TimestampHeader`) | `http/hmac.go`, function `sign`, commit `f7a71a734a8e9d7b3145ac74c5a926b9ebd46af5` of `grafana/alerting`: `hash := hmac.New(sha256.New, []byte(rt.secret))`; when `rt.timestampHeader != ""` the timestamp and a `:` separator are written first; `hash.Write(body)` always runs; `signature := hex.EncodeToString(hash.Sum(nil))` |
| Stripe (generic, for contrast) | yes, but not to FR-307's scheme | `Stripe-Signature` | `t=<ts>,v1=<hex>,v0=<hex>` | `{timestamp}.{body}` | secret per endpoint | [Webhook signatures](https://docs.stripe.com/webhooks/signature), fetched 2026-09-11: the signature parameter looks like `t=xxx,v1=yyy,v0=zzz`; Stripe's own troubleshooting guide for this page states the signed payload is the timestamp concatenated with the raw body |

**Consequence for the design**: three findings, against the owner's decision that a per-source
header name is admissible and a second signing scheme is not.

- **A fixed header name does not fit what real senders do.** GitHub, Gitea/Forgejo and Grafana each
  default to a different header, and Grafana's is a configuration field with no fixed default at
  all. FR-306 therefore carries a fourth declared property per source — the header — alongside the
  name, the secret, the identity location and the replay window, and the plan's Phase 1 sketch names
  the header as that per-source property rather than as one fixed header. GitHub's
  and Gitea's formats also differ in whether the value carries a `sha256=` prefix; that is a format
  the runtime already has to parse per source once the header itself is per source, so it costs the
  design nothing further to declare.
- **Gitea and Forgejo need nothing changed to fit exactly**: HMAC-SHA256, hex, over the exact body,
  in a header of their own. Grafana fits exactly provided the operator leaves its timestamp header
  unconfigured — which is the operator's choice, not the runtime's to enforce, so it is worth saying
  in the deployment-facing documentation this feature eventually needs.
- **Three senders are flagged rather than accommodated, as the owner's decision requires.**
  Prometheus Alertmanager signs nothing at all; GitLab's recommended mechanism and Stripe's both
  sign a composite string, not the body alone, in a format this ingress does not parse. Each needs
  something in front of the ingress that verifies what that sender actually sends and re-signs
  the exact body under a secret this deployment holds — the assumption the specification already
  states ("A sender that cannot sign cannot deliver directly"). Widening FR-307 to a second scheme
  to accommodate any of them is explicitly not the answer the owner gave.

## 8. Repeat detection and the guard's coordination backend

**Question**: the guard's Phase 0 (PR #26, branch `add_guard_research`) has since chosen a
backend. Does FR-319's single-host reach for repeat detection widen through it?

**Method**: read `specs/002-guard/research.md` and `specs/002-guard/contracts/coordination.md` on
`origin/add_guard_research`, at the commit that branch carries the research and contract from.

**Answer**: no, not automatically, and the interface the guard settled on gives no path to "yes"
without a further decision. The guard's `Coordinator` interface (`contracts/coordination.md`) has
exactly four operations — `Acquire`, `Released`, `Reach`, and the methods on the `Claim` it
returns (`Renew`, `Fence`, `Release`) — and every one of them is about a claim on a **playbook
name**, keyed by `AcquireRequest.Name`, with an optional rate limit keyed the same way. Nothing in
it carries a delivery's identity, its source, or its replay window; `TriggerRef` carries only a
trigger's kind and, for a schedule, the instant it was due. FR-316 through FR-319's decision —
"is this identity new" — is answered by the webhook's own record store, established in question 5
above as one atomic SQL statement against the SQLite database opened per host, which is a
different mechanism from the guard's claim on a name entirely. Choosing etcd as the guard's
backend widens the *guard's* reach (`Reach()` returns `"cross-host"` for the etcd adapter and
`"single-host"` for the file lock, per the interface's own doc comment) and says nothing about the
webhook's dedup, because the webhook's dedup does not go through this interface at all.

**Consequence for the design**: FR-319 is unaffected by the guard's choice, and nothing here
commits the webhook to etcd. Stated as an option, not adopted: since the chosen backend is etcd,
which the guard's research already measured providing a server-judged expiry and an atomic
compare-and-set primitive (the fencing token, from a key's creation revision), a later change could
record an accepted delivery's identity as an etcd key with a lease equal to the replay window,
instead of — or alongside — the SQLite row, and have `Acquire`-shaped logic answer "new" or
"repeat" the same way the guard's claim does. That would need its own Phase 0: this document's
question 5 established FR-312's kill-survival property for the SQLite path specifically ("the
process being killed the moment the answer leaves"), and nothing here has measured whether an
etcd lease commit gives the same guarantee, at the same or a different bound, under the same test.
It would also turn a backend the webhook does not need today into one every deployment wanting
webhooks must run, which is a cost FR-319's own wording — stating the narrower reach rather than
implying a wider one — was written to avoid taking on silently. The narrower reach stands.

## 9. Power loss: what SQLite's own documentation establishes (read, not run)

**Question**: question 5 measured a killed process, not a lost machine, against a store opened
with `synchronous=FULL` in `wal` mode — the setting SQLite documents as meant to cover the second.
What does SQLite's own documentation establish about that, and what remains this repository's
assumption rather than something shown?

**Method**: read `https://www.sqlite.org/pragma.html#pragma_synchronous` and
`https://www.sqlite.org/atomiccommit.html`, fetched 2026-09-11.

**Answer**: `pragma.html` states that with `synchronous` FULL "the SQLite database engine will use
the `xSync` method of the VFS to ensure that all content is safely written to the disk surface
prior to continuing" and that "this ensures that an operating system crash or power failure will
not corrupt the database." For WAL mode specifically, it says FULL adds an extra fsync of the WAL
file after every transaction commit, beyond what NORMAL does before each checkpoint, and that this
"extra WAL sync following each transaction helps ensure that transactions are durable across a
power loss" — the sentence FULL exists for, and the reason NORMAL is not this store's setting.

`atomiccommit.html` states the guarantee's own precondition: SQLite's use of fsync "assumes that
the flush or fsync will not return until all pending write operations for the file that is being
flushed have completed," and then qualifies it: "we have received reports that neither of these
interfaces works as advertised on many systems… often the IDE disk control lies and says that data
has reached oxide while it is still held only in the volatile disk cache." Its own conclusion:
"SQLite assumes that the operating system that it is running on works as advertised. If that is
not quite the case, well then hopefully you will not lose power too often."

**Consequence for the design**: established, by reading rather than by running anything, is that
`synchronous=FULL` in WAL mode is the setting SQLite's own documentation names as the one meant to
survive an OS crash or power failure, and that the record store is opened with it (question 5).
Not established, and not establishable from a repository's development machine: whether the disks
and filesystems a deployment actually runs on honestly complete the fsync `synchronous=FULL`
issues — SQLite's own documentation names this as the precise way the guarantee can fail, on
hardware that lies about a flush having reached the physical medium. Question 5's kill test is
evidence for a related but narrower claim — a process killed the instant its answer left survives,
which is what FR-312 requires — and is not evidence for a lost machine, which FR-312 does not
claim to cover and this question does not close. The gap stays named as an assumption in the plan
rather than presented as measured.
