# Crash-resume — the moat

A long agent run that crashes shouldn't restart from zero, re-doing work you've
already paid for. RiskKernel persists enough state that a killed run **resumes from
where it left off, without re-spending** — the budget meter is restored exactly, so
a crash can't hand the run a fresh budget, and the agent picks its work back up from
its last checkpoint.

This is the differentiator. This guide is the full model: what's restored, how to
write a resumable agent, the exact-once guarantee, and the one thing that's *your*
responsibility (idempotent side effects).

> Want to just *see* it? [`examples/kill-9-resume`](../examples/kill-9-resume)
> `kill -9`s the daemon mid-run and resumes — `./demo.sh` scripts the whole thing.

## The model — two cooperating halves

**1. The daemon (automatic).** As a run progresses, the daemon writes its state to
the SQLite file you own — the run row (budget, cumulative usage, status), an
append-only cost ledger, a step row per iteration, and a **checkpoint** after every
model call (and every `run.checkpoint(...)` you make). On startup it **reloads**
every non-terminal run, reconstructing the governor with the budget and usage it had
already spent. Enforcement just continues: a `SIGKILL` can't reset the meter.

**2. The agent (cooperative).** The runtime can restore the *budget* on its own, but
only your agent knows what *work* it had done. So you **checkpoint your progress**
(a cursor, the messages so far — whatever you need to continue) and, on resume,
**re-attach to the same run** and read that checkpoint back.

```python
import riskkernel as rk
rt = rk.Runtime()

with rt.resume_run(run_id) as run:        # attach to the existing run (no new run, no cancel)
    cp = run.latest_checkpoint()          # the state you saved before the crash
    start = cp["payload"]["cursor"] if cp else 0
    for i in range(start, total):         # skip the steps you already finished
        run.step()                        # counts against the SAME budget
        ... do the work ...
        run.checkpoint("progress", {"cursor": i + 1})
```

The run id is the only thing your agent must keep across a restart — persist it
wherever you like (a file, your job queue, a DB row). [`resume_run`](../sdks/python/README.md#resume-after-a-crash)
is the SDK entry point; over the API it's just `GET /v1/runs/{id}` +
`GET /v1/checkpoints/{id}` and reusing the id.

## What's restored

Everything the budget enforces, exactly as it stood:

| Restored | Not restored |
|---|---|
| Spent **tokens, dollars, loops** | In-flight work the agent didn't checkpoint |
| The per-run **budget** (and `policyRef`) | The wall-clock **time** budget's clock — it restarts (it meters one active session, not downtime) |
| The cost **ledger** and step history | — |
| Your last **checkpoint payload** | — |

A resumed run enforces against what it had *already* spent, so it can't overspend by
restarting: if it had burned `$4.20` of a `$5` budget, it resumes at `$4.20`, not
`$0`.

## Exact-once, even mid-step

A step is counted (and the run row persisted) in `run.step()` *before* its work runs
and checkpoints. If the daemon dies in that window, the run row is briefly one loop
ahead of the last durable checkpoint. On restart the daemon **reloads from the last
checkpoint**, rolling that partial step back — so resume re-attempts it and the
**loop and dollar budgets are charged exactly once**, never twice.

What *is* re-attempted is the interrupted step's **work**: at most one step runs
again (never the whole run). Which leads to the one rule that's yours, not ours.

## Your part: make side-effecting steps idempotent

Because the interrupted step can run a second time, a step that *does something to
the outside world* — create a PR, charge a card, send an email — must be safe to
re-run. RiskKernel helps but can't do this for you:

- **It gates side effects.** Side-effecting tool calls route through the
  [approval gate](../sdks/python/README.md#human-in-the-loop-tools); a human (or
  policy) decides before they run.
- **It records every call.** The cost ledger and the `tool_calls` audit trail
  (`riskkernel audit tools <run-id>`) show exactly what ran, so you can reconcile.

Design the work itself to be idempotent — an idempotency key, a check-before-write,
or a "have I already done step N?" guard keyed on your checkpoint cursor. The safest
agents checkpoint *after* the side effect commits, so a re-attempt sees it's done.

## What can't be resumed

Resume is for *interrupted* runs, not *finished* ones. A run that **halted on its
budget** (`token_/dollar_/loop_/time_budget_exceeded`) or was **cancelled** is
terminal — it stays halted. If a budget halt is what you hit, decide deliberately and
start a **new** run with a bigger budget; RiskKernel won't silently keep spending.

Check a run's resumability without starting it:

```bash
riskkernel runs resume <run-id>     # reports: spent / budget / remaining / last checkpoint
riskkernel runs list                # every run, its status, and what it's spent
```

## Stability

Once you depend on resume, the on-disk checkpoint format is a compatibility surface
— it's versioned and forward-migratable like the rest of the SQLite schema (see
[`COMPATIBILITY.md`](../COMPATIBILITY.md)). The `Budget` semantics, the
`resume_run` / checkpoint SDK methods, and the `/v1/checkpoints/{id}` and
`/v1/runs/{id}` endpoints are stable across v0.x minor versions.
