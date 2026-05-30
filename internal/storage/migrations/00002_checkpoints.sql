-- +goose Up
-- Crash-resumable checkpoints. A checkpoint snapshots a run's usage at a step,
-- plus an opaque user-supplied payload (e.g. conversation messages, scratch
-- state) that a resuming agent restarts from. The run row remains the source of
-- truth for usage/budget on resume; checkpoints add the resumable payload and a
-- per-step history.

CREATE TABLE checkpoints (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id                  TEXT    NOT NULL REFERENCES runs(id),
    step_idx                INTEGER NOT NULL,
    name                    TEXT    NOT NULL DEFAULT '',
    usage_prompt_tokens     INTEGER NOT NULL DEFAULT 0,
    usage_completion_tokens INTEGER NOT NULL DEFAULT 0,
    usage_dollars           REAL    NOT NULL DEFAULT 0,
    usage_loops             INTEGER NOT NULL DEFAULT 0,
    payload                 TEXT    NOT NULL DEFAULT '{}',
    created_at              TEXT    NOT NULL
);

CREATE INDEX idx_checkpoints_run ON checkpoints(run_id);

-- +goose Down
-- Forward-only migrations (COMPATIBILITY.md). No down migration is provided.
