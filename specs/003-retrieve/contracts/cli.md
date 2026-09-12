# Contract — Operator surface, what retrieval changes

A delta on the runtime core's [cli.md](../../001-runtime-core/contracts/cli.md). Commands not named
here are unchanged.

## Commands

| Command | Change | Exit code |
| ------- | ------ | --------- |
| `gronin collections list` | New. One line per collection: its name, mode, source, the generation its index holds and when that was built, and whether the sources have changed since (FR-215) | Non-zero if `collections.json` is refused |
| `gronin collections show <collection>` | New. The same header, then every document the source holds and every file it skipped with the reason, each document marked against the generation (FR-215) | Non-zero if the collection is not declared, or its source cannot be read |
| `gronin collections rebuild <collection>` | New. Rebuilds the collection's index in full, outside any run (FR-220) | Non-zero, naming the cause, if the rebuild fails; the previous generation stays in place (FR-219) |
| `gronin show <run>` | Adds each retrieval: collection, mode, generation, the query as searched, the outcome, and each result's rank, score and source | |
| `gronin show <run> --retrieval <as>` | New flag. Writes the results file that retrieval handed the agent to standard output, byte for byte, from the record (FR-225) | Non-zero if the run has no retrieval of that name |
| `gronin validate`, `gronin serve`, `gronin run` | Read `collections.json`; refuse a playbook whose `retrieve` block names an undeclared collection (FR-204) | As today |

`list` and `show` walk and digest the sources to say what has changed, and write nothing: listing a
collection is not a request to index it, and for a semantic collection it sends nothing (FR-221).
That is what lets an operator read, before turning the option on, every document an embeddings API
would receive (US3 scenario 5). `rebuild` is one of the two things that may index a collection — a
retrieval is the other — and the one a collection too large to embed inside a run's bound is
brought into service with.

None of the three reads a run's record, so `list` and `show` work with no run history, and all
three work while `serve` is running: the index is a database two processes can open.

## Output

```text
$ gronin collections list
runbooks        lexical   directory /srv/runbooks   generation 3f9a1c0b2e4d built 2026-09-10T06:00:00Z  unchanged
disk-history    lexical   reports disk-space        generation 7b21e4c09d3a built 2026-09-10T06:05:12Z  changed since
by-meaning      semantic  directory /srv/runbooks   not indexed yet
```

```text
$ gronin collections show runbooks
collection  runbooks
mode        lexical
source      directory /srv/runbooks
generation  3f9a1c0b2e4d built 2026-09-10T06:00:00Z under identity 9c1d…
changed     yes — 1 added, 1 changed, 1 removed

document    disk-full.md          2048 bytes  unchanged
document    cert-expiry.md         911 bytes  changed
document    swap.md                402 bytes  added
removed     old-runbook.md
skipped     scan.pdf              not text
skipped     journal-dump.log      larger than 1 MiB
skipped     shared                symbolic link, not followed
```

For a reports collection each document is a run identifier. For a collection not indexed yet, the
`generation` line says so and every document is marked `not indexed`.

A retrieval in a run's record:

```text
$ gronin show 20260910T061214Z-a41c09e7b6f2
...
retrieved runbooks.md from runbooks (lexical) generation 3f9a1c0b2e4d built 2026-09-10T06:00:00Z: found 3
  query     disk full on /var
  result    1  -2.2713  disk-full.md#2
  result    2  -1.9320  disk-full.md#1
  result    3  -0.8841  swap.md#1
```

A refusal names its cause, and the run is `refused`:

```text
$ gronin run disk-check
20260910T061214Z-a41c09e7b6f2 refused
  retrieve[0] from by-meaning: the embeddings API at https://embeddings.example.com/v1/embeddings
  did not answer within 60s; nothing was searched
```

## The results file

What the agent reads, in the working directory under the retrieval's `as`. Markdown, because a
prompt is, and each result says where it came from so an operator can trace a repeated claim to
its origin (FR-214).

```text
# Retrieved from runbooks (lexical, generation 3f9a1c0b2e4d)
# Query: disk full on /var

## 1. disk-full.md, passage 2 (score -2.2713)

When /var fills, the journal is the usual cause. Check its size with …

## 2. run 20260910T060000Z-3f9a1c0b2e4d, passage 1 (score -1.9320)

…
```

Results are written in rank order. A result that does not fit what remains of `max_bytes` is cut at
the last character boundary that fits, the results after it are dropped, and the file ends with
`(cut to fit 16384 bytes)`; the record marks the retrieval `bytes_truncated`. When more passages
matched than `max_results`, the record marks it `count_truncated`. When nothing matched, the file
holds the two header lines and `Nothing in this collection matched the query.` — the agent is told,
rather than handed an empty file it could read as a failure.

## `collections.json`

In the state directory. Absent means a deployment with no collection, and a playbook naming one is
refused at load. Present, every key is checked when a command loads playbooks, and an unknown key
is refused like an unknown playbook key.

```json
{
  "runbooks": {
    "directory": "/srv/runbooks"
  },
  "disk-history": {
    "reports": ["disk-space"]
  },
  "by-meaning": {
    "directory": "/srv/runbooks",
    "embeddings": {
      "url": "https://embeddings.example.com/v1/embeddings",
      "model": "REPLACE_ME",
      "credential": "${config.embeddings_key}",
      "request_timeout": "60s"
    },
    "retrieval_timeout": "2m"
  }
}
```

| Key | Refused when |
| --- | ------------ |
| the entry's name | It is not a lowercase slug, the shape of a playbook name |
| `directory` | It is relative, or given with `reports` — exactly one source |
| `reports` | It is empty, or names something that is not a playbook name's shape |
| `embeddings.url` | It is not an `http` or `https` URL, or it carries user information — a credential goes in `credential`, where the redactor knows it |
| `embeddings.model` | It is empty |
| `embeddings.credential` | It is not a single `${config.…}` reference, or the key it names is not configured, or is not marked secret |
| `request_timeout`, `retrieval_timeout` | They do not parse as a duration, or are not positive |

A directory that does not exist is not refused here: it may be mounted after `serve` starts. A
retrieval from it is refused, naming it (FR-228).

The credential is set the way every secret is, from standard input, never as an argument:

```bash
gronin config set --secret embeddings_key < /path/to/embeddings-key
```

The two timeouts belong to the deployment because they depend on how fast its endpoint answers,
which a portable playbook cannot know (research.md §9). The result count and byte bound belong to
the playbook, within the ceilings in [retrieve.schema.json](./retrieve.schema.json). The query's
1,024-byte bound, the passage size and the 1 MiB document bound are the runtime's own and appear
in neither file.
