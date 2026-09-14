-- The webhook trigger (specs/004-webhook/data-model.md). A delivery accepted at the
-- ingress, the identity repeat detection consults, one hand-off per delivery and bound
-- playbook, and the two shapes of refusal — authenticated and not.

-- FR-312 through FR-315. Deliberately not `identity` alone as the key: two sources may
-- reuse the same identity text without meaning the same delivery.
CREATE TABLE deliveries (
    id           TEXT PRIMARY KEY,
    source       TEXT NOT NULL,
    identity     TEXT NOT NULL,
    identity_kind TEXT NOT NULL,
    received_at  TEXT NOT NULL,
    peer         TEXT NOT NULL,
    body_ref     TEXT NOT NULL,
    body_sha256  TEXT NOT NULL,
    repeats      INTEGER NOT NULL DEFAULT 0,
    instance     TEXT NOT NULL,
    supersedes   TEXT REFERENCES deliveries(id),
    state        TEXT NOT NULL
);

CREATE INDEX deliveries_by_time ON deliveries(received_at DESC);

-- FR-316 through FR-318. A table of its own rather than a key on the delivery: when an
-- identity recurs past its window it is repointed at a new delivery, and the earlier
-- delivery keeps its own row and its own history.
CREATE TABLE delivery_identities (
    source      TEXT NOT NULL,
    identity    TEXT NOT NULL,
    delivery_id TEXT NOT NULL REFERENCES deliveries(id),
    accepted_at TEXT NOT NULL,
    PRIMARY KEY (source, identity)
);

-- One per delivery and bound playbook (data-model.md, *Hand-off*), created with the
-- delivery so that a process killed partway through handing it off has decided some of
-- them and left the rest `pending`.
CREATE TABLE handoffs (
    delivery_id   TEXT NOT NULL REFERENCES deliveries(id),
    playbook_name TEXT NOT NULL,
    state         TEXT NOT NULL,
    decided_at    TEXT,
    PRIMARY KEY (delivery_id, playbook_name)
);

-- FR-332. An authenticated refusal: the signature passed and something after it did not.
-- One row per request; a repeat is not a refusal and is counted on the delivery instead.
CREATE TABLE delivery_refusals (
    id            TEXT PRIMARY KEY,
    source        TEXT NOT NULL,
    reason        TEXT NOT NULL,
    value_name    TEXT,
    playbook_name TEXT,
    delivery_id   TEXT REFERENCES deliveries(id),
    received_at   TEXT NOT NULL,
    peer          TEXT NOT NULL,
    body_ref      TEXT
);

CREATE INDEX delivery_refusals_by_time ON delivery_refusals(received_at DESC);

-- FR-333. A request refused before its signature passed. Counted, never stored as a row
-- per request: anyone who can reach the ingress can send one of these.
CREATE TABLE ingress_refusal_counts (
    interval_start TEXT NOT NULL,
    reason         TEXT NOT NULL,
    source_bucket  TEXT NOT NULL,
    count          INTEGER NOT NULL,
    last_peer      TEXT NOT NULL,
    last_at        TEXT NOT NULL,
    PRIMARY KEY (interval_start, reason, source_bucket)
);

-- FR-336, and the guard's own records (data-model.md, *Guard records*): each gains a link
-- to the delivery it came from, null for anything else.
ALTER TABLE runs ADD COLUMN delivery_id TEXT REFERENCES deliveries(id);
ALTER TABLE refusals ADD COLUMN delivery_id TEXT REFERENCES deliveries(id);
ALTER TABLE waiting_triggers ADD COLUMN delivery_id TEXT REFERENCES deliveries(id);
