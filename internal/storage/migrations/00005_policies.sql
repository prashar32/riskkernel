-- +goose Up
-- Reusable, named policy bundles. A run can reference one by name (policyRef on
-- POST /v1/runs) instead of inlining its budget — the deterministic seam the
-- AgentProfile model builds on. Re-registering the same name updates the bundle.

CREATE TABLE policies (
    name           TEXT    PRIMARY KEY,
    budget_tokens  INTEGER NOT NULL DEFAULT 0,
    budget_dollars REAL    NOT NULL DEFAULT 0,
    budget_loops   INTEGER NOT NULL DEFAULT 0,
    budget_seconds INTEGER NOT NULL DEFAULT 0,
    tool_allowlist TEXT    NOT NULL DEFAULT '[]', -- JSON array of tool names
    approval_rules TEXT    NOT NULL DEFAULT '[]', -- JSON array of {tool, sideEffect}
    created_at     TEXT    NOT NULL,
    updated_at     TEXT    NOT NULL
);

-- +goose Down
-- Forward-only migrations (COMPATIBILITY.md). No down migration is provided.
