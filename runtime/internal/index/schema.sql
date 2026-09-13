-- One collection's index (specs/003-retrieve/data-model.md). Derived data: deleting this
-- file loses nothing a walk of the collection's sources does not restore.

-- The generation the passages below make up. One row, replaced in the same transaction
-- as the passages it names, so a reader never sees one without the other.
CREATE TABLE IF NOT EXISTS generation (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    generation TEXT NOT NULL,
    identity   TEXT NOT NULL,
    built_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS documents (
    source TEXT PRIMARY KEY,
    digest TEXT NOT NULL,
    bytes  INTEGER NOT NULL
);

-- The tokenizer is Tokenizer in index.go, and part of the configuration identity.
CREATE VIRTUAL TABLE IF NOT EXISTS passages USING fts5(
    text,
    source UNINDEXED,
    ordinal UNINDEXED,
    start UNINDEXED,
    tokenize = 'unicode61'
);
