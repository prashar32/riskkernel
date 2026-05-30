-- +goose Up
-- Initial schema: governed runs, their steps, tool calls, and the cost ledger.
-- All timestamps are stored as RFC3339 text (UTC). Money lives in the ledger and
-- must stay auditable (see `riskkernel audit export`).

CREATE TABLE runs (
    id                      TEXT    PRIMARY KEY,
    name                    TEXT    NOT NULL DEFAULT '',
    status                  TEXT    NOT NULL,
    halt_reason             TEXT    NOT NULL DEFAULT '',
    budget_tokens           INTEGER NOT NULL DEFAULT 0,
    budget_dollars          REAL    NOT NULL DEFAULT 0,
    budget_loops            INTEGER NOT NULL DEFAULT 0,
    budget_seconds          INTEGER NOT NULL DEFAULT 0,
    usage_prompt_tokens     INTEGER NOT NULL DEFAULT 0,
    usage_completion_tokens INTEGER NOT NULL DEFAULT 0,
    usage_dollars           REAL    NOT NULL DEFAULT 0,
    usage_loops             INTEGER NOT NULL DEFAULT 0,
    metadata                TEXT    NOT NULL DEFAULT '{}',
    created_at              TEXT    NOT NULL,
    updated_at              TEXT    NOT NULL
);

CREATE TABLE steps (
    run_id            TEXT    NOT NULL REFERENCES runs(id),
    idx               INTEGER NOT NULL,
    status            TEXT    NOT NULL,
    prompt_tokens     INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    dollars           REAL    NOT NULL DEFAULT 0,
    started_at        TEXT    NOT NULL,
    ended_at          TEXT,
    PRIMARY KEY (run_id, idx)
);

CREATE TABLE tool_calls (
    id          TEXT    PRIMARY KEY,
    run_id      TEXT    NOT NULL REFERENCES runs(id),
    step_idx    INTEGER NOT NULL,
    tool        TEXT    NOT NULL,
    side_effect TEXT    NOT NULL DEFAULT '',
    arguments   TEXT    NOT NULL DEFAULT '{}',
    status      TEXT    NOT NULL,
    created_at  TEXT    NOT NULL
);

CREATE TABLE cost_ledger (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id            TEXT    NOT NULL REFERENCES runs(id),
    step_idx          INTEGER NOT NULL,
    provider          TEXT    NOT NULL,
    model             TEXT    NOT NULL,
    prompt_tokens     INTEGER NOT NULL,
    completion_tokens INTEGER NOT NULL,
    dollars           REAL    NOT NULL,
    priced            INTEGER NOT NULL DEFAULT 1,
    response_id       TEXT    NOT NULL DEFAULT '',
    created_at        TEXT    NOT NULL
);

CREATE INDEX idx_steps_run ON steps(run_id);
CREATE INDEX idx_tool_calls_run ON tool_calls(run_id);
CREATE INDEX idx_ledger_run ON cost_ledger(run_id);

-- +goose Down
-- RiskKernel uses forward-only migrations (COMPATIBILITY.md / Temporal policy).
-- A down migration is intentionally not provided; roll forward, never back.
