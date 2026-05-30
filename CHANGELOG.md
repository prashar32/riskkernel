# Changelog

All notable changes to RiskKernel are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html) once `v0.1.0` ships.

We ship loudly: every user-facing change lands here, and the stability of each
surface is governed by [`COMPATIBILITY.md`](COMPATIBILITY.md).

## [Unreleased]

### Added
- **Public contract (`api/v1`)** — OpenAPI 3.1 spec for the versioned REST surface:
  `POST /v1/runs`, `GET /v1/runs/{id}`, `POST /v1/runs/{id}/approve`,
  `POST /v1/runs/{id}/cancel`, `GET /v1/checkpoints/{run_id}`, `POST /v1/policies`.
  This is the frozen contract Product 2 (and all SDKs) consume.
- **`COMPATIBILITY.md`** — backwards-compatibility stability charter.
- **`SECURITY.md`** — security posture and the verifiable no-telemetry promise.
- **OTel GenAI attribute set** pinned in `api/v1/otel-genai.md`.
- **Provider abstraction** — `Provider` interface returning token usage; native
  Anthropic Messages implementation; OpenAI / Bedrock / Ollama stubbed.
- **`riskkernel serve`** — daemon skeleton with health and version endpoints.
- **Deterministic governor** — hard per-run token / dollar / loop / wall-clock
  budgets with a kill switch, enforced in Go around every call. Halts cancel the
  run's context to interrupt in-flight calls.
- **Cost pricing** — static, config-overridable USD/1M-token price table.
- **OpenAI-compatible proxy (Surface 1)** — `POST /v1/chat/completions` and
  `POST /v1/messages`. The zero-code on-ramp: one env var
  (`OPENAI_BASE_URL=http://localhost:7070/v1`) puts every call under governance.
  Stamps `X-RiskKernel-*` headers (run-id, step, cost, tokens, halt reason);
  returns `402` when a run is out of budget. Bearer token doubles as a virtual
  key. Streaming is rejected (501) in v0.1.
- **Default budget config** — `RISKKERNEL_DEFAULT_{TOKENS,DOLLARS,LOOPS,SECONDS}`.
- **SQLite state + cost ledger** — durable `Store` interface (SQLite default,
  Postgres later) with tables for `runs`, `steps`, `tool_calls`, and an auditable
  `cost_ledger`. Pure-Go driver (`modernc.org/sqlite`, no cgo) keeps the single
  static binary. Embedded Goose migrations run forward-only in a transaction on
  startup; the daemon **refuses to start if the on-disk schema is newer** than the
  binary (downgrade protection). WAL mode, foreign keys enforced.
- **Write-through persistence** — the run manager persists runs, steps, and every
  priced call to the ledger (best-effort, background context, never fails a call).
- **CLI** — `riskkernel runs list` and `riskkernel audit export <run-id>`.

[Unreleased]: https://github.com/prashar32/riskkernel/commits/main
