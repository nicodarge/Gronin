-- The guard (specs/002-guard/data-model.md). A trigger that did not become a run, the
-- acceptance of a trigger that waits, and a playbook's last tick on a deployment with no
-- coordination backend.

-- FR-117. Not a run: nothing about it has a working directory, a cost or an outcome.
CREATE TABLE refusals (
    id                 TEXT PRIMARY KEY,
    playbook_name      TEXT NOT NULL,
    trigger_kind       TEXT NOT NULL,
    due_at             TEXT,
    waiting_trigger_id TEXT,
    mechanism          TEXT NOT NULL,
    detail             TEXT NOT NULL,
    refused_at         TEXT NOT NULL
);

CREATE INDEX refusals_by_time ON refusals(refused_at DESC);

-- FR-127. Written when a trigger is accepted to wait, so that a kill cannot erase it.
CREATE TABLE waiting_triggers (
    id            TEXT PRIMARY KEY,
    playbook_name TEXT NOT NULL,
    playbook_path TEXT NOT NULL,
    trigger_kind  TEXT NOT NULL,
    trigger_ref   TEXT,
    accepted_at   TEXT NOT NULL,
    expires_at    TEXT NOT NULL,
    instance      TEXT NOT NULL,
    outcome       TEXT NOT NULL,
    outcome_at    TEXT,
    run_id        TEXT REFERENCES runs(id)
);

-- FR-111: one live waiting trigger per playbook per state directory, held by the
-- database rather than by whichever process happens to check first.
CREATE UNIQUE INDEX waiting_triggers_one_live
    ON waiting_triggers(playbook_name) WHERE outcome = 'waiting';

-- FR-128 on a single host: read and written only while the playbook's file lock is held.
CREATE TABLE last_ticks (
    playbook_name TEXT PRIMARY KEY,
    due_at        TEXT NOT NULL,
    host          TEXT NOT NULL,
    instance      TEXT NOT NULL,
    run_id        TEXT NOT NULL
);

ALTER TABLE runs ADD COLUMN waiting_trigger_id TEXT REFERENCES waiting_triggers(id);
ALTER TABLE runs ADD COLUMN waited_ms INTEGER;
ALTER TABLE runs ADD COLUMN claim_reach TEXT;
ALTER TABLE runs ADD COLUMN claim_token INTEGER;
