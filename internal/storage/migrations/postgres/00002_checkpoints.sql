-- +goose Up
-- Crash-resumable checkpoints (Postgres mirror). A checkpoint snapshots a run's
-- usage at a step plus an opaque user-supplied payload that a resuming agent
-- restarts from. The run row stays the source of truth for usage/budget on resume;
-- checkpoints add the resumable payload and a per-step history.

CREATE TABLE checkpoints (
    id                      BIGINT           GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id                  TEXT             NOT NULL REFERENCES runs(id),
    step_idx                BIGINT           NOT NULL,
    name                    TEXT             NOT NULL DEFAULT '',
    usage_prompt_tokens     BIGINT           NOT NULL DEFAULT 0,
    usage_completion_tokens BIGINT           NOT NULL DEFAULT 0,
    usage_dollars           DOUBLE PRECISION NOT NULL DEFAULT 0,
    usage_loops             BIGINT           NOT NULL DEFAULT 0,
    payload                 TEXT             NOT NULL DEFAULT '{}',
    created_at              TEXT             NOT NULL
);

CREATE INDEX idx_checkpoints_run ON checkpoints(run_id);

-- +goose Down
-- Forward-only migrations (COMPATIBILITY.md). No down migration is provided.
