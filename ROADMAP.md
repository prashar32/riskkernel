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
- **OpenAI- and Anthropic-compatible proxy** — point one env var at RiskKernel and
  every call (streaming or not) is metered, priced, and budget-enforced (BYO key).
  Native providers: Anthropic, OpenAI, and Ollama (local, key-free).
- **Human-in-the-loop approval** — gate side-effecting tools; resolve from the CLI,
  a local web page, a webhook, or **Slack** ([`docs/APPROVALS_SLACK.md`](docs/APPROVALS_SLACK.md)).
- **Policy-as-code, enforced per-run** — named policy bundles via `POST /v1/policies`
  or a reviewed `riskkernel.yaml`, with a dry-run against recorded runs; a run created
  under a bundle is governed by its tool allowlist and approval rules, not just its
  budget ([`docs/POLICY.md`](docs/POLICY.md)).
- **OpenTelemetry GenAI — export and ingress** — emit cost/halt/tool spans into your
  existing backend (ready-made **Grafana + Tempo** and **SigNoz** dashboards), *and*
  ingest GenAI spans (`POST /v1/traces`) to meter apps RiskKernel never proxied
  ([`docs/OTLP_INGRESS.md`](docs/OTLP_INGRESS.md)).
- **Spend attribution** — roll cost up across runs by team/user/feature
  (`riskkernel audit summary --by metadata.team`), with the run name and tags also on
  the OTel spans so the same grouping works in your backend.
- **MCP gateway** — govern an agent's `tools/call` (allowlist + approval + audit).
- **Compliance evidence export** — controls mapped to OWASP / EU AI Act references
  with a tamper-evident, hash-chained event log ([`docs/COMPLIANCE.md`](docs/COMPLIANCE.md)).
- **Git-native memory** — markdown/YAML the agent reads, that you own.
- **Storage** — zero-config SQLite by default; an opt-in **Postgres** backend for
  multi-instance / HA behind the same `Store` interface ([`docs/POSTGRES.md`](docs/POSTGRES.md)).
- **SDKs** — Python (`pip install riskkernel`) and TypeScript (`@riskkernel/sdk`),
  with adapters for the Claude Agent SDK, OpenAI Agents, LangChain, LlamaIndex,
  CrewAI, AutoGen, and PydanticAI (Python), and the Vercel AI SDK (TypeScript).
- **Operability** — a Prometheus `/metrics` endpoint, a `riskkernel doctor` setup
  check, shell completions, and a one-command `docker compose` quickstart.
- **Measured, low overhead** — the enforcement decision is ~150 ns and zero
  allocations per call, with a reproducible cost benchmark and a timed `kill -9`
  recovery benchmark (exact-once spend) behind the claims ([`docs/PERFORMANCE.md`](docs/PERFORMANCE.md)).

## Next

Where the work is heading near-term:

- **More native providers** — AWS Bedrock ([#24](https://github.com/prashar32/riskkernel/issues/24));
  the long tail via LiteLLM-as-upstream.
- **More backend dashboards** — a Datadog dashboard to join the Grafana and SigNoz
  examples.
- **Easier install** — a Homebrew tap for `brew install riskkernel`
  ([#97](https://github.com/prashar32/riskkernel/issues/97)).

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
