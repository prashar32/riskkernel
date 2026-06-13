-- +goose Up
-- Record which policy bundle a run was created under (POST /v1/runs policyRef), so
-- the bundle's tool allowlist and approval rules can be enforced per-run — not just
-- its budget. Empty means no bundle (global config applies).

ALTER TABLE runs ADD COLUMN policy_ref TEXT NOT NULL DEFAULT '';

-- +goose Down
-- Forward-only migrations (COMPATIBILITY.md). No down migration is provided.
