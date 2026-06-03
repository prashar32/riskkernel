#!/usr/bin/env python3
"""wrap-your-agent — put an existing Python agent loop under RiskKernel.

The smallest possible governed run: a plain Python ``while`` loop that would spin
forever, wrapped so RiskKernel's deterministic governor hard-stops it at a loop
budget. The kill comes from the Go core (the daemon), not from this script.

No API key, no model call. The loop budget is enforced in the daemon before each
iteration, so you can watch the governor stop a runaway agent with nothing running
but ``riskkernel serve``. It's the loop-killer reduced to its essence — and the
starting point for putting your *own* agent under governance. (Add real model
calls, and dollar/token budgets, via ``run.proxy_config()`` — see the README.)

Run it:
    riskkernel serve            # in another terminal — no key needed for this demo
    python agent.py             # the governor caps the loop and halts the run

The governance bits are exactly three calls: ``rt.governed_run(budget=...)`` to
open the run, ``run.step()`` at the top of each iteration, and catching
``rk.BudgetExceeded`` when the governor refuses the next step.
"""

from __future__ import annotations

import os
import time

import riskkernel as rk

# ─────────────────────────────────────────────────────────────────────────────
# Knobs — tune for a clean recording. Nothing here fakes the kill: the governor
# (inside the daemon) does the enforcing, deterministically, before each step.
# ─────────────────────────────────────────────────────────────────────────────
DAEMON_URL = os.environ.get("RISKKERNEL_BASE_URL", "http://localhost:7070")
LOOP_BUDGET = 8        # max loop iterations the governor will allow for the run
WORK_SECONDS = 0.25    # simulated "thinking" / tool time per iteration (0 = instant)


def do_one_step(step: int) -> None:
    """Stand-in for your real per-iteration work — an LLM call, a tool call, a
    retrieval. Here it only prints and sleeps, so the demo needs no API key."""
    time.sleep(WORK_SECONDS)
    print(f"  step {step:>2} │ working… (your model / tool call goes here)")


def main() -> int:
    rt = rk.Runtime(base_url=DAEMON_URL)
    print(f"▶ wrap-your-agent   loop budget = {LOOP_BUDGET}   (enforced by the Go governor)")

    try:
        # 1) open a governed run with a hard budget …
        with rt.governed_run(name="wrap-your-agent",
                             budget=rt.budget(loops=LOOP_BUDGET)) as run:
            print(f"  run id: {run.id}\n")
            step = 0
            while True:                       # a deliberately runaway agent loop
                try:
                    run.step()                # 2) … and call step() before each iteration
                except rk.BudgetExceeded as halt:
                    _report_halt(run, halt)   # 3) the governor refused the next step
                    return 0
                step += 1
                do_one_step(step)
    except rk.APIError as e:
        if e.code == "connection_error":
            print(f"\n✗ can't reach the daemon at {DAEMON_URL} — start it first:")
            print("    riskkernel serve")
            print("    # or: docker run --rm -p 7070:7070 ghcr.io/prashar32/riskkernel:latest")
            return 1
        raise


def _report_halt(run: rk.Run, halt: rk.BudgetExceeded) -> None:
    """Print the final, governor-enforced ledger for the halted run."""
    usage = run.status().get("usage", {})
    print(f"\n🛑 RiskKernel halted the run — reason: {halt.reason}")
    print("   ── final ledger (enforced by the governor) ──")
    print(f"     steps (loops) : {usage.get('loops', 0):>4}   (budget: {LOOP_BUDGET})")
    print(f"     run id        : {run.id}")
    print("   The loop would have run forever; the governor capped it — deterministically,")
    print("   before the next iteration, with no LLM in the decision.")


if __name__ == "__main__":
    raise SystemExit(main())
