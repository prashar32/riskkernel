# Changelog

All notable changes to RiskKernel are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

We ship loudly: every user-facing change lands here, and the stability of each
surface is governed by [`COMPATIBILITY.md`](COMPATIBILITY.md).

## [Unreleased]

### Added
- **SDK: resume a run after a crash.** `Runtime.resume_run(run_id)` attaches to an
  existing governed run (it neither creates a new run nor cancels on error), so a
  Python agent can pick its work back up from the last checkpoint after a `SIGKILL`.
  The run keeps its server-side budget and already-spent usage, so it can't
  overspend by restarting. See the [SDK README](sdks/python/README.md#resume-after-a-crash).
- **`examples/kill-9-resume`** — the flagship crash-resume demo. A checkpointing
  agent whose daemon is `kill -9`'d mid-run resumes from its last checkpoint and
  finishes without re-spending; `./demo.sh` scripts the whole crash-and-recover and
  proves the loop counter doesn't double. Key-free.
- **Crash-resume guide** ([`docs/RESUME.md`](docs/RESUME.md)) — the full model: what's
  restored, the exact-once budget guarantee, writing a resumable agent with
  `resume_run`, and the one thing that's yours (idempotent side effects).

### Fixed
- **Resume is exact-once across a mid-step crash.** If the daemon died after a step
  was counted (`BeginStep`) but before that step's checkpoint, the run row was left
  one step ahead of the last durable snapshot — so resume re-attempted the step and
  the loop budget counted it twice. The daemon now reloads each run from its last
  checkpoint, rolling that partial step back so it's counted exactly once. (The
  dollar budget was already exact — a partial step records no cost.)

## [0.2.0] - 2026-06-04

A frictionless-adoption release: a one-line CLI install, three runnable key-free
examples, safe default budgets out of the box, config-updatable model pricing, and
a fix that makes LangChain budget enforcement actually stop the chain. No breaking
API changes; forward-compatible with existing v0.1.x state.

### Added
- **`tool_calls` audit trail is now readable** — `GET /v1/runs/{id}/tool-calls`,
  `riskkernel audit tools <run-id>`, and a `tool_calls` array in `audit export`
  surface the governed MCP tool-call record (it was previously write-only). Thanks
  @Sebastefanelli! ([#38](https://github.com/prashar32/riskkernel/issues/38))
- **`docs/BUDGETS.md`** — the budget surface (cost/loop/time/tokens) defined in
  one place as a public contract: the four dimensions, where they can be set,
  precedence, halt semantics, and the stability promise.
- **Updatable token→$ pricing.** Point `RISKKERNEL_PRICING_FILE` at a JSON file of
  `{ model: { inputPerM, outputPerM } }` to override the built-in list prices or
  add models — the dollar budget's basis, kept current as providers change pricing
  without recompiling. Overrides layer on the defaults; the daemon refuses to start
  on a malformed, unknown-field, or negative-rate file. See
  [`docs/BUDGETS.md`](docs/BUDGETS.md#pricing--the-dollar-budgets-basis).
- **Runnable, key-free examples.** `examples/wrap-your-agent` (a generic governed
  Python loop), `examples/langchain` (a LangChain agent capped at its loop budget),
  and `examples/mcp` (the MCP gateway's allowlist, approval gate, and audit trail) —
  each runs with nothing but the daemon, no API key.
- **One-line CLI install.** `go install github.com/prashar32/riskkernel/cmd/riskkernel@latest`
  needs no clone; the SDK's install-from-source command is documented (a PyPI
  publish is still pending).

### Changed
- **Safe default budgets out of the box.** A daemon started with *no*
  `RISKKERNEL_DEFAULT_*` variable set now applies a conservative default budget
  to runs created without an explicit one — **$5 / 100 loops / 1 hour per run**
  — and logs it prominently at startup. Previously an unconfigured daemon left
  runs unlimited, which is the wrong default for a reliability runtime.
  *Behavior change:* setting any `RISKKERNEL_DEFAULT_*` variable (even to `0`,
  meaning unlimited) is explicit control and disables the safe defaults
  entirely — explicit configuration is always respected as-is.

### Fixed
- **LangChain budget halts now actually stop the chain.** The callback handler
  raised `BudgetExceeded` / `ApprovalDenied` from its hooks, but LangChain swallows
  exceptions thrown inside a callback unless the handler sets `raise_error=True` — so
  on a real LangChain agent the halt was silently dropped and the run kept spending
  past budget. The handler now propagates the halt; the OpenAI Agents adapter was
  audited for the same flaw (it propagates correctly) and has regression tests.

## [0.1.2] - 2026-06-01

A polish release: correctness and UX fixes, plus a cleaner audit-export shape.
No breaking changes; forward-compatible with existing v0.1.x state.

### Fixed
- **Loop/time-budget halts now persist the run status.** A run halted on its loop
  or wall-clock budget is enforced in `BeginStep`/`CanProceed`, which returned
  before writing through — so `runs list`, `audit`, and `GET /v1/runs/{id}` still
  showed it as `running`. The halt (and its reason) is now persisted on that path,
  matching the token/dollar halt behavior. ([#34](https://github.com/prashar32/riskkernel/issues/34))
- **`audit export` totals use API-style JSON keys** — `storage.LedgerTotals` now
  carries json tags, so the `totals` object emits `runId`/`calls`/`promptTokens`/…
  to match the rest of the API instead of capitalized Go field names. Thanks
  @yzhkali! ([#30](https://github.com/prashar32/riskkernel/issues/30))

### Changed
- **MCP gateway audits allowlist-blocked tool calls.** A `tools/call` refused by the
  allowlist is now recorded in the `tool_calls` audit trail (status `blocked`)
  instead of being dropped silently — a refused call is part of the audit record.
- **Memory entry lookup accepts a name without its extension** — `GET
  /v1/memory/entry?name=runbook` now resolves `runbook.md` (it previously required
  the exact filename and 404'd otherwise). Path-traversal safety is unchanged.

## [0.1.1] - 2026-05-31

A fast follow-up to v0.1.0: makes the Python SDK installable from a build, and
ships a runnable demo of the headline feature (deterministic budget/loop governance
killing a runaway agent).

### Added
- **`examples/codebase-qa`** — a runnable demo agent (Python SDK + proxy) that
  showcases the headline feature: a real ReAct loop over a codebase that the
  deterministic governor halts on its loop/dollar budget. Includes `--mode normal`
  (completes within budget) and `--mode runaway` (governor kills it), a bundled
  sample codebase, and expected terminal output. No RAG, vector DB, or framework.

### Fixed
- **Python SDK packaged an empty wheel** — `pip install riskkernel` (and installing
  the SDK by path) installed no modules, so `import riskkernel` failed. The project
  sits in a repo subdirectory and hatchling's VCS file selector excluded the package
  files; selecting from the filesystem (`ignore-vcs`) fixes it. ([#32](https://github.com/prashar32/riskkernel/issues/32))

## [0.1.0] - 2026-05-31

The first release: the deterministic reliability runtime for AI agents —
governor, proxy, crash-resume, observability, human-in-the-loop, MCP governance,
and a memory you own, in one self-hosted binary. Three integration surfaces
(proxy, Python SDK, OpenTelemetry), Apache-2.0, no telemetry.

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
- **Crash-resume** — checkpoints table (migration `00002`) snapshots usage after
  each step plus an opaque payload to restart from. On startup the daemon reloads
  non-terminal runs and reconstructs each governor with its spent usage, so a
  `SIGKILL`'d run keeps enforcing against its accumulated budget (budget is *not*
  reset). `GET /v1/checkpoints/{run_id}` returns the latest checkpoint;
  `riskkernel runs resume <id>` reports a run's resumable state.
- **OpenTelemetry GenAI export (Surface 3)** — one span per governed model call
  carrying the pinned `gen_ai.*` + `riskkernel.*` attribute set (provider, model,
  token usage, cost USD, budget remaining, halt reason, finish reason). OTLP
  gRPC/HTTP via standard `OTEL_*` env vars. **Off unless an endpoint is
  configured** — spans go only to the user's backend. Example Jaeger backend +
  dashboard guidance in `examples/otel/`.
- **Human-in-the-loop approval gate (Surface: HITL)** — a side-effecting tool call
  that policy gates pauses until a human approves or denies it. Deterministic
  policy match (exact tool or side-effect glob, plus a fail-closed default-safe
  mode); the gate blocks the call and is resolved via three channels: the CLI
  (`riskkernel approvals list / approve / deny`), a local embedded admin page
  (`/admin/approvals`), and an optional webhook (`RISKKERNEL_APPROVAL_WEBHOOK`).
  `POST /v1/runs/{id}/approve`, `GET /v1/approvals`, and `GET /v1/runs/{id}`
  (surfaces `pendingApproval` + `waiting_approval` status). Approvals are persisted
  (migration `00003`) as an audit trail. Webhook is user-configured egress only
  (see SECURITY.md).
- **Run-control API** — `POST /v1/runs` (create with budget),
  `POST /v1/runs/{id}/steps` (loop/time enforcement → 402 on halt),
  `POST /v1/runs/{id}/checkpoints`, `POST /v1/runs/{id}/cancel`,
  `POST /v1/runs/{id}/approvals` (request → poll), `GET /v1/approvals/{id}`.
- **Python SDK (Surface 2)** — `pip install riskkernel`, a stdlib-only thin client:
  `Runtime`, `governed_run`, `Budget`, `Run.step/checkpoint/cancel/proxy_config`,
  `ApprovalGate`, `@governed_tool`. Lazy-imported framework adapters for LangChain
  (callback handler), the Claude Agent SDK (PreToolUse hook), and the OpenAI Agents
  SDK (RunHooks). Verified end-to-end against the daemon; CI on Python 3.9/3.12.
- **MCP gateway** — a JSON-RPC reverse proxy at `POST /mcp` in front of an upstream
  MCP server. Forwards every method transparently; intercepts `tools/call` to
  enforce a per-tool allowlist (exact or glob), classify read-only vs
  side-effecting, route side-effecting tools through the approval gate (blocking,
  bounded by `RISKKERNEL_MCP_APPROVAL_TIMEOUT`), and record an auditable
  `tool_calls` row. Enabled by `RISKKERNEL_MCP_UPSTREAM`; allowlist/read-only via
  `RISKKERNEL_MCP_ALLOWLIST` / `RISKKERNEL_MCP_READONLY`. Point your MCP client at
  the gateway and governance is invisible to allowed, approved calls.
- **Git-native memory layer** — a user-owned directory of markdown/YAML/text the
  agent reads (`RISKKERNEL_MEMORY_DIR`, default `./memory`). Deterministic
  retrieval: list, read, keyword search; markdown frontmatter (`title`/
  `description`) surfaced; reads are path-traversal-safe. **No embedding index /
  vector DB** (off by default, not implemented in v0.1). Episodic facts (small
  key/value) persist in SQLite (migration `00004`). `GET /v1/memory`,
  `GET /v1/memory/entry`, `GET`/`PUT /v1/memory/facts`; `riskkernel memory
  list/show`; Python SDK `list_memory`/`read_memory`/`list_facts`/`put_fact`.
- **Packaging & release** — single static binary (`make build`); cross-compiled
  multi-arch Docker image (distroless, nonroot) on GHCR, **cosign-signed**
  (keyless) on each `v*` tag; GoReleaser binaries + checksums + GitHub release;
  `govulncheck` + CodeQL in CI. One-line `docker run` quickstart.

[Unreleased]: https://github.com/prashar32/riskkernel/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/prashar32/riskkernel/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/prashar32/riskkernel/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/prashar32/riskkernel/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/prashar32/riskkernel/releases/tag/v0.1.0
