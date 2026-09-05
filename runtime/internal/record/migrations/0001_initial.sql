CREATE TABLE runs (
    id                    TEXT PRIMARY KEY,
    playbook_name         TEXT NOT NULL,
    resolved_playbook_ref TEXT,
    report_ref            TEXT,
    prompt_ref            TEXT,
    trigger_kind          TEXT NOT NULL,
    parent_run_id         TEXT REFERENCES runs(id),
    status                TEXT NOT NULL,
    started_at            TEXT NOT NULL,
    ended_at              TEXT,
    cost_usd              REAL,
    tokens                INTEGER,
    agent_session_id      TEXT,
    credential_source     TEXT,
    error                 TEXT
);

CREATE INDEX runs_by_start ON runs(started_at DESC);
CREATE INDEX runs_by_playbook ON runs(playbook_name, started_at DESC);

CREATE TABLE gathered_inputs (
    run_id     TEXT NOT NULL REFERENCES runs(id),
    name       TEXT NOT NULL,
    blob_ref   TEXT,
    bytes      INTEGER NOT NULL,
    truncated  INTEGER NOT NULL,
    exit_code  INTEGER NOT NULL,
    stderr_ref TEXT,
    PRIMARY KEY (run_id, name)
);

CREATE TABLE tool_calls (
    run_id      TEXT NOT NULL REFERENCES runs(id),
    sequence    INTEGER NOT NULL,
    name        TEXT NOT NULL,
    input_ref   TEXT,
    output_ref  TEXT,
    started_at  TEXT NOT NULL,
    duration_ms INTEGER NOT NULL,
    outcome     TEXT NOT NULL,
    PRIMARY KEY (run_id, sequence)
);

-- Small table, high value: a run that succeeds while repeatedly reaching for something
-- it cannot have is telling you its declared tool set is wrong. Nothing else surfaces it.
CREATE TABLE refused_actions (
    run_id   TEXT NOT NULL REFERENCES runs(id),
    sequence INTEGER NOT NULL,
    tool     TEXT NOT NULL,
    reason   TEXT NOT NULL,
    PRIMARY KEY (run_id, sequence)
);

CREATE TABLE sink_outcomes (
    run_id        TEXT NOT NULL REFERENCES runs(id),
    sink          TEXT NOT NULL,
    status        TEXT NOT NULL,
    items_created INTEGER NOT NULL,
    items_skipped INTEGER NOT NULL,
    detail        TEXT,
    PRIMARY KEY (run_id, sink)
);

-- FR-030, and the "Missed occurrence" entity in data-model.md. Not a run row with an
-- unusual status: nothing about it has a working directory, a cost or an outcome.
CREATE TABLE missed_occurrences (
    playbook_name TEXT NOT NULL,
    due_at        TEXT NOT NULL,
    noticed_at    TEXT NOT NULL,
    reason        TEXT NOT NULL,
    PRIMARY KEY (playbook_name, due_at)
);
