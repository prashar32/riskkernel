# wrap-your-agent — put your loop under RiskKernel

The smallest possible governed run: a plain Python `while` loop that would spin
forever, wrapped so RiskKernel's **deterministic governor hard-stops it at a loop
budget**. The kill comes from the Go core, not from the script.

**No API key, no model call.** The loop budget is enforced in the daemon before
each iteration, so you can watch the governor stop a runaway agent with nothing
running but `riskkernel serve`. It's the loop-killer reduced to its essence — and
the starting point for putting your *own* agent under governance.

## Run it in 60 seconds

```bash
# 1. start the daemon — no key needed for this demo (the loop cap is enforced
#    without any model call). Docker, or `riskkernel serve` from a binary:
docker run --rm -p 7070:7070 ghcr.io/prashar32/riskkernel:latest

# 2. in another terminal, install the SDK and run the loop
cd examples/wrap-your-agent
pip install -r requirements.txt        # the RiskKernel SDK (stdlib-only)
python agent.py                        # watch the governor cap the loop
```

## What you'll see

A real run (the run-id varies; the structure is the point):

```
▶ wrap-your-agent   loop budget = 8   (enforced by the Go governor)
  run id: f6339d2b-07db-4e4e-b7de-d4754163d759

  step  1 │ working… (your model / tool call goes here)
  step  2 │ working… (your model / tool call goes here)
  step  3 │ working… (your model / tool call goes here)
  step  4 │ working… (your model / tool call goes here)
  step  5 │ working… (your model / tool call goes here)
  step  6 │ working… (your model / tool call goes here)
  step  7 │ working… (your model / tool call goes here)
  step  8 │ working… (your model / tool call goes here)

🛑 RiskKernel halted the run — reason: loop_budget_exceeded
   ── final ledger (enforced by the governor) ──
     steps (loops) :    8   (budget: 8)
     run id        : f6339d2b-07db-4e4e-b7de-d4754163d759
   The loop would have run forever; the governor capped it — deterministically,
   before the next iteration, with no LLM in the decision.
```

The 9th `run.step()` never returns: the daemon refuses it with HTTP
`402 loop_budget_exceeded`, which the SDK raises as `rk.BudgetExceeded`. The script
never decides to stop — the governor does.

## Wrapping your own agent

Putting an existing loop under governance is three lines around the loop you
already have:

```python
import riskkernel as rk

rt = rk.Runtime()                                       # points at http://localhost:7070
with rt.governed_run(budget=rt.budget(loops=50, dollars=5, seconds=600)) as run:
    while not done:
        run.step()                                      # raises rk.BudgetExceeded at the cap
        ...                                             # your existing reasoning + tool calls
```

`budget=` takes any mix of `loops`, `dollars`, `tokens`, `seconds` — the first to
trip halts the run (see [the budget contract](../../docs/BUDGETS.md)). This example
caps **loops** because that needs no key. To meter **dollars and tokens** too,
route your model client through the run's proxy so every call is priced and
counted under the same budget:

```python
cfg = run.proxy_config()
#   cfg["base_url"] -> http://localhost:7070/v1   (point your OpenAI/Anthropic client here)
#   cfg["headers"]  -> {"X-RiskKernel-Run-Id": run.id}
```

Prefer a decorator? `@rk.governed_run(budget=rk.Budget(loops=50))` wraps a whole
function as one governed run, and `rk.current_run()` fetches the run inside it.

## Tuning for a recording

All knobs are constants at the top of `agent.py`:

- `LOOP_BUDGET` (default `8`) — lower it for a faster kill, raise it for more
  steps before the halt.
- `WORK_SECONDS` — the simulated per-iteration delay; set `0` for an instant run.

Nothing about the kill is faked: lower the budget and it halts sooner; remove the
budget entirely and the `while True` really would run forever. The halt is always
the real governor returning HTTP `402` from the daemon.

## Want a real model in the loop?

This example stays key-free to isolate the loop budget. For the same agent making
**real model calls** through the proxy — with the dollar ledger climbing each step
until the governor kills it — see [`examples/codebase-qa`](../codebase-qa).
