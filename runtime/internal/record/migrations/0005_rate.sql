-- FR-114 on a single host: a playbook's rate window, since there is no lease here to
-- attach a rate slot to. A row is written the instant a scheduled or manual trigger takes
-- the claim, and is never deleted: the window moves past it, it does not free it (C9).
CREATE TABLE rate_starts (
    playbook_name TEXT NOT NULL,
    run_id        TEXT NOT NULL,
    started_at    TEXT NOT NULL
);

CREATE INDEX rate_starts_by_playbook ON rate_starts(playbook_name, started_at);
