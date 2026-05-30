# Compatibility & Stability Charter

> Self-hosted users cannot be force-migrated. The cardinal sin is breaking a
> user's data or config on upgrade. This charter is a binding contract about what
> is stable across versions and how we evolve everything else. Modeled on the
> Grafana Agent stability RFC (RFC-0008) and Temporal's migration policy.

This charter takes effect at **v0.1.0**. Before v0.1.0 the project is explicitly
unstable and anything may change.

## Stability tiers

### Stable across all minor versions (the public contract)

These surfaces will not break within a major version (`0.x` is treated as the
pre-1.0 major; we hold the line within `0.x` to the extent practical and document
every exception here):

| Surface | What's covered |
|---|---|
| **`/v1` REST/gRPC API** | Request/response shapes in `api/v1/`. New optional fields may be added; existing fields keep their meaning. |
| **Config schema** | Fields explicitly marked `stable` in the schema. Governed by top-level `schemaVersion`. |
| **SQLite schema** | Forward-migratable only. We never silently change a user's layout. |
| **CLI flags** | Flags marked stable in `riskkernel --help`. |
| **Python SDK public methods** | Documented `@governed_run`, `@governed_tool`, `runtime.budget`, `runtime.checkpoint`, `ApprovalGate`. |
| **OTel attribute names** | The `gen_ai.*` and `riskkernel.*` attribute set pinned in `api/v1/otel-genai.md`. |
| **Plugin interfaces** | Go interfaces in `pkg/plugin/` (storage, memory, approval channel, provider). |

### Not covered (may change at any time)

- Everything under `internal/`.
- Undocumented endpoints and config fields.
- Log line formats and human-readable CLI output text.
- Anything behind an `--enable-feature=<name>` experimental flag.

## Config schema versioning

- Every config file declares a top-level `schemaVersion: N`.
- The binary ships in-process migrators that understand older schema versions.
- `riskkernel config migrate` writes the upgraded form, and **only** on that
  explicit command. We never rewrite a user's config silently.

## Database migrations

- Forward migrations run inside a transaction on startup, via embedded
  [Goose](https://github.com/pressly/goose) (`//go:embed`).
- **Downgrade protection:** the binary refuses to start if the on-disk schema is
  *newer* than the binary understands.
- **No downgrade migrations** (Temporal's policy). Roll forward, never back.
- Forward-compatibility is guaranteed for **N+2 minor versions**.
- Layout-changing major bumps ship a separate `riskkernel upgrade-storage`
  command **two minor versions in advance** of the change.

## Storage backend

- SQLite (WAL) is the default; Postgres is opt-in behind the same `Store`
  interface. We never silently change a user's storage layout.

## Deprecation policy

- A deprecation is announced in a minor release and the `CHANGELOG.md`.
- Deprecated surfaces remain for **≥ 2 minor releases or 6 months**, whichever is
  longer, and are removed only at a major bump.
- Where possible we auto-shim old names to new ones so nothing breaks at removal.

## CI gates (enforced, not aspirational)

- Prior versions' integration suites run against `HEAD`.
- A `migrate-from-vX` smoke test: boot an old SQLite file → upgrade → assert read
  parity.
- A contract-breaking-change check (`buf breaking` / OpenAPI diff) on `api/v1`.

## Telemetry

**None.** No phone-home, ever. See [`SECURITY.md`](SECURITY.md). Any "versions
behind" hint is a local log line against a build-time constant; any usage sharing
is strictly opt-in and OFF by default.
