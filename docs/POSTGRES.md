# Postgres backend

SQLite is RiskKernel's default state store — zero-config, a single file you own,
right for one daemon on one host. For a **multi-instance / HA deployment** (several
daemons sharing one durable state, rolling restarts, a managed database), RiskKernel
has an opt-in **Postgres** backend behind the same `Store` interface.

It's opt-in and off by default: nothing changes unless you point RiskKernel at a
database.

## Enabling it

Set one env var to a standard libpq/pgx connection string:

```bash
export RISKKERNEL_DATABASE_URL="postgres://user:pass@host:5432/riskkernel?sslmode=require"
riskkernel serve
```

When `RISKKERNEL_DATABASE_URL` is set, RiskKernel uses Postgres and ignores the
SQLite data dir; unset, it falls back to the SQLite file in `RISKKERNEL_DATA_DIR`.
On startup the daemon logs which backend is active:

```
state store ready  backend=postgres
```

The connection string carries credentials, so it follows the same rule as every
other secret: read from the environment (or `.env`), never logged, never stored in
state.

## What you get

Everything the SQLite backend does, against shared Postgres state:

- Governed runs, steps, the cost ledger, tool-call audit trail, approvals, policy
  bundles, episodic memory facts, and crash-resumable checkpoints.
- **Crash-resume across instances:** a run persisted by one daemon is reloaded and
  keeps enforcing its already-spent budget after a restart — or on another instance
  pointed at the same database.
- The same spend summaries (`riskkernel audit`, the usage rollups), including
  grouping by run metadata.

The schema mirrors SQLite's exactly (timestamps stored as RFC3339 text, JSON
marshaled in the application), so behavior is identical. A shared conformance test
suite runs against both backends to keep them at parity.

## Migrations & safety

- Forward-only migrations run in a transaction on startup (embedded Goose), the
  same model as SQLite.
- **Downgrade protection:** the daemon refuses to start if the database schema is
  newer than the binary understands, so a rolled-back deploy can't corrupt state.
- No down migrations — roll forward, never back ([`COMPATIBILITY.md`](../COMPATIBILITY.md)).

## Operational notes

- Point every daemon instance at the same database; they coordinate through it.
- Use a connection pooler (PgBouncer) or a managed Postgres if you run many
  instances; RiskKernel keeps a small bounded pool per instance.
- Back up the database as you would any system of record — it holds the auditable
  cost ledger and the resumable run state.
- Migrating existing SQLite state into Postgres is not automated; a fresh Postgres
  database starts empty. Treat the backend choice as a deployment-time decision.
