# langchain — stop a runaway LangChain agent

Wrap a LangChain LLM loop with RiskKernel's callback handler and the
**deterministic governor caps it** — one governed step per model call, hard-stopped
at the loop budget. The halt propagates out of `llm.invoke()` and ends the chain.
The kill comes from RiskKernel, not from the script.

**No API key, no model call.** This uses LangChain's `FakeListLLM` so the loop
enforcement runs with nothing but `riskkernel serve`. (Add a real model — and the
dollar/token ceiling — with a few lines; see [below](#add-the-dollar--token-ceiling-real-model).)

## Run it in 60 seconds

```bash
# 1. start the daemon — no key needed for this demo
docker run --rm -p 7070:7070 ghcr.io/prashar32/riskkernel:latest

# 2. in another terminal, install langchain-core + the SDK, and run it
cd examples/langchain
pip install -r requirements.txt
python agent.py
```

## What you'll see

A real run (the run-id varies; the structure is the point):

```
▶ langchain   loop budget = 6   (enforced by the Go governor)
  run id: 31ab03d4-e7e4-47ad-bb74-6402999ca4fd

  step  1 │ LLM call allowed by the governor
  step  2 │ LLM call allowed by the governor
  step  3 │ LLM call allowed by the governor
  step  4 │ LLM call allowed by the governor
  step  5 │ LLM call allowed by the governor
  step  6 │ LLM call allowed by the governor

🛑 RiskKernel halted the LangChain run — reason: loop_budget_exceeded
   ── final ledger (enforced by the governor) ──
     LLM calls (loops) :    6   (budget: 6)
     run id            : 31ab03d4-e7e4-47ad-bb74-6402999ca4fd
   The agent would have looped forever; the governor capped it — and the
   halt propagated out of llm.invoke(), ending the chain.
```

The 7th `llm.invoke()` is refused: the handler's `on_llm_start` ticks a governed
step, the daemon returns HTTP `402 loop_budget_exceeded`, and that surfaces as
`rk.BudgetExceeded` — which propagates out of LangChain and stops the loop.

## Wrapping your own LangChain agent

Two objects: the handler, and passing it as a callback.

```python
import riskkernel as rk
from riskkernel.adapters.langchain import RiskKernelCallbackHandler

rt = rk.Runtime()                                       # http://localhost:7070
with rt.governed_run(budget=rt.budget(loops=50, seconds=600)) as run:
    handler = RiskKernelCallbackHandler(run)            # one governed step per LLM call
    while not done:
        llm.invoke(prompt, config={"callbacks": [handler]})   # raises rk.BudgetExceeded at the cap
        ...                                             # your existing chain / tools
```

Works the same with `AgentExecutor`, `Runnable` chains, or LangGraph — anywhere you
can pass `callbacks`. The handler also gates tools on human approval when you
construct it with `RiskKernelCallbackHandler(run, gate_tools=True)`.

> The handler sets `raise_error = True` so the budget halt actually propagates —
> LangChain otherwise swallows exceptions raised inside a callback and the chain
> would keep spending past budget.

## Add the dollar / token ceiling (real model)

The handler caps **loops and time**. To also cap **dollars and tokens**, route the
model through the run's proxy so every call is priced from real provider usage and
the dollar budget halts the run:

```python
from langchain_openai import ChatOpenAI     # speaks the OpenAI API to the proxy

cfg = run.proxy_config()
llm = ChatOpenAI(
    base_url=cfg["base_url"],               # http://localhost:7070/v1
    default_headers=cfg["headers"],         # groups calls into this governed run
    api_key="sk-unused",                    # the real provider key lives in the daemon
    model="claude-sonnet-4-5",
)
# budget=rt.budget(loops=50, dollars=1.00, seconds=600)
# the dollar ceiling trips at the proxy with HTTP 402; the loop/time ceiling
# trips in the handler. Start the daemon with your ANTHROPIC_API_KEY.
```

For the real-model dollar kill end to end — the cost ledger climbing each step
until the governor stops it — see [`examples/codebase-qa`](../codebase-qa).

## Tuning for a recording

- `LOOP_BUDGET` (default `6`) — lower it for a faster kill, raise it for more
  steps before the halt.

Nothing about the kill is faked: it's the daemon returning HTTP `402` before the
over-budget call, surfaced through LangChain's own callback path.
