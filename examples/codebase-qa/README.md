# codebase-qa — watch RiskKernel kill a runaway agent

A tiny, **real** codebase Q&A agent governed by RiskKernel. It's a plain
ReAct-style loop (no RAG, no vector DB, no framework): each step it asks the model
what to do, READs a file or ANSWERs, repeats. Every model call goes through the
RiskKernel proxy, so the **deterministic governor meters cost and enforces a hard
per-run loop / dollar / time budget** around the loop.

Two modes:
- **`--mode normal`** — a sensible question that finishes within budget. Prints each
  step, the token count, and the running USD cost, then the answer. (Happy path +
  full per-step observability.)
- **`--mode runaway`** — the *same* agent with a deliberately weak stopping
  condition (it's told to re-read every file before answering), so it loops. The
  counters climb each step, then the governor's loop budget **halts it cleanly**.
  The kill comes from RiskKernel, not from the script — this is the money shot.

It uses your own `ANTHROPIC_API_KEY` (BYO key) and nothing else.

## Run it in 60 seconds

```bash
# 1. start the daemon with your key (Docker — or `riskkernel serve` from a binary)
docker run --rm -p 7070:7070 -e ANTHROPIC_API_KEY=sk-ant-... ghcr.io/prashar32/riskkernel:latest

# 2. in another terminal, install the SDK and run the agent
cd examples/codebase-qa
pip install -r requirements.txt          # installs the local RiskKernel SDK (stdlib-only)

python agent.py --mode normal            # happy path — answers within budget
python agent.py --mode runaway           # the money shot — governor kills the loop
```

By default it answers questions about the bundled `./sample` todo app. Point it at
any codebase with `--dir ../../internal/governor --question "What does this enforce?"`.

## Output

Real runs against `claude-haiku-4-5-20251001` (run-ids and exact token/cost numbers
vary; the structure is the point).

`--mode normal` — reads what it needs, then answers, well under budget:

```
▶ codebase-qa  mode=normal  dir=.../sample  model=claude-haiku-4-5-20251001
  budget: loops=10 dollars=$0.1 seconds=120
  question: What does this codebase do and where is the entrypoint?

  run: d3b4e0db-db34-4bd8-9efc-39171bc782a1

  step  1 │ READ main.py                 │ tokens=  147 │ cost=$0.0002
  step  2 │ ANSWER                       │ tokens=  462 │ cost=$0.0007

✅ completed within budget.

— Answer —
This codebase is a todo CLI application. The entrypoint is main.py, which loads
configuration, initializes a TodoStore database, adds a sample todo item ("write
the RiskKernel demo"), and then lists and renders all todos to the console.
```

`--mode runaway` — the money shot. Counters climb each step, then the governor
refuses the over-budget step:

```
▶ codebase-qa  mode=runaway  dir=.../sample  model=claude-haiku-4-5-20251001
  budget: loops=4 dollars=$0.05 seconds=120
  question: What does this codebase do and where is the entrypoint?

  run: 5b9a4efa-b714-46bd-9c50-f517d74557e9

  step  1 │ READ README.md               │ tokens=  187 │ cost=$0.0002
  step  2 │ READ main.py                 │ tokens=  453 │ cost=$0.0005
  step  3 │ READ config.py               │ tokens=  833 │ cost=$0.0009
  step  4 │ READ models.py               │ tokens= 1294 │ cost=$0.0014

🛑 RiskKernel refused the next step — reason: loop_budget_exceeded
   ── final ledger (enforced by the governor) ──
     steps (loops) :      4   (budget: 4)
     tokens        :   1294
     cost          : $  0.0014   (budget: $0.05)
     run id        : 5b9a4efa-b714-46bd-9c50-f517d74557e9
   The agent would have looped forever; the governor capped it at 4 steps.
```

The 5th call never reaches the model: its `BeginStep` is rejected by the governor
with HTTP `402 loop_budget_exceeded`, which the SDK surfaces as `BudgetExceeded`.
That's the deterministic kill — the script never decides to stop.

## Tuning for a recording

All knobs are commented constants at the top of `agent.py`:
- `RUNAWAY_BUDGET` (`loops=4`) — lower it for a faster kill, raise it for more
  climbing steps before the halt.
- `MODEL`, `MAX_OUTPUT_TOKENS`, `MAX_FILE_CHARS` — keep the demo cheap and fast.

The kill is **always** the real governor returning HTTP `402` from the proxy; the
script never fakes it.

## Same agent, zero code — via the proxy

You don't need this SDK at all to get governance. Any app that speaks the OpenAI
API is governed by changing **one env var** to point at the daemon:

```bash
export OPENAI_BASE_URL=http://localhost:7070/v1
# your existing agent runs unchanged; set X-RiskKernel-Run-Id to group its calls
# into one budgeted run. Same loop/dollar/time enforcement, zero code changes.
```

This example uses the Python SDK because it shows the per-step ledger and the
`BudgetExceeded` halt explicitly — but the proxy path is the zero-code on-ramp.

## What about approvals?

This agent is read-only (it only READs files), so the human-in-the-loop approval
gate isn't exercised here. For a side-effecting tool (shell, write, deploy) you'd
wrap it with `@riskkernel.governed_tool(side_effect="write")` or call
`run.approve(...)`, and the call would pause for approval — see the SDK README.
