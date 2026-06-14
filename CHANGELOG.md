# Changelog

All notable changes to RiskKernel are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

We ship loudly: every user-facing change lands here, and the stability of each
surface is governed by [`COMPATIBILITY.md`](COMPATIBILITY.md).

## [Unreleased]

### Added
- **Troubleshooting guide.** [`docs/TROUBLESHOOTING.md`](docs/TROUBLESHOOTING.md)
  maps the errors new users actually hit — each as symptom → cause → fix — to the
  exact messages the daemon emits: a missing/invalid provider key, port 7070 already
  in use, the **expected** HTTP 402 budget halt (with the `HaltReason` it carries —
  not a bug), a killed run that didn't resume, the schema-newer-than-binary
  downgrade guard, a 401 from API auth, an unreachable OTLP endpoint, and OTLP
  ingress 400s. It leads with `riskkernel doctor` and is linked from the README
  quickstart.
- **One-command docker-compose quickstart.** [`examples/quickstart-compose`](examples/quickstart-compose)
  brings up the daemon, a stand-in mock LLM, and a tiny looping agent with a single
  `docker compose up` — so a newcomer watches the deterministic loop budget hard-stop
  a runaway agent (HTTP 402, `loop_budget_exceeded`) with **no API key** and no local
  Go/Python setup. The daemon pulls fresh on each run so a stale cached image can't
  skew the demo; a short README shows how to swap the mock for a real provider.

## [0.7.0] - 2026-06-14

Reach and scale. RiskKernel now plugs into the whole Python agent ecosystem —
adapters for **LlamaIndex, CrewAI, AutoGen, and PydanticAI** join the existing
LangChain and OpenAI/Claude Agent SDK adapters — and completes the OpenTelemetry
surface with an **OTLP trace ingress** that meters apps it didn't proxy. Streaming
now works on **both** the OpenAI- and Anthropic-compatible endpoints, a native
**Ollama** provider runs local models, and an opt-in **Postgres** backend takes the
state layer multi-instance. Plus operability: a Prometheus **`/metrics`** endpoint,
shell completions, **`riskkernel doctor`**, and per-run policy enforcement. No
breaking API changes; forward-compatible with v0.6.x state.

### Added
- **Postgres storage backend (opt-in).** For multi-instance / HA deployments,
  RiskKernel can run its state on Postgres instead of the default SQLite file — set
  `RISKKERNEL_DATABASE_URL` to a connection string and the daemon uses Postgres for
  runs, steps, the cost ledger, tool-call audit trail, approvals, policy bundles,
  memory facts, and crash-resumable checkpoints. It sits behind the same `Store`
  interface and the same schema as SQLite (forward-only migrations, downgrade
  protection), and a shared conformance suite holds the two backends at behavioral
  parity. SQLite stays the zero-config default; nothing changes unless you set the
  URL. See [`docs/POSTGRES.md`](docs/POSTGRES.md).
- **AutoGen adapter (Python SDK).** `from riskkernel.adapters.autogen import
  GovernedChatCompletionClient` — wrap your AutoGen model client once and hand it to
  your existing `AssistantAgent` (or team) to bind it to a governed run with no other
  code change. One model request counts as one governed step, so the deterministic
  loop/time budget halts a runaway agent; with `gate_tools=True` each tool call the
  model requests (a `FunctionCall` in the result) routes through the human-approval
  gate before the agent can run it. Targets the actively maintained v0.4+ line
  (`autogen-agentchat` / `autogen-core` >= 0.4), not the legacy `pyautogen` 0.2 API;
  `autogen` is lazily handled (the wrapper is duck-typed and imports nothing), so it
  stays an optional dependency. A single agent run propagates the halt typed; a team
  (`RoundRobinGroupChat`, …) re-raises it as a `RuntimeError`, so `governed_run_errors()`
  (from the same module) restores the typed `BudgetExceeded`/`ApprovalDenied` around a
  team call.
- **OTLP trace ingress.** RiskKernel can now act as an OTLP/HTTP trace endpoint
  (`POST /v1/traces`), the consume side of the OpenTelemetry surface — point any
  exporter at it (`OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:7070`) and the GenAI
  model calls an app already traces (OpenLLMetry, the OpenAI Agents SDK, the Vercel
  AI SDK) show up against governed runs with tokens and cost metered into the
  ledger. Correlates by `riskkernel.run.id` (on the span or its resource); accepts
  protobuf and JSON and replies with an OTLP `ExportTraceServiceResponse` (a span
  without a run id is reported as a rejected partial-success span). Scope is observe
  + meter — a consumed call is recorded (and marks the run halted if it crosses the
  budget) but isn't blocked after the fact. Off by default; enable with
  `RISKKERNEL_OTEL_INGRESS_ENABLED` and authenticated like the rest of the API. See
  [`docs/OTLP_INGRESS.md`](docs/OTLP_INGRESS.md).
- **CrewAI adapter (Python SDK).** `from riskkernel.adapters.crewai import
  RiskKernelStepCallback` — a `step_callback` you wire onto a CrewAI `Agent` or the
  whole `Crew` to bind it to a governed run with no other code change. One agent step
  counts as one governed step, so the deterministic loop/time budget halts a runaway
  crew; with `gate_tools=True` each tool call routes through the human-approval gate.
  The halt is raised from the callback and propagates out of the crew (CrewAI's
  `step_callback` is synchronous and re-raised by the executor, unlike its
  fire-and-forget event bus, which would drop it). `crewai` is lazily imported, so it
  stays an optional dependency; supported against `crewai` >= 0.80, < 2.
- **PydanticAI adapter (Python SDK).** `from riskkernel.adapters.pydantic_ai import
  govern` — wrap your model with `Agent(govern(model, run))` to bind a PydanticAI
  agent to a governed run with no other code change. One model request counts as one
  governed step, so the deterministic loop/time budget halts a runaway agent; with
  `gate_tools=True` each tool call the model proposes routes through the human-approval
  gate before the agent executes it. The halt is raised from the model wrapper and
  propagates out of `agent.run()` / `agent.run_sync()` — PydanticAI only retries its
  own `ModelRetry` signal, so the deterministic `BudgetExceeded`/`ApprovalDenied`
  surfaces to the caller and stops the agent rather than being retried into another
  paid request. Built on PydanticAI's `WrapperModel` contract, which forwards every
  model method to the wrapped model and is stable across the post-1.0 line; both the
  non-streaming and streaming request paths are governed. `pydantic-ai` is lazily
  imported, so it stays an optional dependency; supported against `pydantic-ai`
  (`pydantic-ai-slim`) >= 1, < 2.
- **Streaming proxy.** Both `POST /v1/chat/completions` and `POST /v1/messages` now
  support `stream:true`: the budget is enforced before the stream opens, the
  provider's SSE is forwarded to the client verbatim (authentic OpenAI or Anthropic
  chunks, no translation) while token usage is metered from the stream's own
  accounting, and the run's context — time budget, kill switch, or client
  disconnect — cuts a live stream. Dollar/token budgets are checked pre-stream and
  recorded after (so the next call is refused if it went over). A provider whose
  backend doesn't implement streaming returns a clear 501 rather than silently
  buffering.
- **Python SDK: LlamaIndex adapter.** `RiskKernelCallbackHandler` (from
  `riskkernel.adapters.llama_index`) is a LlamaIndex `BaseCallbackHandler` that ticks
  one governed step per LLM call (`CBEventType.LLM`), so a run's loop/time budget is
  enforced over a LlamaIndex query or agent and a halt surfaces as `BudgetExceeded` —
  the LlamaIndex analog of the LangChain and OpenAI-Agents adapters. Register it on
  `Settings.callback_manager` and the governance is invisible until the budget bites;
  LlamaIndex doesn't swallow handler exceptions, so no extra flag is needed. Pass
  `gate_tools=True` to route `CBEventType.FUNCTION_CALL` through the approval gate.
  `llama-index-core` is lazily imported, so the SDK stays dependency-free; pinned to
  the `llama-index-core` >= 0.10 callback protocol.
- **Prometheus `/metrics` endpoint.** Scrape the daemon's own state: governed runs
  by status, halted runs by halt reason, total spend in dollars and tokens, priced
  model calls, and the pending-approval queue depth. Plain Prometheus text
  exposition (version 0.0.4), authenticated like the rest of the API, served when a
  durable store is configured. It's local metrics you scrape — no phone-home, no
  prompt content, no PII — and it's hand-rolled, so it adds no dependency. See
  [`docs/METRICS.md`](docs/METRICS.md) for the metric list and an example scrape config.
- **Shell completions.** `riskkernel completion <bash|zsh|fish>` prints a completion
  script to stdout — tab-complete the top-level commands and their sub-subcommands
  (`runs list|resume`, `audit export|tools|compliance`, `policy validate|dry-run`,
  `approvals list|approve|deny`, `memory list|show`). Hand-written, no new
  dependency; the `rk` alias is completed too. Each shell's script carries its own
  one-line install hint.
- **`riskkernel doctor`.** Diagnose a setup before relying on it: a checklist over
  the data dir (creatable/writable), the default provider and its credential, the
  default budget (flags an explicitly-unlimited one), the API token, a configured
  `riskkernel.yaml` (validated), and whether the daemon is reachable. Exits non-zero
  on a hard failure so it's CI-friendly.
- **Per-run policy enforcement.** A run created under a policy bundle (`policyRef`)
  is now governed by that bundle, not just its budget: the MCP gateway enforces the
  bundle's tool **allowlist** for that run (a tool outside it is blocked even if the
  global allowlist would allow it) and its **approval rules** on top of the global
  fail-safe gating — a bundle can *add* a requirement, never silently drop one. The
  run's `policyRef` is persisted, so enforcement survives a daemon restart. See
  [`docs/POLICY.md`](docs/POLICY.md#per-run-enforcement).
- **Native Ollama provider.** Run local models through RiskKernel — set
  `RISKKERNEL_DEFAULT_PROVIDER=ollama` (key-free) and budgets, the proxy, the audit
  trail, and crash-resume all work the same as for a hosted provider. Talks to
  Ollama's `/api/chat`; token usage comes from `prompt_eval_count` / `eval_count`.
  Point it at a remote server with `RISKKERNEL_OLLAMA_BASE_URL` (default
  `http://localhost:11434`).

## [0.6.0] - 2026-06-13

Governance and compliance. Approvals can route to **Slack**, policy is now
**code** — reusable bundles via `POST /v1/policies` or a reviewed `riskkernel.yaml`,
with a dry-run against recorded runs — and a new **compliance evidence export** maps
RiskKernel's recorded controls to OWASP / EU AI Act references with a tamper-evident
event log. Plus the published enforcement-overhead number (~150 ns, zero allocations)
and a public roadmap. No breaking API changes; forward-compatible with v0.5.x state.

### Added
- **Published enforcement overhead + a public roadmap.** The deterministic
  enforcement decision is measured at ~150 ns and **zero heap allocations** per
  governed call (`go test -bench ./internal/governor`); methodology and numbers are
  in [`docs/PERFORMANCE.md`](docs/PERFORMANCE.md), and [`ROADMAP.md`](ROADMAP.md) lays
  out what's shipped and where it's heading.
- **Compliance evidence export.** `riskkernel audit compliance <run-id>` produces an
  auditor-ready report: the controls RiskKernel recorded (budget enforcement, human
  oversight, tool governance, record-keeping) mapped to the relevant OWASP and EU AI
  Act references, plus a **hash-chained, tamper-evident** event log (each event's
  hash chains the previous, so any edit/reorder/truncation breaks it — the
  verification procedure is embedded). It's an *evidence* export with a built-in
  disclaimer — not a legal compliance determination; nothing is inferred by an LLM.
  See [`docs/COMPLIANCE.md`](docs/COMPLIANCE.md).
- **Reusable policy bundles.** Register a named bundle of a default budget, a tool
  allowlist, and approval rules with `POST /v1/policies` (re-registering the same
  name updates it; `GET /v1/policies` and `GET /v1/policies/{name}` read them back),
  then a run references it by name: `POST /v1/runs` with `policyRef` applies the
  bundle's budget, and an inline `budget` overrides it field-by-field. Deterministic
  config persisted in the SQLite state — the seam the `AgentProfile` model builds on.
- **Approve from Slack.** A new push channel for the human-in-the-loop gate: set
  `RISKKERNEL_APPROVAL_SLACK_BOT_TOKEN` + `RISKKERNEL_APPROVAL_SLACK_CHANNEL` and a
  gated, side-effecting tool call is posted to the channel with **Approve / Deny**
  buttons; the click resolves the pending action and the message is rewritten with
  the outcome. The interactivity callback (`/v1/integrations/slack/interactions`) is
  authenticated by the Slack request signature (`RISKKERNEL_APPROVAL_SLACK_SIGNING_SECRET`),
  verified over the raw body with a replay window and failing closed without it — not
  the daemon API token, which Slack can't send. Works alongside the existing
  CLI/web/webhook channels. See [`docs/APPROVALS_SLACK.md`](docs/APPROVALS_SLACK.md).
- **Policy-as-code (`riskkernel.yaml`).** Define those bundles in a YAML file
  reviewed in PRs and applied on startup: point the daemon at it with
  `RISKKERNEL_POLICY_FILE` and the bundles register on boot (a malformed file fails
  startup, not silently). `riskkernel policy validate <file>` checks it, and
  `riskkernel policy dry-run <file> <run-id>` replays a recorded run against a bundle
  to show what it *would* have halted, blocked, or gated — changing nothing. See
  [`docs/POLICY.md`](docs/POLICY.md) and [`examples/policy`](examples/policy).

## [0.5.0] - 2026-06-13

The TypeScript SDK lands. `@riskkernel/sdk` is on npm — a thin, dependency-free
client at parity with the Python SDK: governed runs, budgets, the governing proxy,
approval gates, crash-resume (`resumeRun`), and a Vercel AI SDK adapter that governs
a Node agent with ~no code change. Alongside it: a reproducible cost benchmark and a
crash-resume correctness fix (a halted run now stays halted across a restart when its
id is reused). No breaking API changes; forward-compatible with v0.4.x state.

### Added
- **TypeScript SDK: crash-resume.** `Runtime.resumeRun(runId, fn)` re-attaches a
  Node/TypeScript agent to an existing governed run after a crash — it neither
  creates a new run nor cancels on error, so the daemon's restored budget and usage
  continue enforcing without re-spending. Mirrors the Python SDK's `resume_run`; read
  `run.latestCheckpoint()` to resume from where the agent left off. See
  [`docs/RESUME.md`](docs/RESUME.md) and [`sdks/typescript`](sdks/typescript).
- **TypeScript SDK: Vercel AI SDK adapter.** `governMiddleware(run)` (from
  `@riskkernel/sdk/vercel`) is an AI SDK language-model middleware that ticks one
  governed step per model call, so a run's loop/time budget is enforced and a halt
  surfaces as `BudgetExceeded` out of `generateText` / `streamText` — the JS analog
  of the Python LangChain / OpenAI-Agents adapters. `@ai-sdk/provider` is an optional
  peer used at compile time only, so the core stays dependency-free. Pinned to AI SDK
  v5; runnable example at [`examples/vercel-ai-sdk`](examples/vercel-ai-sdk).
- **Point a provider at a custom upstream.** Set `RISKKERNEL_OPENAI_BASE_URL` or
  `RISKKERNEL_ANTHROPIC_BASE_URL` to route that provider through an OpenAI-compatible
  gateway, a corporate proxy, or a local mock (e.g. for benchmarking) instead of its
  default API endpoint. RiskKernel-namespaced so it never collides with the
  caller-facing `OPENAI_BASE_URL` used to point an app *at* RiskKernel.
- **A reproducible cost benchmark** ([`benchmark/`](benchmark)) — runs the same
  looping agent with and without a RiskKernel dollar budget against a deterministic
  mock provider, and reports the spend saved straight from the cost ledger. Key-free
  and tunable: `python3 benchmark/benchmark.py`.

### Fixed
- **A halted run stays halted when its id is reused after a restart.** Reload
  restores only *running* runs, so a proxy call that reused the run-id of a run that
  had already halted (e.g. hit its dollar budget) was minting a fresh, default-budget
  run for that id and returning `200` instead of `402` — the halt didn't survive the
  restart. The proxy path (`GetOrCreate`) now consults the store on a cache miss and
  restores an existing run as-is: a terminal run (budget halt or kill-switch cancel)
  comes back terminal and keeps refusing work; only a genuinely new id gets a fresh
  run.

## [0.4.0] - 2026-06-07

Observability, rounded out: tool-call governance now shows up in your traces, a
ready-made Grafana + Tempo dashboard turns RiskKernel's spans into cost/halt/tool
panels, and OTLP export can authenticate to backends that require it (Honeycomb,
Grafana Cloud). No breaking API changes; forward-compatible with v0.3.x state.

### Added
- **Tool governance shows up in your traces.** Every governed MCP `tools/call` now
  emits an OpenTelemetry span (`execute_tool {tool}`) alongside the model-call spans,
  carrying `gen_ai.tool.name`, `riskkernel.tool.side_effect`, and
  `riskkernel.tool.status` (`approved`, `blocked`, `denied`, or `timeout`). Allowlist
  blocks and approval denials are now visible in whatever OTLP backend you already
  run — a refused call is marked with an error span status so it stands out. See
  [`api/v1/otel-genai.md`](api/v1/otel-genai.md) and [`examples/otel`](examples/otel).
- **A ready-made cost & governance dashboard** ([`examples/otel/grafana`](examples/otel/grafana)) —
  a provisioned Grafana + Tempo stack that turns RiskKernel's spans into panels:
  spend over time and per run, token burn, budget halts by reason, tool-call outcomes,
  and p95 latency by model. Built from the spans you already emit (Tempo TraceQL
  metrics — no extra instrumentation); `docker compose up`, no import step.
- **Authenticated OTLP export.** Set the standard `OTEL_EXPORTER_OTLP_HEADERS` (or the
  traces-specific `OTEL_EXPORTER_OTLP_TRACES_HEADERS`) — a comma-separated list of
  `key=value` pairs — to send an auth header on every span export, so RiskKernel can
  feed a backend that requires one (Honeycomb's `x-honeycomb-team`, a `Bearer` token,
  Grafana Cloud). Header values carry secrets and are never logged. See
  [`examples/otel`](examples/otel#other-backends).

## [0.3.0] - 2026-06-06

The crash-resume moat, proven and polished: a real `kill -9` → resume demo, the
`resume_run` SDK entry point, exact-once budgets across a mid-step crash, and a
crash-resume guide — plus a one-command `riskkernel init` on-ramp and a clean
`pip install riskkernel`. No breaking API changes; forward-compatible with v0.2.x
state.

### Added
- **The SDK is on PyPI** — install it with the ordinary `pip install riskkernel`
  (no git URL, no `#subdirectory`). Published on release via PyPI Trusted Publishing.
- **`riskkernel init`** — scaffolds a working starting point in one command: a `.env`
  (provider key, default budget, the data dir where runs and crash-resume checkpoints
  live) and a runnable, key-free `quickstart.py` (a governed loop the budget stops),
  then prints the next steps. Never overwrites existing files.
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

[Unreleased]: https://github.com/prashar32/riskkernel/compare/v0.7.0...HEAD
[0.7.0]: https://github.com/prashar32/riskkernel/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/prashar32/riskkernel/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/prashar32/riskkernel/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/prashar32/riskkernel/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/prashar32/riskkernel/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/prashar32/riskkernel/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/prashar32/riskkernel/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/prashar32/riskkernel/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/prashar32/riskkernel/releases/tag/v0.1.0
