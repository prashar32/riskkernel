# RiskKernel — Vision & Scope

RiskKernel is the **deterministic reliability runtime for AI agents** — the risk
engine that sits in front of your agents and enforces hard limits. This document
states what we're building, the principles that constrain it, and what is
deliberately out of scope.

## The wedge

Production agent systems fail in a consistent way: runaway loops, surprise token
spend, no failure recovery, no observability, no human-in-the-loop, no governance.
Orchestration frameworks coordinate reasoning but don't ship these guardrails.

RiskKernel occupies the gap that gateways, observability tools, and content
guardrails leave open: **deterministic, run-level control** in one self-hosted
binary —

1. Hard cost ceiling per run (kill switch)
2. Hard loop-iteration cap
3. Hard wall-clock time budget per run
4. Crash-resumable checkpointing
5. Framework-agnostic human-in-the-loop approval gate
6. User-owned, git-native memory layer

We don't try to out-compete gateways on provider routing or observability tools on
dashboards — we **interoperate** with them (OpenAI-compatible proxy, OpenTelemetry
GenAI) and compete only on deterministic reliability: the "agent SRE" layer.

## Principles (non-negotiable)

- **The LLM proposes; deterministic code disposes.** All enforcement — budgets,
  kill switches, approval gating, tool-permission checks, retries, routing,
  checkpoint/resume — is compiled, statically-typed Go. An LLM is never in the
  enforcement path.
- **No telemetry, ever.** Nothing phones home. Spans go only to an OTLP endpoint
  *you* configure. See [`SECURITY.md`](../SECURITY.md).
- **Your keys, your infra.** Secrets come from env / `.env` / OS-keyring, never
  stored in state, never logged.
- **Near-zero adoption friction.** Every decision is judged by *"how few changes
  must an existing user make?"* One env var is the gold standard.
- **Defensible from fundamentals.** This is a systems/reliability product, not an
  ML/research product. We don't ship features that need hand-waving.
- **Backwards compatibility is sacred.** Self-hosted users can't be force-migrated;
  see [`COMPATIBILITY.md`](../COMPATIBILITY.md).

## Architecture at a glance

A single Go binary (the deterministic core) plus a thin Python SDK, speaking
OpenTelemetry GenAI as the universal wire format. Three integration surfaces, one
core:

1. **Proxy / gateway** — an OpenAI/Anthropic-compatible endpoint; the zero-code
   on-ramp (one env var).
2. **Python SDK** — thin client + framework adapters for deep control (loop counts,
   per-step budgets, local tools, approval gates).
3. **OpenTelemetry ingress/egress** — govern apps already instrumented, and export
   to the backend you already run.

State lives in SQLite by default (the file you own); Postgres is an opt-in backend
behind the same interface.

## Scope guardrails

In scope: deterministic budget/loop/time enforcement, kill switch, checkpoint &
resume, human-in-the-loop approval, OTel export, the cost ledger, MCP tool
governance, and a git-native memory layer.

Out of scope (interoperate, don't rebuild): a 100+-provider router, an
observability dashboard product, a content/policy guardrails engine, multi-agent
orchestration inside the runtime, and a required heavyweight datastore.

## Status & roadmap

v0.9.0 (released). The runtime is the entire current focus. See
[`CHANGELOG.md`](../CHANGELOG.md) for what has landed and the public contract in
[`api/v1/`](../api/v1) for the stable surface SDKs build on.

The license is [Apache-2.0](../LICENSE) and the runtime stays permissive, forever.
