-- +goose Up
-- Reusable, named policy bundles (Postgres mirror). A run can reference one by name
-- (policyRef on POST /v1/runs) instead of inlining its budget — the deterministic
-- seam the AgentProfile model builds on. Re-registering the same name updates it.

CREATE TABLE policies (
    name           TEXT             PRIMARY KEY,
    budget_tokens  BIGINT           NOT NULL DEFAULT 0,
    budget_dollars DOUBLE PRECISION NOT NULL DEFAULT 0,
    budget_loops   BIGINT           NOT NULL DEFAULT 0,
    budget_seconds BIGINT           NOT NULL DEFAULT 0,
    tool_allowlist TEXT             NOT NULL DEFAULT '[]', -- JSON array of tool names
    approval_rules TEXT             NOT NULL DEFAULT '[]', -- JSON array of {tool, sideEffect}
    created_at     TEXT             NOT NULL,
    updated_at     TEXT             NOT NULL
);

-- +goose Down
-- Forward-only migrations (COMPATIBILITY.md). No down migration is provided.
