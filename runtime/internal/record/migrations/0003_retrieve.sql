-- The retrieve stage (specs/003-retrieve/data-model.md). What a run retrieved, in full:
-- a record points at nothing in the index, because the index moves under a run's feet and
-- a pointer into it answers what the index says now rather than what the run saw.

CREATE TABLE retrievals (
    run_id              TEXT NOT NULL REFERENCES runs(id),
    sequence            INTEGER NOT NULL,
    as_name             TEXT NOT NULL,
    collection          TEXT NOT NULL,
    mode                TEXT NOT NULL,
    -- Null only for a retrieval refused before it read a generation.
    generation          TEXT,
    generation_built_at TEXT,
    identity            TEXT,
    query               TEXT NOT NULL,
    query_truncated     INTEGER NOT NULL,
    outcome             TEXT NOT NULL,
    results_ref         TEXT,
    results_bytes       INTEGER NOT NULL,
    count_truncated     INTEGER NOT NULL,
    bytes_truncated     INTEGER NOT NULL,
    error               TEXT,
    PRIMARY KEY (run_id, sequence)
);

-- One row per result, as the agent received it. The score is the evidence of a rank and
-- not a measure: it is only comparable within one retrieval.
CREATE TABLE retrieved_items (
    run_id      TEXT NOT NULL REFERENCES runs(id),
    sequence    INTEGER NOT NULL,
    rank        INTEGER NOT NULL,
    score       REAL NOT NULL,
    source      TEXT NOT NULL,
    ordinal     INTEGER NOT NULL,
    -- Quoted: OFFSET is a keyword, and the column is named for what it holds.
    "offset"    INTEGER NOT NULL,
    content_ref TEXT,
    PRIMARY KEY (run_id, sequence, rank),
    FOREIGN KEY (run_id, sequence) REFERENCES retrievals(run_id, sequence)
);
