# autogen — stop a runaway AutoGen agent

Wrap an AutoGen model client with RiskKernel's `GovernedChatCompletionClient` and
the **deterministic governor caps the agent** — one governed step per model request,
hard-stopped at the loop budget. The halt propagates out of `agent.run()` and ends
the run. The kill comes from RiskKernel, not from the script.

Targets the actively maintained **AutoGen v0.4+** line (`autogen-agentchat` /
`autogen-core`), not the legacy `pyautogen` 0.2 API.

**No API key, no model call.** This uses a tiny local model-client stub that always
returns the same tool call (so the agent loops forever), so the loop enforcement
runs with nothing but `riskkernel serve`. (Add a real model — and the dollar/token
ceiling — with a few lines; see [below](#add-the-dollar--token-ceiling-real-model).)

## Run it in 60 seconds

```bash
# 1. start the daemon — no key needed for this demo
docker run --rm -p 7070:7070 ghcr.io/prashar32/riskkernel:latest

# 2. in another terminal, install autogen + the SDK, and run it
cd examples/autogen
pip install -r requirements.txt
python agent.py
```

## What you'll see

A real run (the run-id varies; the structure is the point):

```
▶ autogen   loop budget = 6   (enforced by the Go governor)
  run id: 31ab03d4-e7e4-47ad-bb74-6402999ca4fd

🛑 RiskKernel halted the AutoGen run — reason: loop_budget_exceeded
   ── final ledger (enforced by the governor) ──
     model calls (loops) :    7   (budget: 6)
     run id              : 31ab03d4-e7e4-47ad-bb74-6402999ca4fd
   The agent would have looped forever; the governor capped it — and the
   halt propagated out of agent.run().
```

The 7th model request is refused: the wrapper's `create()` ticks a governed step,
the daemon returns HTTP `402 loop_budget_exceeded`, and that surfaces as
`rk.BudgetExceeded` — which propagates out of AutoGen and stops the agent.

## Wrapping your own AutoGen agent

One object: wrap the model client, then use your agent as-is.

```python
import riskkernel as rk
from riskkernel.adapters.autogen import GovernedChatCompletionClient
from autogen_agentchat.agents import AssistantAgent

rt = rk.Runtime()                                       # http://localhost:7070
with rt.governed_run(budget=rt.budget(loops=50, seconds=600)) as run:
    client = GovernedChatCompletionClient(model_client, run)  # one step per model call
    agent = AssistantAgent("assistant", model_client=client)  # otherwise unchanged
    await agent.run(task="...")                         # raises rk.BudgetExceeded at the cap
```

Gate side-effecting tools on human approval by constructing it with
`GovernedChatCompletionClient(model_client, run, gate_tools=True)` — each tool the
model requests routes through the approval gate before the agent can run it.

## Teams re-raise the halt as a RuntimeError

A single agent run propagates the typed `rk.BudgetExceeded` directly. But a **team**
(`RoundRobinGroupChat`, `SelectorGroupChat`, …) catches an agent's exception and
re-raises it to the caller as a plain `RuntimeError` (its container serializes the
error and the team's `run()` re-raises `RuntimeError(str(error))`). The run still
halts — it is **not** swallowed — but the type is lost. Wrap the team call in
`governed_run_errors()` to get the typed exception back:

```python
from riskkernel.adapters.autogen import governed_run_errors

with governed_run_errors():
    await team.run(task="...")          # re-raises rk.BudgetExceeded, not RuntimeError
```

## Add the dollar / token ceiling (real model)

The wrapper caps **loops and time**. To also cap **dollars and tokens**, route the
real model client through the run's proxy so every call is priced from real provider
usage and the dollar budget halts the run:

```python
from autogen_ext.models.openai import OpenAIChatCompletionClient  # speaks OpenAI to the proxy

cfg = run.proxy_config()
real = OpenAIChatCompletionClient(
    model="claude-sonnet-4-5",
    base_url=cfg["base_url"],               # http://localhost:7070/v1
    api_key="sk-unused",                    # the real provider key lives in the daemon
    default_headers=cfg["headers"],         # groups calls into this governed run
)
client = GovernedChatCompletionClient(real, run)
# budget=rt.budget(loops=50, dollars=1.00, seconds=600)
# the dollar ceiling trips at the proxy with HTTP 402; the loop/time ceiling
# trips in the wrapper. Start the daemon with your ANTHROPIC_API_KEY.
```

## Tuning for a recording

- `LOOP_BUDGET` (default `6`) — lower it for a faster kill, raise it for more
  steps before the halt.

Nothing about the kill is faked: it's the daemon returning HTTP `402` before the
over-budget model request, surfaced through AutoGen's own model-client path.
