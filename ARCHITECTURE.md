# Architecture

A map of the RiskKernel codebase, so you know **where to make a change**. For the
*why* (positioning, scope), see [`docs/VISION.md`](docs/VISION.md).

## The shape in one breath

A single **Go binary** is the deterministic core. A thin **Python SDK** and the
**OpenTelemetry GenAI** wire format wrap it. The LLM proposes; deterministic Go
code disposes. There are three ways in, one core:

```
                 ┌──────────────────────── riskkernel (Go daemon, :7070) ───────────────────────┐
 your app  ──1── │  gateway (OpenAI/Anthropic proxy) ─┐                                          │
 SDK / curl ─2── │  httpapi (/v1 run-control API) ────┤                                          │
 MCP client ──── │  mcp (MCP tools/call gateway) ─────┼─▶ runs.Manager ─▶ governor (DISPOSE)     │
                 │                                     │        │            budgets, kill switch │──▶ provider ──▶ LLM (your key)
 OTel backend ◀3─│  otel (gen_ai.* + riskkernel.*) ◀──┘        ├─▶ pricing  (cost)               │
                 │                                              ├─▶ approval (human-in-the-loop)  │
                 │                                              └─▶ storage  (SQLite: runs/steps/ │
                 │                                                   ledger/checkpoints/approvals/│
                 │                                                   tool_calls/memory_facts)     │
                 └──────────────────────────────────────────────────────────────────────────────┘
       1 = Proxy (zero-code)   2 = Python SDK (deep control)   3 = OpenTelemetry (universal)
```

## The one rule that shapes everything

**All enforcement is deterministic Go and only Go.** Budgets, kill switch,
approval gating, tool allowlists, routing, retries, checkpoint/resume — none of
it is ever delegated to an LLM. The LLM only does the agent's own reasoning, which
lives in *your* code, not here. A change that puts a governance decision behind a
model call will be declined.

## Public vs private (the boundary)

| Public (stable contract) | Private |
|---|---|
| `api/v1/` — REST/JSON-RPC contract + pinned OTel attributes | everything under `internal/` |
| `sdks/` — the Python SDK | |
| `pkg/` — public Go packages (none yet) | |

External consumers — including any future product built on top — use only
`api/v1/`, `pkg/`, and the SDKs. Go's `internal/` convention enforces this; don't
work around it. Stability of the public surface is governed by
[`COMPATIBILITY.md`](COMPATIBILITY.md).

## Package tour (`internal/`)

| Package | Responsibility |
|---|---|
| `governor` | **The disposer.** Hard per-run token/dollar/loop/wall-clock budgets + kill switch. The headline; most-tested. |
| `pricing` | Deterministic USD pricing of token usage (static, config-overridable table). |
| `provider` | LLM provider abstraction (`Provider` interface). Native Anthropic; OpenAI/Bedrock/Ollama stubbed. The only outbound LLM calls. |
| `runs` | The run manager — identity + lifecycle around a `governor.Run`; write-through persistence; crash-resume reload. |
| `storage` | The `Store` interface + SQLite backend; embedded forward-only Goose migrations. The file the user owns. |
| `approval` | Human-in-the-loop gate: deterministic policy match + a queue that blocks a side-effecting call until resolved. |
| `gateway` | Surface 1 — OpenAI/Anthropic-compatible proxy; meters + governs every call. |
| `mcp` | MCP gateway — intercepts `tools/call` for allowlist + approval + audit. |
| `memory` | Git-native memory reader (user-owned md/yaml; path-traversal-safe; keyword search). |
| `otel` | Surface 3 — OpenTelemetry GenAI span export. |
| `httpapi` | HTTP server: mounts the proxy, the `/v1` run-control API, the memory + approval endpoints, and the local admin page. |
| `config` | Config from env + `.env`. Secrets only from here; never stored/logged. |
| `httpx`, `id`, `version`, `app` | Small shared helpers: JSON responses, UUIDs, build identity, bootstrap wiring. |

`cmd/riskkernel/` is the CLI/daemon entrypoint (`serve`, `chat`, `runs`,
`audit`, `approvals`, `memory`, `version`). `sdks/python/` is the SDK.

## Request flow — a governed proxy call

1. `gateway` receives `POST /v1/chat/completions`, resolves the run from the
   `X-RiskKernel-Run-Id` header (`runs.Manager`).
2. `run.BeginStep()` → `governor` enforces the **loop + time** budgets.
3. `run.CanProceed()` → `governor` enforces the **hard ceiling** (no work once a
   budget is spent).
4. Route by model → `provider.Chat(ctx, …)` (ctx is cancelled on kill switch /
   time budget / client disconnect).
5. `pricing.Cost(...)` → `run.RecordCall(...)` → `governor` re-checks budgets,
   `storage` writes the ledger + step + checkpoint, `otel` emits the span.
6. Response returns with `X-RiskKernel-*` headers; a budget-exhausting call still
   returns its paid-for result but the **next** call gets `402`.

## "I want to… — where do I code?"

- **Add an LLM provider** → implement `provider.Provider` in `internal/provider`, register it in `internal/app`.
- **Add/adjust an enforcement rule** → `internal/governor` (and tests — it's safety-critical).
- **Add a `/v1` endpoint** → handler in `internal/httpapi` + update `api/v1/openapi.yaml`.
- **Add a storage backend (e.g. Postgres)** → implement `storage.Store`; wire in `internal/app`.
- **Change model pricing** → `internal/pricing` (or via config overrides).
- **Add an approval channel** → `internal/approval` (see the webhook notifier as a template).
- **Touch the Python SDK** → `sdks/python/riskkernel` (+ `tests/`).
- **A DB schema change** → add a **new** `internal/storage/migrations/NNNN_*.sql` (forward-only; never edit a shipped migration).

## State & migrations

SQLite (WAL) is the default store — one file the user owns. Tables: `runs`,
`steps`, `tool_calls`, `cost_ledger`, `checkpoints`, `approvals`, `memory_facts`.
Migrations are embedded and **forward-only** (the daemon refuses to start if the
on-disk schema is newer than the binary). Postgres is a future opt-in behind the
same `Store` interface.
