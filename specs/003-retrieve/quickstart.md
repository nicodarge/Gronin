# Phase 1 — Quickstart

How an operator checks, on a real host and against a real embeddings server, that retrieval does
what the suite says it does. The suite's semantic tests run against a stub on loopback; this is
where the other side of research.md §7's caveat — that the stub records one server on one date —
is looked at. It is a validation rather than a gate, and nothing in it has been run: it is written
before the code it describes.

## Prerequisites

- A host set up as in the runtime core's [quickstart](../001-runtime-core/quickstart.md) §1:
  `gronin` installed, the agent authenticated.
- A directory of a few Markdown runbooks, below at `/srv/runbooks`, one of them about a full disk.
- For §5 onwards, an OpenAI-compatible embeddings server, below at
  `https://embeddings.example.com/v1/embeddings`. A server on the same host is as good: where the
  endpoint runs is the deployment's choice.

`collections.json` in the state directory, as in [contracts/cli.md](./contracts/cli.md):

```json
{
  "runbooks": {"directory": "/srv/runbooks"},
  "disk-history": {"reports": ["disk-check"]}
}
```

A playbook, differing from `examples/doc-check.yaml` in its trigger and its `retrieve` block. It is
shown as a fragment: until this feature lands the published schema refuses the block.

```yaml
trigger:
  type: manual

retrieve:
  - collection: runbooks
    query: ${trigger.summary}
    as: runbooks.md
  - collection: disk-history
    query: ${trigger.summary}
    as: history.md
```

## 1. A collection is listed before anything indexes it (FR-215, FR-221)

```bash
gronin collections list
gronin collections show runbooks
```

**Expected**: `runbooks` is `lexical` and `not indexed yet`; `show` names every runbook, and any
file that is not text, is over 1 MiB, or is a symbolic link is listed as skipped with that reason.
No index file exists yet under the state directory.

## 2. A run reads what the runbooks say (SC-201, SC-214)

```bash
gronin run disk-check --trigger summary='disk full on /var'
gronin show <run>
gronin show <run> --retrieval runbooks.md
```

**Expected**: `show` names the collection, `lexical`, a generation, the query, and the full-disk
runbook among the results. `--retrieval` prints the file the agent read. The report refers to what
the runbook says.

## 3. An edited runbook is found as it now stands (SC-209)

Edit the full-disk runbook, add a new one, remove another. `gronin collections list` says
`changed since`. Run the playbook again.

**Expected**: the new generation reflects all three changes, and `gronin show` on the first run
still shows what it retrieved then and the generation it was.

## 4. What earlier runs concluded (SC-208)

Run the playbook a second time, then replay and resume the first run.

**Expected**: `history.md` in the second run names the first run as a result's source, and never
the replay or the resume.

## 5. Semantic search, and a refusal instead of a fallback (SC-204, SC-205)

Add the semantic collection of [contracts/cli.md](./contracts/cli.md), pointed at the server, and
set its credential from standard input with `--secret`. Before anything else:

```bash
gronin collections show by-meaning
```

That is the complete list of what the server will receive. Then:

```bash
gronin collections rebuild by-meaning
```

Change the playbook's first retrieval to `by-meaning`, and run it with a summary that shares no
word with the runbook it should find — `no space left on device` against a runbook titled
*Disk full*.

**Expected**: the record says `semantic`, and the runbook is among the results. Stop the server and
run again: the run is `refused`, the refusal names the URL, and no result was recorded under
either mode.

## 6. A model change re-embeds (SC-210)

Change `embeddings.model` to another model the server holds and run again.

**Expected**: the server receives every passage of the collection again, and the record names a new
generation built under the new model.

## Replay after the index is gone (SC-215)

Delete the index directory under the state directory, stop the embeddings server, and replay the
run of §5.

**Expected**: the replay succeeds, and its agent receives the results the original received.
