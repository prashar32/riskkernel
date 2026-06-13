# Roadmap

RiskKernel is the deterministic reliability layer for AI agents — cost/loop/time
budgets, crash-resumable runs, human approval gates, tool governance, and an audit
trail you own, self-hosted, your keys, no telemetry.

This roadmap is a direction, not a contract — it moves with what users need. The
[CHANGELOG](CHANGELOG.md) is the record of what's actually shipped; the public
contract and its stability are in [`api/v1`](api/v1) and [`COMPATIBILITY.md`](COMPATIBILITY.md).

## Shipped

The core runtime is built and released:

- **Deterministic budgets** — hard per-run cost, token, loop, and wall-clock ceilings
  that halt a run *before* it overspends ([`docs/BUDGETS.md`](docs/BUDGETS.md)).
- **Crash-resume** — `kill -9` a run mid-flight and resume it without re-spending
  ([`docs/RESUME.md`](docs/RESUME.md)).
- **OpenAI-compatible proxy** — point one env var at RiskKernel and every call is
  metered, priced, and budget-enforced (BYO key).
- **Human-in-the-loop approval** — gate side-effecting tools; resolve from the CLI,
  a local web page, a webhook, or **Slack** ([`docs/APPROVALS_SLACK.md`](docs/APPROVALS_SLACK.md)).
- **Policy-as-code** — named policy bundles via `POST /v1/policies` or a reviewed
  `riskkernel.yaml`, with a dry-run against recorded runs ([`docs/POLICY.md`](docs/POLICY.md)).
- **OpenTelemetry GenAI export** — cost/halt/tool spans into your existing backend,
  with a ready-made Grafana + Tempo dashboard.
- **MCP gateway** — govern an agent's `tools/call` (allowlist + approval + audit).
- **Compliance evidence export** — controls mapped to OWASP / EU AI Act references
  with a tamper-evident, hash-chained event log ([`docs/COMPLIANCE.md`](docs/COMPLIANCE.md)).
- **Git-native memory** — markdown/YAML the agent reads, that you own.
- **SDKs** — Python (`pip install riskkernel`) and TypeScript (`@riskkernel/sdk`),
  with LangChain, OpenAI Agents, and Vercel AI SDK adapters.
- **Measured, low overhead** — the enforcement decision is ~150 ns and zero
  allocations per call ([`docs/PERFORMANCE.md`](docs/PERFORMANCE.md)).

## Next

Where the work is heading near-term:

- **Per-run policy enforcement** — a referenced policy bundle applies its budget to a
  run today; extend that to enforce the bundle's tool allowlist and approval rules
  per-run, not just globally.
- **More framework adapters** — CrewAI ([#83](https://github.com/prashar32/riskkernel/issues/83)),
  AutoGen ([#84](https://github.com/prashar32/riskkernel/issues/84)),
  PydanticAI ([#85](https://github.com/prashar32/riskkernel/issues/85)),
  LlamaIndex ([#86](https://github.com/prashar32/riskkernel/issues/86)).
- **More native providers** — AWS Bedrock ([#24](https://github.com/prashar32/riskkernel/issues/24))
  and local Ollama ([#23](https://github.com/prashar32/riskkernel/issues/23)).
- **Streaming proxy** — SSE pass-through with mid-stream budget enforcement
  ([#22](https://github.com/prashar32/riskkernel/issues/22)).
- **OTLP ingress** — consume GenAI spans to govern apps RiskKernel didn't instrument
  ([#90](https://github.com/prashar32/riskkernel/issues/90)).
- **Postgres storage backend** — behind the same `Store` interface, SQLite stays the
  default ([#25](https://github.com/prashar32/riskkernel/issues/25)).
- **Operability** — a `/metrics` endpoint ([#91](https://github.com/prashar32/riskkernel/issues/91)),
  a `riskkernel doctor` setup check ([#94](https://github.com/prashar32/riskkernel/issues/94)),
  shell completions ([#93](https://github.com/prashar32/riskkernel/issues/93)),
  and a Homebrew tap ([#97](https://github.com/prashar32/riskkernel/issues/97)).

## Exploring

Bigger bets, gated on demand from real usage:

- **Fleet-level budget throttling** — graceful degradation as a fleet of agents
  approaches a shared budget.
- **Policy replay** — re-evaluate a new policy against a corpus of historical runs.
- **Optional semantic memory** — an embeddings index, off by default (no vector DB
  in the core).

## Principles that won't change

- **Apache 2.0 core, feature-complete forever** — paid is the hosted dashboard and
  support, never gated runtime features.
- **No telemetry** — the OSS binary never phones home ([`SECURITY.md`](SECURITY.md)).
- **Deterministic enforcement** — the same state always yields the same allow/deny;
  no LLM is ever in the enforcement decision.
- **Near-zero adoption friction** — meet existing agents where they are; one env var
  is the gold standard.

Have a need that isn't here? Open an issue — the roadmap follows real usage.
