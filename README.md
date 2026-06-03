<div align="center">

# RiskKernel

**The risk engine for your AI agents.**

Deterministic cost / loop / time budgets · full observability · crash-resumable runs · human-approval gates · a memory you own.
Self-hosted. Your keys. No telemetry. Point it at your existing agents — one env var.

[![CI](https://github.com/prashar32/riskkernel/actions/workflows/ci.yml/badge.svg)](https://github.com/prashar32/riskkernel/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/prashar32/riskkernel?sort=semver)](https://github.com/prashar32/riskkernel/releases)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

<br/>

<img src="examples/codebase-qa/runaway.gif" alt="A runaway agent halted at its loop budget — RiskKernel returns HTTP 402 at the loop cap" width="820">

<sub><b>A runaway agent, stopped.</b> It loops over a codebase; the deterministic governor halts it at its loop budget with an HTTP&nbsp;402 — no model call escapes the cap. (<a href="examples/codebase-qa">runnable example</a>)</sub>

</div>

---

## The problem

Production AI agents fail in the same handful of ways, every time: **runaway loops**, **surprise token bills**, **no failure recovery**, **no observability**, **no human-in-the-loop**, **no governance**. Agent *frameworks* (LangGraph, CrewAI, AutoGen) orchestrate the reasoning — but none of them ship the guardrails that keep a run from burning $400 in a midnight loop while you sleep.

RiskKernel is a **self-hosted agent reliability runtime** — the deterministic, run-level layer that sits in front of your agents and enforces hard limits. The LLM proposes; deterministic Go code disposes. Every irreversible action is gated.

It is **not** another gateway (LiteLLM/Portkey own routing), **not** another observability dashboard (Langfuse/Phoenix own traces), and **not** a content-guardrails engine (Guardrails AI/NeMo own PII/jailbreak). It interoperates with all of those and competes on the one thing nobody ships in a single self-hosted binary: **deterministic run controls** — the agent SRE layer.

## What it does

| Capability | What it means |
|---|---|
| 💸 **Hard cost ceiling per run** | A run that hits its dollar/token budget is killed cleanly, state persisted. Safe defaults out of the box ([the budget contract](docs/BUDGETS.md)). |
| 🔁 **Hard loop-iteration cap** | No more infinite agent loops. |
| ⏱️ **Hard wall-clock budget** | Runs that exceed their time budget halt. |
| 💾 **Crash-resumable checkpoints** | `SIGKILL` a run; `riskkernel runs resume <id>` picks up from the last step. |
| ✋ **Framework-agnostic approval gates** | Side-effecting tool calls pause for human approval — CLI, local web, or webhook. |
| 🧠 **Memory you own** | Git-native markdown/YAML on your disk; episodic state in your SQLite. |
| 📡 **OpenTelemetry GenAI** | Emits `gen_ai.*` spans to *your* backend (Grafana/SigNoz/Datadog/Langfuse). |

## Three ways to adopt — pick the one that fits

1. **Proxy (zero code).** Set one env var: `OPENAI_BASE_URL=http://localhost:7070/v1`. Every call is intercepted, budgeted, logged, checkpointed, and forwarded to the real provider with your key.
2. **Python SDK (deep control).** Install the SDK (from source today — see the [Quickstart](#quickstart-60-seconds)), then `@governed_run` / `@governed_tool` / `runtime.budget(...)` / `ApprovalGate`. Adapters for the Claude Agent SDK, OpenAI Agents SDK, and LangChain.
3. **OpenTelemetry (universal).** RiskKernel is an OTLP endpoint *and* emitter — govern apps already instrumented with OpenLLMetry / the OpenAI Agents SDK, and export to the backend you already run.

## Quickstart (60 seconds)

Run the daemon with your key (nothing leaves your machine except calls to the
provider you choose). Unconfigured, every run gets a safe default budget —
$5 / 100 loops / 1 hour — so nothing is ever unbounded; here we set an explicit
50¢ cap (see [the budget contract](docs/BUDGETS.md)):

```bash
docker run --rm -p 7070:7070 -v "$PWD/data:/data" \
  -e ANTHROPIC_API_KEY=sk-ant-... \
  -e RISKKERNEL_DEFAULT_DOLLARS=0.50 \
  ghcr.io/prashar32/riskkernel:latest
```

Now put your **existing** OpenAI-compatible app under governance with **one env
var** — no code changes — and point it at a Claude model:

```bash
export OPENAI_BASE_URL=http://localhost:7070/v1
# your app runs unchanged; every call is metered, priced, budget-enforced
```

Or hit it directly and watch the governance headers:

```bash
curl -s -D- http://localhost:7070/v1/chat/completions \
  -H 'content-type: application/json' \
  -H 'X-RiskKernel-Run-Id: demo' \
  -d '{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}'
# → X-RiskKernel-Cost-Usd, X-RiskKernel-Tokens, X-RiskKernel-Step …
# the run is killed with HTTP 402 the moment it exceeds $0.50.
```

Inspect and audit, all on your disk:

```bash
riskkernel runs list                 # every governed run
riskkernel audit export <run-id>     # the cost ledger as JSON
riskkernel audit tools <run-id>      # governed tool calls as JSON
```

Prefer a native binary to Docker? Install the CLI with one command — no clone
needed — and run it:

```bash
go install github.com/prashar32/riskkernel/cmd/riskkernel@latest
riskkernel serve
```

(or `make build` from a clone). Deeper control (loops, checkpoints, approval
gates) is the Python SDK — install it from source (PyPI publish is on the
roadmap):

```bash
pip install "git+https://github.com/prashar32/riskkernel.git#subdirectory=sdks/python"
```

See [`sdks/python`](sdks/python). Trace every run in your own backend:
[`examples/otel`](examples/otel).

Want to *see* the headline feature? [`examples/codebase-qa`](examples/codebase-qa)
is a runnable agent that loops over a codebase until the governor kills it on its
loop/dollar budget — the deterministic kill, end to end, with a real model.

Brand new to the SDK? [`examples/wrap-your-agent`](examples/wrap-your-agent) is the
no-key, two-minute version — a generic Python loop the governor caps at a loop
budget, the deterministic kill with nothing running but the daemon.

On LangChain? [`examples/langchain`](examples/langchain) wraps a LangChain loop
with the callback handler and caps it at a loop budget — also key-free.

## Design principles

- **Deterministic core in Go.** All enforcement (budgets, kill switches, gating, routing, retries, checkpointing) lives in compiled, statically-typed code — never in an LLM.
- **No telemetry, ever.** Nothing phones home. It's a verifiable promise; see [`SECURITY.md`](SECURITY.md).
- **Your keys, your infra.** Secrets come from env / `.env` / OS-keyring, never stored in state, never logged.
- **Near-zero adoption friction.** Every decision is judged by *"how few changes must an existing user make?"* One env var is the gold standard.
- **Backwards compatibility is sacred.** Self-hosted users can't be force-migrated. See [`COMPATIBILITY.md`](COMPATIBILITY.md).

## ⭐ If this is useful

RiskKernel is a one-person, build-in-public project. If the idea resonates — or you
just want runaway agents to stop quietly burning money — a star genuinely helps:
it's how other people find it, and it tells me which parts are worth building next.

And if you actually run it, I'd love to hear where the guardrails are too strict or
too loose — [open an issue](https://github.com/prashar32/riskkernel/issues). That
feedback shapes the roadmap directly.

## Contributing

Contributions are welcome. Start with [`ARCHITECTURE.md`](ARCHITECTURE.md) for a
map of the codebase (and a "where do I code?" table), then
[`CONTRIBUTING.md`](CONTRIBUTING.md) for dev setup and the PR flow. We use GitHub
Flow — fork, branch off `main`, open a PR; CI (`build & test` + `CodeQL`) and a
maintainer review gate every merge.

Good places to start: issues tagged [`good first issue`](https://github.com/prashar32/riskkernel/labels/good%20first%20issue).
Be excellent to each other — see the [Code of Conduct](CODE_OF_CONDUCT.md).

## License

[Apache-2.0](LICENSE). The runtime stays permissive, forever.
