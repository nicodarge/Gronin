# Contract — The embeddings client

What the runtime sends to a collection's embeddings API and what it accepts back. Every rule is
drawn from research.md §7, which observed one real server; that section also lists what a stub
built from the observation cannot show, and none of it is claimed here.

## Request

```http
POST <embeddings.url> HTTP/1.1
Content-Type: application/json
Authorization: Bearer <the credential, when one is configured>

{"model": "<embeddings.model>", "input": ["<passage or query>", "..."]}
```

| Rule | Why |
| ---- | --- |
| `model` and `input`, `input` always a list of strings | The shape both protocols observed agree on |
| Never `encoding_format` | It changes the type of what comes back |
| Never `dimensions` | It changes the vector length under an unchanged model name, which FR-218 would then refuse on every query |
| Never an empty string | The passage rules produce none, and an empty query is refused before this (FR-228) |
| At most 16 inputs per request | research.md §9 |
| Every input has passed the redactor, applied here and not by the caller | FR-223. It is the redactor the record store and the log are built with, so the three cannot disagree about what a secret is |
| Each request bounded by the collection's `request_timeout` | FR-208. Exceeding it is the API being unreachable |
| Only that collection's passages and the queries of retrievals from it | FR-222 |

The credential travels in the header and nowhere else: not in the URL, not in a log line, not in
the index, not in the record (FR-224).

## Response

Accepted only when the status is 200 and the body is:

```json
{"data": [{"embedding": [0.1, 0.2, 0.3], "index": 0}, {"embedding": [0.4, 0.5, 0.6], "index": 1}]}
```

with exactly one entry per input, each `index` present once and in range, and every vector of one
non-zero length. Vectors are ordered by `index`, never by position, and normalised to unit length
before they are stored or compared: the observed server returned unit vectors, and the protocol
does not promise it. Other fields — `object`, `model`, `usage` — are ignored.

## Refusal

Every failure refuses the retrieval, names the API by its URL, and falls back to nothing (FR-207):

| What happened | The refusal says |
| ------------- | ---------------- |
| Connection refused, name not resolved | the API could not be reached, and the error |
| No answer within `request_timeout` | the API did not answer within it |
| Any status but 200 | the status, and `error.message` when the body is `{"error": {"message": …}}` |
| A body that does not decode, or `data` short, long, duplicated or out of range | the response was not an embeddings response, and which rule it broke |
| Vectors of differing or zero length within one response | the lengths found |
| A query vector whose length differs from the index's | the two lengths (FR-218) |

The credential is never part of a refusal, a URL carrying user information is refused by the
catalogue rather than printed, and a response body is quoted only through `error.message`.
