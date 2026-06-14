-- +goose Up
-- Episodic memory (Postgres mirror): small, fact-granular key/value state an agent
-- accumulates across a run (distinct from the git-native markdown/YAML the user
-- owns on disk). Keyed by namespace + key; run_id is optional attribution (no FK —
-- facts may be global, and a fact write must not fail on an unknown run).

CREATE TABLE memory_facts (
    namespace  TEXT NOT NULL,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    run_id     TEXT,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (namespace, key)
);

CREATE INDEX idx_memory_facts_ns ON memory_facts(namespace);

-- +goose Down
-- Forward-only migrations (COMPATIBILITY.md). No down migration is provided.
