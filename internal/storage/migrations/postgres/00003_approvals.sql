-- +goose Up
-- Human-in-the-loop approvals (Postgres mirror). A side-effecting tool call that
-- policy gates pauses here as a pending row until a human approves or denies it.
-- Resolved rows are an audit trail of who allowed which side effect, when, and why.

CREATE TABLE approvals (
    id          TEXT   PRIMARY KEY,
    run_id      TEXT   NOT NULL REFERENCES runs(id),
    step_idx    BIGINT NOT NULL,
    tool        TEXT   NOT NULL,
    side_effect TEXT   NOT NULL DEFAULT '',
    arguments   TEXT   NOT NULL DEFAULT '{}',
    status      TEXT   NOT NULL, -- pending | approved | denied
    reason      TEXT   NOT NULL DEFAULT '',
    decided_by  TEXT   NOT NULL DEFAULT '',
    created_at  TEXT   NOT NULL,
    decided_at  TEXT
);

CREATE INDEX idx_approvals_run ON approvals(run_id);
CREATE INDEX idx_approvals_status ON approvals(status);

-- +goose Down
-- Forward-only migrations (COMPATIBILITY.md). No down migration is provided.
