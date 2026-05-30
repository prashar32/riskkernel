<div align="center">

# RiskKernel

**The risk engine for your AI agents.**

Deterministic cost / loop / time budgets · full observability · crash-resumable runs · human-approval gates · a memory you own.
Self-hosted. Your keys. No telemetry. Point it at your existing agents — one env var.

[![CI](https://github.com/prashar32/riskkernel/actions/workflows/ci.yml/badge.svg)](https://github.com/prashar32/riskkernel/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Status](https://img.shields.io/badge/status-pre--v0.1-orange.svg)](CHANGELOG.md)

</div>

---

## The problem

Production AI agents fail in the same handful of ways, every time: **runaway loops**, **surprise token bills**, **no failure recovery**, **no observability**, **no human-in-the-loop**, **no governance**. Agent *frameworks* (LangGraph, CrewAI, AutoGen) orchestrate the reasoning — but none of them ship the guardrails that keep a run from burning $400 in a midnight loop while you sleep.

RiskKernel is the **deterministic, run-level reliability layer** that sits in front of your agents and enforces hard limits. The LLM proposes; deterministic Go code disposes. Every irreversible action is gated.

It is **not** another gateway (LiteLLM/Portkey own routing), **not** another observability dashboard (Langfuse/Phoenix own traces), and **not** a content-guardrails engine (Guardrails AI/NeMo own PII/jailbreak). It interoperates with all of those and competes on the one thing nobody ships in a single self-hosted binary: **deterministic run controls** — the agent SRE layer.

## What it does

| Capability | What it means |
|---|---|
| 💸 **Hard cost ceiling per run** | A run that hits its dollar/token budget is killed cleanly, state persisted. |
| 🔁 **Hard loop-iteration cap** | No more infinite agent loops. |
| ⏱️ **Hard wall-clock budget** | Runs that exceed their time budget halt. |
| 💾 **Crash-resumable checkpoints** | `SIGKILL` a run; `riskkernel runs resume <id>` picks up from the last step. |
| ✋ **Framework-agnostic approval gates** | Side-effecting tool calls pause for human approval — CLI, local web, or webhook. |
| 🧠 **Memory you own** | Git-native markdown/YAML on your disk; episodic state in your SQLite. |
| 📡 **OpenTelemetry GenAI** | Emits `gen_ai.*` spans to *your* backend (Grafana/SigNoz/Datadog/Langfuse). |

## Three ways to adopt — pick the one that fits

1. **Proxy (zero code).** Set one env var: `OPENAI_BASE_URL=http://localhost:7070/v1`. Every call is intercepted, budgeted, logged, checkpointed, and forwarded to the real provider with your key.
2. **Python SDK (deep control).** `pip install riskkernel`, then `@governed_run` / `@governed_tool` / `runtime.budget(...)` / `ApprovalGate`. Adapters for the Claude Agent SDK, OpenAI Agents SDK, and LangChain.
3. **OpenTelemetry (universal).** RiskKernel is an OTLP endpoint *and* emitter — govern apps already instrumented with OpenLLMetry / the OpenAI Agents SDK, and export to the backend you already run.

## Quickstart

> Status: **pre-v0.1, under active construction.** The build follows the sequence in [`CLAUDE.md`](CLAUDE.md) §8. This section becomes a working 60-second quickstart at the v0.1.0 tag.

```bash
# build the single static binary
go build -o riskkernel ./cmd/riskkernel
export ANTHROPIC_API_KEY=sk-ant-...
./riskkernel serve            # daemon on :7070
```

## Design principles

- **Deterministic core in Go.** All enforcement (budgets, kill switches, gating, routing, retries, checkpointing) lives in compiled, statically-typed code — never in an LLM.
- **No telemetry, ever.** Nothing phones home. It's a verifiable promise; see [`SECURITY.md`](SECURITY.md).
- **Your keys, your infra.** Secrets come from env / `.env` / OS-keyring, never stored in state, never logged.
- **Near-zero adoption friction.** Every decision is judged by *"how few changes must an existing user make?"* One env var is the gold standard.
- **Backwards compatibility is sacred.** Self-hosted users can't be force-migrated. See [`COMPATIBILITY.md`](COMPATIBILITY.md).

## License

[Apache-2.0](LICENSE). The runtime stays permissive, forever.
